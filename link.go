package link

import (
    "context"
    "encoding/binary"
    "errors"
    "fmt"
    "math"
    "net"
    "sync"

    "github.com/satori/go.uuid"
    "golang.org/x/sync/semaphore"
)

type Link struct {
    ID     uuid.UUID
    Status error

    mutex    sync.Mutex
    lowLevel net.Conn

    closed       bool
    readableChan chan *Packet
    writtenSize  chan int64

    semaphore     *semaphore.Weighted // wind
    ctx           context.Context
    ctxCancelFunc context.CancelFunc
}

func (l *Link) closeReadChan() {
    if !l.closed {
        l.closed = true
        close(l.readableChan)
    }
}

func (l *Link) read() {
    for true {
        if l.Status == CLOSED || l.Status == RST {
            break
        }

        var (
            b      = make([]byte, HeaderLength)
            length int
        )
        for length < HeaderLength {
            n, err := l.lowLevel.Read(b[length:])
            if err != nil {
                l.Status = RST
                l.ctxCancelFunc()
                l.closeReadChan()

                break
            }
            length += n
        }

        payloadLength := binary.BigEndian.Uint16(b[HeaderLength-2:])

        if payloadLength != 0 {
            length = 0
            payload := make([]byte, payloadLength)

            for length < int(payloadLength) {
                n, err := l.lowLevel.Read(payload[length:])
                if err != nil {
                    l.Status = RST
                    l.closeReadChan()
                    l.ctxCancelFunc()
                    break
                }
                length += n
            }

            b = append(b, payload...)
        }

        packet, err := Decode(b)
        if err != nil {
            l.Status = RST
            rst := Packet{ID: l.ID, RST: true}
            l.lowLevel.Write(rst.Bytes())
            l.closeReadChan()
            l.ctxCancelFunc()
            break
        }

        if packet.PSH {
            l.readableChan <- packet
            continue
        }

        if packet.FIN {
            switch l.Status {
            case ESTAB:
                l.Status = CLOSE_WAIT
                l.closeReadChan()
                continue

            case FIN_WAIT:
                l.Status = CLOSED
                l.closeReadChan()
                l.ctxCancelFunc()
                break
            }
        }

        if packet.ACK {
            l.semaphore.Release(<-l.writtenSize)
            continue
        }

        if packet.RST {
            l.Status = RST
            l.closeReadChan()
            l.ctxCancelFunc()
            break
        }
    }
}

func (l *Link) Read() (packet *Packet, err error) {
    switch l.Status {
    case ESTAB, FIN_WAIT:
        packet, err = <-l.readableChan, nil

        if packet == nil {
            return nil, l.Status
        }

    default:
        return nil, l.Status
    }

    go func() {
        ack := Packet{
            ID:  l.ID,
            ACK: true,
        }
        _, err := l.lowLevel.Write(ack.Bytes())
        if err != nil {
            l.Status = RST
            l.ctxCancelFunc()
            l.closeReadChan()
        }
    }()

    return
}

func (l *Link) Write(b []byte) (int, error) {
    switch l.Status {
    case ESTAB, CLOSE_WAIT:
        if len(b) > math.MaxUint16 {
            return 0, fmt.Errorf("bytes length %d > %d", len(b), math.MaxUint16)
        }

        packet := Packet{
            ID:      l.ID,
            PSH:     true,
            Payload: b,
            Length:  uint16(len(b)),
        }

        l.semaphore.Acquire(l.ctx, int64(len(b)))
        select {
        case <-l.ctx.Done():
            return 0, errors.New("link can't write again")

        default:
        }

        l.writtenSize <- int64(len(b))

        _, err := l.lowLevel.Write(packet.Bytes())
        if err != nil {
            l.Status = RST
            l.closeReadChan()
            l.ctxCancelFunc()
            return 0, RST
        }

        return len(b), nil

    default:
        return 0, l.Status
    }
}

func (l *Link) CloseWrite() error {
    switch l.Status {
    case ESTAB:
        fin := Packet{
            ID:  l.ID,
            FIN: true,
        }

        _, err := l.lowLevel.Write(fin.Bytes())
        if err != nil {
            l.Status = RST
            l.closeReadChan()
            l.ctxCancelFunc()
            return RST
        }

        l.Status = FIN_WAIT
        l.ctxCancelFunc()
        return nil

    case CLOSE_WAIT:
        fin := Packet{
            ID:  l.ID,
            FIN: true,
        }

        _, err := l.lowLevel.Write(fin.Bytes())
        if err != nil {
            l.Status = RST
            l.closeReadChan()
            l.ctxCancelFunc()
            return RST
        }

        l.Status = CLOSED
        l.ctxCancelFunc()
        return nil

    default:
        return l.Status
    }
}

func (l *Link) Close() error {
    switch l.Status {
    case CLOSE_WAIT:
        return l.CloseWrite()

    default:
        rst := Packet{
            ID:  l.ID,
            RST: true,
        }

        l.lowLevel.Write(rst.Bytes())
        l.Status = RST
        l.closeReadChan()
        l.ctxCancelFunc()
        return RST
    }
}
