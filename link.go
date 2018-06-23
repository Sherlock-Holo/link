package link

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

const writeWind = 768 * 1024

type Link struct {
	ID uint32

	readCtx           context.Context    // readCtx.Done() can recv means link read is closed
	readCtxCancelFunc context.CancelFunc // close the link read
	readCtxLock       sync.Mutex         // ensure link read close one time

	writeCtx           context.Context    // writeCtx.Done() can recv means link write is closed
	writeCtxCancelFunc context.CancelFunc // close the link write
	writeCtxLock       sync.Mutex         // ensure link write close one time

	manager *Manager

	buf            bytes.Buffer
	bufSize        int32 // add it to improve performance
	bufLock        sync.Mutex
	readEvent      chan struct{} // notify Read link has some data to be read, manager.readLoop and Read will notify it by call readEventNotify
	releaseBufOnce sync.Once

	writeWind  int32
	writeEvent chan struct{}

	closed bool

	rst int32
}

func newLink(id uint32, m *Manager) *Link {
	rctx, rcancelFunc := context.WithCancel(context.Background())
	wctx, wcancelFunc := context.WithCancel(context.Background())

	link := &Link{
		ID: id,

		readCtx:           rctx,
		readCtxCancelFunc: rcancelFunc,

		writeCtx:           wctx,
		writeCtxCancelFunc: wcancelFunc,

		manager: m,

		buf: *bufferPool.Get().(*bytes.Buffer),

		readEvent: make(chan struct{}, 1),

		writeWind:  writeWind,
		writeEvent: make(chan struct{}, 1),
	}

	link.writeEventNotify()

	return link
}

func (l *Link) readEventNotify() {
	select {
	case l.readEvent <- struct{}{}:
	default:
	}
}

func (l *Link) writeEventNotify() {
	select {
	case l.writeEvent <- struct{}{}:
	default:
	}
}

func (l *Link) pushBytes(p []byte) {
	l.bufLock.Lock()
	l.buf.Write(p)
	l.bufLock.Unlock()

	atomic.AddInt32(&l.bufSize, int32(len(p)))
	l.readEventNotify()
}

func (l *Link) pushPacket(p *Packet) {
	switch p.CMD {
	case PSH:
		l.pushBytes(p.Payload)

	case ACK:
		if p.PayloadLength != 2 {
			return
		}

		if atomic.AddInt32(&l.writeWind, int32(binary.BigEndian.Uint16(p.Payload))) > 0 {
			l.writeEventNotify()
		}

	case FIN:
		l.closeRead()

	case RST:
		l.errorClose()
	}
}

func (l *Link) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		select {
		case <-l.readCtx.Done():
			if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
				return 0, net.OpError{
					Net: "link",
					Op:  "Read",
					Err: errors.New("link reset by peer"),
				}.Err
			} else {
				if l.buf.Len() > 0 {
					return 0, nil
				}

				return 0, io.EOF
			}

		default:
			return 0, nil
		}
	}

	for {
		l.bufLock.Lock()
		n, err = l.buf.Read(p)
		l.bufLock.Unlock()

		if n > 0 {
			atomic.AddInt32(&l.bufSize, -int32(n))

			select {
			case <-l.readCtx.Done():
				// when link read closed or RST, other side doesn't care about the ack
				// because it won't send any packets again
			default:
				go func() {
					ack := make([]byte, 2)
					binary.BigEndian.PutUint16(ack, uint16(n))
					l.manager.writePacket(newPacket(l.ID, ACK, ack))
				}()
			}

			return
		}

		select {
		case <-l.readCtx.Done():
			if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
				return 0, net.OpError{
					Net: "link",
					Op:  "Read",
					Err: errors.New("link reset by peer"),
				}.Err
			} else {
				return 0, io.EOF
			}

		case <-l.readEvent:
			if atomic.LoadInt32(&l.bufSize) > int32(len(p)) {
				l.readEventNotify()
			}
		}
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

	if len(p) <= 65535 {
		select {
		case <-l.writeCtx.Done():
			return 0, io.ErrClosedPipe

		case <-l.writeEvent:
			if err := l.manager.writePacket(newPacket(l.ID, PSH, p)); err != nil {
				l.writeEventNotify()
				return 0, err
			}

			if atomic.AddInt32(&l.writeWind, -int32(len(p))) > 0 {
				l.writeEventNotify()
			}

			return len(p), nil
		}

	} else {
		packets := split(l.ID, p)
		for _, packet := range packets {
			select {
			case <-l.writeCtx.Done():
				return 0, io.ErrClosedPipe

			case <-l.writeEvent:
				if err := l.manager.writePacket(packet); err != nil {
					l.writeEventNotify()
					return 0, err
				}

				if atomic.AddInt32(&l.writeWind, -int32(packet.PayloadLength)) > 0 {
					l.writeEventNotify()
				}
			}
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

	if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
		return nil
	}

	defer func() {
		l.manager.linksLock.Lock()
		l.manager.removeLink(l.ID)
		l.manager.linksLock.Unlock()

		l.releaseBuf()
	}()

	// dry writeEvent
	select {
	case <-l.writeEvent:
	default:
	}

	l.readCtxLock.Lock()
	l.writeCtxLock.Lock()

	select {
	case <-l.readCtx.Done():
		l.readCtxLock.Unlock()

		select {
		case <-l.writeCtx.Done():

			l.writeCtxLock.Unlock()

			return nil
		default:
			l.writeCtxCancelFunc()

			l.writeCtxLock.Unlock()

			return l.manager.writePacket(newPacket(l.ID, FIN, nil))
		}
	default:
		l.readCtxCancelFunc()
		l.readCtxLock.Unlock()

		select {
		case <-l.writeCtx.Done():

			l.writeCtxLock.Unlock()

			return nil

		default:
			l.writeCtxCancelFunc()

			l.writeCtxLock.Unlock()

			atomic.StoreInt32(&l.rst, 1)

			return l.manager.writePacket(newPacket(l.ID, RST, nil))
		}
	}
}

