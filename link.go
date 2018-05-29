package link

import (
    "bytes"
    "context"
    "encoding/binary"
    "errors"
    "io"
    "math"
    "net"
    "sync"

    "github.com/satori/go.uuid"
    "golang.org/x/sync/semaphore"
)

const (
    maxBufSize = 1 << 18
)

type Link struct {
    ID     uuid.UUID // *
    Status error     // *

    lowLevel net.Conn // *

    closed    bool
    readMutex sync.Mutex
    readBuf   *bytes.Buffer // *
    locker    sync.Mutex    // *
    cond      *sync.Cond    // *

    semaphore     *semaphore.Weighted // wind
    ctx           context.Context     // *
    ctxCancelFunc context.CancelFunc  // *
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
                l.cond.Signal()
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
                    l.ctxCancelFunc()
                    l.cond.Signal()
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
            l.ctxCancelFunc()
            l.cond.Signal()
            break
        }

        if packet.PSH {
            l.readMutex.Lock()
            l.readBuf.Write(packet.Payload)
            l.readMutex.Unlock()

            l.cond.Signal()
            continue
        }

        if packet.FIN {
            switch l.Status {
            case ESTAB:
                l.Status = CLOSE_WAIT
                continue

            case FIN_WAIT:
                l.Status = CLOSED
                l.ctxCancelFunc()
                break
            }
        }

        if packet.ACK {
            size := binary.BigEndian.Uint32(packet.Payload)
            l.semaphore.Release(int64(size))
            continue
        }

        if packet.RST {
            l.Status = RST
            l.ctxCancelFunc()
            l.cond.Signal()
            break
        }
    }
}

func (l *Link) Read(b []byte) (n int, err error) {
    switch l.Status {
    case RST:
        return 0, RST

    case CLOSE_WAIT, CLOSED:
        if l.readBuf.Len() == 0 {
            return 0, io.EOF
        } else {
            n, err = l.readBuf.Read(b)
            return
        }

    case ESTAB, FIN_WAIT:
        if l.readBuf.Len() == 0 {
            l.cond.Wait()
        }

        if l.Status == RST {
            return 0, RST
        }

        l.readMutex.Lock()
        n, err = l.readBuf.Read(b)
        l.readMutex.Unlock()
    }

    go func() {
        size := make([]byte, 4)
        binary.BigEndian.PutUint32(size, uint32(n))
        ack := Packet{
            ID:      l.ID,
            ACK:     true,
            Payload: size,
            Length:  4,
        }
        _, err := l.lowLevel.Write(ack.Bytes())
        if err != nil {
            l.Status = RST
            l.ctxCancelFunc()
        }
    }()

    return n, err
}

func (l *Link) Write(b []byte) (int, error) {
    switch l.Status {
    case ESTAB, CLOSE_WAIT:
        var (
            lastB   []byte
            smaller bool
        )

        for {
            if len(b) > math.MaxUint16 {
                b, lastB = b[:math.MaxUint16], b[math.MaxUint16:]
            } else {
                smaller = true
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

            _, err := l.lowLevel.Write(packet.Bytes())
            if err != nil {
                l.Status = RST
                l.ctxCancelFunc()
                return 0, RST
            }

            if smaller {
                return len(b), nil
            } else {
                b = lastB
            }
        }

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
        l.ctxCancelFunc()
        return RST
    }
}
