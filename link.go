package link

import (
    "bytes"
    "context"
    "encoding/binary"
    "errors"
    "io"
    "math"
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

    manager       *Manager // *
    readChan      chan *Packet
    readChanClose bool

    closed    bool
    readMutex sync.Mutex
    readBuf   *bytes.Buffer // *
    locker    sync.Mutex    // *
    cond      *sync.Cond    // *

    semaphore     *semaphore.Weighted // wind
    ctx           context.Context     // *
    ctxCancelFunc context.CancelFunc  // *
}

func (l *Link) closeReadChan() {
    if !l.readChanClose {
        close(l.readChan)
    }
}

func (l *Link) removeFromManager() {
    delete(l.manager.m, l.ID)
}

func (l *Link) read() {
    for packet := range l.readChan {
        if l.Status == CLOSED || l.Status == RST {
            l.ctxCancelFunc()
            l.closeReadChan()
            l.removeFromManager()
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
                l.closeReadChan()
                l.removeFromManager()
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
            l.closeReadChan()
            l.removeFromManager()
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

    size := make([]byte, 4)
    binary.BigEndian.PutUint32(size, uint32(n))
    ack := Packet{
        ID:      l.ID,
        ACK:     true,
        Payload: size,
        Length:  4,
    }

    l.manager.writeChan <- &ack

    return n, err
}

func (l *Link) Write(b []byte) (int, error) {
    switch l.Status {
    case ESTAB, CLOSE_WAIT:
        if b == nil {
            return 0, nil
        }

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

            l.manager.writeChan <- &packet

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

        l.manager.writeChan <- &fin

        l.Status = FIN_WAIT
        l.ctxCancelFunc()
        return nil

    case CLOSE_WAIT:
        fin := Packet{
            ID:  l.ID,
            FIN: true,
        }

        l.manager.writeChan <- &fin

        l.Status = CLOSED
        l.ctxCancelFunc()
        l.closeReadChan()
        l.removeFromManager()
        return nil

    case FIN_WAIT:
        return nil

    default:
        l.ctxCancelFunc()
        l.closeReadChan()
        l.removeFromManager()
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

        l.manager.writeChan <- &rst
        l.Status = RST
        l.ctxCancelFunc()
        l.closeReadChan()
        l.removeFromManager()
        return RST
    }
}
