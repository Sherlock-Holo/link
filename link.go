package link

import (
    "bytes"
    "context"
    "sync"
    "io"
    "errors"
)

const (
    maxBufSize = 1 << 18
)

type Link struct {
    ID uint32 // *

    readCtx           context.Context    // readCtx.Done() can recv means link read is closed
    readCtxCancelFunc context.CancelFunc // close the link read
    readCtxLock       sync.Mutex         // ensure link read close one time

    writeCtx           context.Context    // writeCtx.Done() can recv means link write is closed
    writeCtxCancelFunc context.CancelFunc // close the link write
    writeCtxLock       sync.Mutex         // ensure link write close one time

    manager *Manager // *

    buf       bytes.Buffer // *
    bufLock   sync.Mutex
    readEvent chan struct{} // notify Read link has some data to be read, manager.readLoop and Read will notify it by call bufNotify

    closed bool
}

func newLink(id uint32, m *Manager) *Link {
    rctx, rcancelFunc := context.WithCancel(context.Background())
    wctx, wcancelFunc := context.WithCancel(context.Background())

    return &Link{
        ID: id,

        readCtx:           rctx,
        readCtxCancelFunc: rcancelFunc,

        writeCtx:           wctx,
        writeCtxCancelFunc: wcancelFunc,

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
        case <-l.readCtx.Done():
            return 0, io.EOF
        default:
            return 0, nil
        }
    }

    select {
    case <-l.readCtx.Done():
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
        case <-l.writeCtx.Done():
            return 0, io.ErrClosedPipe
        default:
            return 0, nil
        }
    }

    select {
    case <-l.writeCtx.Done():
        return 0, io.ErrClosedPipe
    default:
        if err := l.manager.writePacket(newPacket(l.ID, "PSH", p)); err != nil {
            return 0, err
        }
        return len(p), nil
    }
}

func (l *Link) Close() error {
    if l.closed {
        return errors.New("close again")
    } else {
        l.closed = true
    }

    l.readCtxLock.Lock()
    l.writeCtxLock.Lock()
    defer l.readCtxLock.Unlock()
    defer l.writeCtxLock.Unlock()

    select {
    case <-l.readCtx.Done():
        select {
        case <-l.writeCtx.Done():
            return nil
        default:
            l.writeCtxCancelFunc()
            l.manager.linksLock.Lock()
            l.manager.removeLink(l.ID)
            l.manager.linksLock.Unlock()
            return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
        }
    default:
        l.readCtxCancelFunc()
        select {
        case <-l.writeCtx.Done():
            return nil
        default:
            l.writeCtxCancelFunc()
            return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
        }
    }
}

// when recv FIN, link should be closed and should not send FIN too
func (l *Link) close() {
    l.manager.returnToken(l.buf.Len())

    // l.writeCtxLock.Lock()
    l.readCtxLock.Lock()
    // defer l.writeCtxLock.Unlock()
    defer l.readCtxLock.Unlock()

    select {
    case <-l.readCtx.Done():
        // l.manager.linksLock.Lock()
        l.manager.removeLink(l.ID)
        // l.manager.linksLock.Unlock()
    default:
        l.readCtxCancelFunc()
        select {
        case <-l.writeCtx.Done():
            // l.manager.linksLock.Lock()
            l.manager.removeLink(l.ID)
            // l.manager.linksLock.Unlock()
        default:
        }
    }
}

func (l *Link) CloseWrite() error {
    l.writeCtxLock.Lock()
    defer l.writeCtxLock.Unlock()

    select {
    case <-l.readCtx.Done():
        select {
        case <-l.writeCtx.Done():
            return errors.New("close write on a closed link")
        default:
            l.writeCtxCancelFunc()
            l.manager.linksLock.Lock()
            l.manager.removeLink(l.ID)
            l.manager.linksLock.Unlock()
            return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
        }
    default:
        select {
        case <-l.writeCtx.Done():
            return errors.New("close write again")
        default:
            l.writeCtxCancelFunc()
            return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
        }
    }
}
