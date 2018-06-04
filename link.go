package link

import (
    "bytes"
    "context"
    "sync"

    "golang.org/x/sync/semaphore"
    "io"
    "encoding/binary"
)

const (
    maxBufSize = 1 << 18
)

type Link struct {
    ID uint32 // *

    ctx           context.Context    // *
    ctxCancelFunc context.CancelFunc // *

    manager *Manager // *

    buf       bytes.Buffer // *
    bufLock   sync.Mutex
    readEvent chan struct{} // size 1

    writeSemaphore *semaphore.Weighted // wind
}

func newLink(id uint32, m *Manager) *Link {
    ctx, cancelFunc := context.WithCancel(context.Background())

    return &Link{
        ID: id,

        ctx:           ctx,
        ctxCancelFunc: cancelFunc,

        manager: m,

        readEvent: make(chan struct{}, 1),

        writeSemaphore: semaphore.NewWeighted(maxBufSize),
    }
}

func (l *Link) pushBytes(p []byte) {
    l.bufLock.Lock()
    l.buf.Write(p)
    l.notifyReadEvent()
    l.bufLock.Unlock()
}

func (l *Link) notifyReadEvent() {
    select {
    case l.readEvent <- struct{}{}:
    default:
    }
}

func (l *Link) ack(n uint32) {
    select {
    case <-l.ctx.Done():
    default:
        l.writeSemaphore.Release(int64(n))
    }
}

func (l *Link) Read(p []byte) (n int, err error) {
    if len(p) == 0 {
        select {
        case <-l.ctx.Done():
            return 0, io.ErrClosedPipe
        default:
            return 0, nil
        }
    }

    select {
    case <-l.ctx.Done():
        n, err = l.buf.Read(p)
        return

    case <-l.readEvent:
        l.bufLock.Lock()
        n, err = l.buf.Read(p)
        if l.buf.Len() != 0 {
            l.notifyReadEvent()
        }
        l.bufLock.Unlock()

        select {
        case <-l.ctx.Done():
            return n, io.ErrUnexpectedEOF
        default:
            ack := make([]byte, 4)
            binary.BigEndian.PutUint32(ack, uint32(n))
            l.manager.writePacket(newPacket(l.ID, "ACK", ack))
        }
        return
    }
}

func (l *Link) Write(p []byte) (n int, err error) {
    if len(p) == 0 {
        select {
        case <-l.ctx.Done():
            return 0, io.ErrClosedPipe
        case <-l.manager.ctx.Done():
            return 0, io.ErrClosedPipe
        default:
            return
        }
    }

    select {
    case <-l.ctx.Done():
        return 0, io.ErrClosedPipe

    case <-l.manager.ctx.Done():
        return 0, io.ErrClosedPipe

    default:
        l.writeSemaphore.Acquire(l.ctx, int64(len(p)))
        select {
        case <-l.ctx.Done():
            return 0, io.ErrClosedPipe

        case <-l.manager.ctx.Done():
            return 0, io.ErrClosedPipe

        default:
            l.manager.writePacket(newPacket(l.ID, "PSH", p))
            return len(p), nil
        }
    }
}

func (l *Link) Close() error {
    l.manager.removeLink(l.ID)

    select {
    case <-l.ctx.Done():
        return nil
    default:
        l.ctxCancelFunc()
        l.manager.writePacket(newPacket(l.ID, "FIN", nil))
        return nil
    }
}

func (l *Link) close() {
    l.ctxCancelFunc()
}
