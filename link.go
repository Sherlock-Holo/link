package link

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"sync"
	"sync/atomic"
)

type Link struct {
	ID uint32

	readCtx           context.Context    // readCtx.Done() can recv means link read is closed
	readCtxCancelFunc context.CancelFunc // close the link read
	readCtxLock       sync.Mutex         // ensure link read close one time

	writeCtx           context.Context    // writeCtx.Done() can recv means link write is closed
	writeCtxCancelFunc context.CancelFunc // close the link write
	writeCtxLock       sync.Mutex         // ensure link write close one time

	manager *Manager

	buf       bytes.Buffer
	bufLock   sync.Mutex
	readEvent chan struct{} // notify Read link has some data to be read, manager.readLoop and Read will notify it by call readEventNotify

	writeWind  int32
	writeEvent chan struct{}

	closed bool
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

		readEvent: make(chan struct{}, 1),

		writeWind:  512 * 1024,
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
	l.readEventNotify()
	l.bufLock.Unlock()
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
		log.Println("recv FIN")
		l.close()
	}
}

func (l *Link) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		select {
		case <-l.readCtx.Done():
			if l.buf.Len() > 0 {
				return 0, nil
			}
			return 0, io.EOF
		default:
			return 0, nil
		}
	}

	for {
		l.bufLock.Lock()
		n, err = l.buf.Read(p)
		/*if l.buf.Len() > 0 {
			l.readEventNotify()
		}*/
		l.bufLock.Unlock()

		if n > 0 {
			select {
			case <-l.readCtx.Done():
				// when recv FIN, other size doesn't care about the ack because it won't send any packets again
			default:
				go func() {
					ack := make([]byte, 2)
					binary.BigEndian.PutUint16(ack, uint16(n))
					l.manager.writePacket(newPacket(l.ID, "ACK", ack))
				}()
			}

			return
		}

		select {
		case <-l.readCtx.Done():
			return 0, io.EOF
		case <-l.readEvent:
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
			l.writeEventNotify()

			return 0, io.ErrClosedPipe

		case <-l.writeEvent:
			if err := l.manager.writePacket(newPacket(l.ID, "PSH", p)); err != nil {
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
				l.writeEventNotify()

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
	l.readCtxLock.Lock()
	l.writeCtxLock.Lock()
	defer l.readCtxLock.Unlock()
	defer l.writeCtxLock.Unlock()

	if l.closed {
		return errors.New("close again")
	} else {
		l.closed = true
	}

	select {
	case <-l.readCtx.Done():
		select {
		case <-l.writeCtx.Done():
			return nil
		default:
			l.writeCtxCancelFunc()

			l.writeEventNotify()

			l.manager.removeLink(l.ID)

			log.Println("send FIN")
			return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
		}
	default:
		l.readCtxCancelFunc()

		select {
		case <-l.writeCtx.Done():
			return nil
		default:
			l.writeCtxCancelFunc()

			l.writeEventNotify()

			log.Println("send FIN")
			return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
		}
	}
}

// when recv FIN, link read should be closed
func (l *Link) close() {
	l.readCtxLock.Lock()
	defer l.readCtxLock.Unlock()

	select {
	case <-l.readCtx.Done(): // manager error or closed, called managerClosed()
		l.manager.removeLink(l.ID)

	default:
		l.readCtxCancelFunc()

		l.writeCtxLock.Lock()
		defer l.writeCtxLock.Unlock()

		select {
		case <-l.writeCtx.Done():
			l.manager.removeLink(l.ID)
		default:
		}
	}
}

func (l *Link) CloseWrite() error {
	log.Println("close write")
	l.writeCtxLock.Lock()
	l.readCtxLock.Lock()
	defer l.writeCtxLock.Unlock()
	defer l.readCtxLock.Unlock()

	select {
	case <-l.readCtx.Done():
		select {
		case <-l.writeCtx.Done():
			return errors.New("close write on a closed link")
		default:
			l.writeCtxCancelFunc()

			l.writeEventNotify()

			l.manager.removeLink(l.ID)

			log.Println("send FIN")
			return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
		}
	default:
		select {
		case <-l.writeCtx.Done():
			return errors.New("close write again")
		default:
			l.writeCtxCancelFunc()

			l.writeEventNotify()

			log.Println("send FIN")
			return l.manager.writePacket(newPacket(l.ID, "FIN", nil))
		}
	}
}

func (l *Link) managerClosed() {
	l.readCtxLock.Lock()
	l.writeCtxLock.Lock()
	defer l.readCtxLock.Unlock()
	defer l.writeCtxLock.Unlock()

	select {
	case <-l.readCtx.Done():
	default:
		l.readCtxCancelFunc()
	}

	select {
	case <-l.writeCtx.Done():
	default:
		l.writeCtxCancelFunc()

		l.writeEventNotify()
	}
}