func (l *Link) errorClose() {
	l.writeCtxLock.Lock()
	l.readCtxLock.Lock()
	defer l.writeCtxLock.Unlock()
	defer l.readCtxLock.Unlock()

	atomic.StoreInt32(&l.rst, 1)

	// dry writeEvent
	select {
	case <-l.writeEvent:
	default:
	}

	l.readCtxCancelFunc()
	l.writeCtxCancelFunc()

	l.manager.linksLock.Lock()
	l.manager.removeLink(l.ID)
	l.manager.linksLock.Unlock()

	l.releaseBuf()
}

// when recv FIN, link read should be closed
func (l *Link) closeRead() {
	l.readCtxLock.Lock()
	l.writeCtxLock.Lock()
	defer l.readCtxLock.Unlock()
	defer l.writeCtxLock.Unlock()

	select {

	// link read closed but haven't call closeRead, means Close or errClose called.
	case <-l.readCtx.Done():

	default:
		l.readCtxCancelFunc()

		select {
		case <-l.writeCtx.Done():
			l.manager.linksLock.Lock()
			l.manager.removeLink(l.ID)
			l.manager.linksLock.Unlock()

			l.releaseBuf()

		default:
		}
	}
}

func (l *Link) CloseWrite() error {
	l.writeCtxLock.Lock()
	l.readCtxLock.Lock()
	defer l.writeCtxLock.Unlock()
	defer l.readCtxLock.Unlock()

	if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
		return nil
	}

	// dry writeEvent
	select {
	case <-l.writeEvent:
	default:
	}

	select {
	case <-l.writeCtx.Done():
		select {
		case <-l.readCtx.Done():
			return net.OpError{
				Net: "link",
				Op:  "CloseWrite",
				Err: errors.New("close write on a closed link"),
			}.Err

		default:
			return net.OpError{
				Net: "link",
				Op:  "CloseWrite",
				Err: errors.New("close write on a write closed link"),
			}.Err
		}

	default:
		l.writeCtxCancelFunc()

		select {
		case <-l.readCtx.Done():
			l.manager.linksLock.Lock()
			l.manager.removeLink(l.ID)
			l.manager.linksLock.Unlock()

			l.releaseBuf()

		default:
		}

		return l.manager.writePacket(newPacket(l.ID, FIN, nil))
	}
}

func (l *Link) managerClosed() {
	l.errorClose()
}

func (l *Link) releaseBuf() {
	l.releaseBufOnce.Do(func() {
		l.buf.Reset()
		bufferPool.Put(&l.buf)
	})
}

func (l *Link) IsClosed() bool {
	return l.closed
}
