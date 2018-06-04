package link

import (
    "bytes"
    "context"
    "sync"

    "github.com/satori/go.uuid"
    "golang.org/x/sync/semaphore"
    "io"
    "encoding/binary"
    "log"
    "fmt"
)

const (
    maxBufSize = 1 << 18
)

type Link struct {
    ID uuid.UUID // *

    readClosed  chan struct{}
    writeClosed chan struct{}

    die     chan struct{}
    dieLock sync.Mutex

    manager *Manager // *

    buf       bytes.Buffer // *
    bufLock   sync.Mutex
    readEvent chan struct{} // size 1

    writeSemaphore     *semaphore.Weighted // wind
    writeCtx           context.Context     // *
    writeCtxCancelFunc context.CancelFunc  // *
}

func newLink(id uuid.UUID, m *Manager) *Link {
    ctx, cancelFunc := context.WithCancel(context.Background())

    return &Link{
        ID: id,

        readClosed:  make(chan struct{}),
        writeClosed: make(chan struct{}),

        die: make(chan struct{}),

        manager: m,

        readEvent: make(chan struct{}, 1),

        writeSemaphore:     semaphore.NewWeighted(maxBufSize),
        writeCtx:           ctx,
        writeCtxCancelFunc: cancelFunc,
    }
}

func (l *Link) pushBytes(p []byte) {
    l.bufLock.Lock()
    l.buf.Write(p)
    l.bufLock.Unlock()

    l.notifyReadEvent()
}

func (l *Link) notifyReadEvent() {
    select {
    case l.readEvent <- struct{}{}:
    default:
    }
}

func (l *Link) Read(p []byte) (n int, err error) {
    if len(p) == 0 || p == nil {
        return 0, nil
    }

    select {
    case <-l.manager.die:
        return 0, fmt.Errorf("read: %s", LowLevelErr)

    case <-l.die:
        return 0, fmt.Errorf("read: %s", RST)

    default:
        select {
        case <-l.readClosed:
            /*if l.buf.Len() != 0 {
                n, _ = l.buf.Read(p)
            }
            select {
            case <-l.writeClosed:
                return n, io.EOF
            default:
                return n, io.EOF
            }*/
            /*if l.buf.Len() == 0 {
                return 0, io.EOF
            }*/

            n, err = l.buf.Read(p)
            return n, fmt.Errorf("read: %s", err)

        case <-l.readEvent:
            l.bufLock.Lock()
            n, err = l.buf.Read(p)
            l.bufLock.Unlock()

            if l.buf.Len() != 0 {
                select {
                case l.readEvent <- struct{}{}:
                default:
                }
            }

            go func() {
                b := make([]byte, 4)
                binary.BigEndian.PutUint32(b, uint32(n))

                if err := l.manager.writePacket(newPacket(l.ID, "ACK", b)); err != nil {
                    l.rst()
                }
            }()
            return
        }
    }
}

func (l *Link) Write(p []byte) (n int, err error) {
    select {
    case <-l.manager.die:
        return 0, fmt.Errorf("write: %s", LowLevelErr)

    case <-l.writeClosed:
        select {
        case <-l.readClosed:
            return 0, CLOSED
        default:
            return 0, FIN_WAIT
        }

    default:
        l.writeSemaphore.Acquire(l.writeCtx, int64(len(p)))

        select {
        case <-l.writeCtx.Done():
            return 0, io.ErrClosedPipe
        default:
            if err := l.manager.writePacket(newPacket(l.ID, "PSH", p)); err != nil {
                return 0, fmt.Errorf("write: %s", err)
            }

            return len(p), nil
        }
    }
}

func (l *Link) ack(size uint32) {
    select {
    case <-l.writeClosed:
    default:
        l.writeSemaphore.Release(int64(size))
    }
}

func (l *Link) rst() {
    l.dieLock.Lock()
    select {
    case <-l.die:
    default:
        close(l.die)
    }
    l.dieLock.Unlock()

    select {
    case <-l.readClosed:
    default:
        close(l.readClosed)
    }

    select {
    case <-l.writeClosed:
    default:
        close(l.writeClosed)
        l.writeCtxCancelFunc()
    }

    l.manager.removeLink(l.ID)
}

func (l *Link) CloseWrite() error {
    l.writeCtxCancelFunc()

    select {
    case <-l.die:
        l.manager.removeLink(l.ID)
        return fmt.Errorf("closeWrite: %s", RST)
    default:
        select {
        case <-l.readClosed:
            l.manager.removeLink(l.ID)
        default:
        }

        select {
        case <-l.writeClosed:
            return nil

        default:
            close(l.writeClosed)
            if err := l.manager.writePacket(newPacket(l.ID, "FIN", nil)); err != nil {
                return fmt.Errorf("closeWrite: %s", err)
            }
            return nil
        }
    }
}

func (l *Link) CloseRead() error {
    select {
    case <-l.die:
        l.manager.removeLink(l.ID)
        return fmt.Errorf("closeRead: %s", RST)
    default:
        select {
        case <-l.writeClosed:
            l.manager.removeLink(l.ID)
        default:
        }

        select {
        case <-l.readClosed:
            return nil

        default:
            close(l.readClosed)
            return nil
        }
    }
}

func (l *Link) RST() {
    l.dieLock.Lock()
    select {
    case <-l.die:
        return
    default:
        close(l.die)
    }
    l.dieLock.Unlock()

    select {
    case <-l.manager.die:
    default:
        if err := l.manager.writePacket(newPacket(l.ID, "RST", nil)); err != nil {
            log.Println(fmt.Errorf("RST: %s", err))
            return
        }
    }

    select {
    case <-l.readClosed:
    default:
        close(l.readClosed)
    }

    select {
    case <-l.writeClosed:
    default:
        close(l.writeClosed)
    }
}
