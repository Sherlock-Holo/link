package link

import (
    "bytes"
    "context"
    "sync"
    "io"
)

const (
    maxBufSize = 1 << 18
)

type Link struct {
    ID uint32 // *

    ctx           context.Context    // ctx.Done() can recv means link is closed
    ctxCancelFunc context.CancelFunc // close the link
    ctxLock       sync.Mutex         // ensure link close one time

    manager *Manager // *

    buf       bytes.Buffer // *
    bufLock   sync.Mutex
    readEvent chan struct{} // notify Read link has some data to be read, manager.readLoop and Read will notify it by call bufNotify
}

func newLink(id uint32, m *Manager) *Link {
    ctx, cancelFunc := context.WithCancel(context.Background())

    return &Link{
        ID: id,

        ctx:           ctx,
        ctxCancelFunc: cancelFunc,

        manager: m,

        readEvent: make(chan struct{}, 1),
    }
}

func (l *Link) bufNotify() {
    select {
    case l.readEvent <- struct{}{}:
    default:
    }
}

func (l *Link) pushBytes(p []byte) {
    l.bufLock.Lock()
    l.buf.Write(p)
    l.bufNotify()
    l.bufLock.Unlock()
}

func (l *Link) Read(p []byte) (n int, err error) {
    if len(p) == 0 {
        select {
        case <-l.ctx.Done():
            return 0, io.EOF
        default:
            return 0, nil
        }
    }

    select {
    case <-l.ctx.Done():
        return l.buf.Read(p)

    case <-l.readEvent:
        l.bufLock.Lock()
        n, err = l.buf.Read(p)
        if l.buf.Len() != 0 {
            l.bufNotify()
        }
        l.bufLock.Unlock()

        l.manager.returnToken(n)

        return
    }
}

func (l *Link) Write(p []byte) (int, error) {
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
        return 0, io.ErrClosedPipe
    default:
        if err := l.manager.writePacket(newPacket(l.ID, "PSH", p)); err != nil {
            return 0, err
        }
        return len(p), nil
    }
}

func (l *Link) Close() error {
    l.ctxLock.Lock()
    defer l.ctxLock.Unlock()

    l.manager.returnToken(l.buf.Len())

    select {
    case <-l.ctx.Done():
        return nil
    default:
        l.ctxCancelFunc()
        l.manager.linksLock.Lock()
        l.manager.removeLink(l.ID)
        l.manager.linksLock.Unlock()
        return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
    }
}

// when recv FIN, link should be closed and should not send FIN too
func (l *Link) close() {
    l.ctxLock.Lock()
    l.ctxCancelFunc()
    l.ctxLock.Unlock()
    l.manager.returnToken(l.buf.Len())
}
