package internal

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing/iotest"
	"time"

	"github.com/Sherlock-Holo/link"
)

const writeWind = 768 * 1024

// Link impalement io.ReadWriteCloser.
type Link struct {
	ID uint32

	ctx          context.Context
	ctxCloseFunc context.CancelFunc

	manager *Manager

	buf       *bytes.Buffer
	bufSize   int32 // add it to improve performance, by using atomic instead of read buf.Len() with bufLock
	bufLock   sync.Mutex
	readEvent chan struct{} // notify Read link has some data to be read, manager.readLoop and Read will notify it by call readEventNotify

	// when writeWind < 0, Link.Write will be blocked
	writeWind  int32
	writeEvent chan struct{}

	// rst int32
	eof sync.Once

	readDLLock    sync.RWMutex
	writeDLLock   sync.RWMutex
	readDeadline  time.Time
	writeDeadline time.Time
}

// dial will create a Link, but the Link isn't created at other side,
// user should write some data to let other side create the link.
func dial(id uint32, m *Manager) *Link {
	link := &Link{
		ID: id,

		manager: m,

		buf: bytes.NewBuffer(make([]byte, 0, 1024*16)),

		readEvent: make(chan struct{}, 1),

		writeWind:  writeWind,
		writeEvent: make(chan struct{}, 1),
	}

	link.ctx, link.ctxCloseFunc = context.WithCancel(m.ctx)

	link.writeEventNotify()

	return link
}

// readEventNotify notify link is readable
func (l *Link) readEventNotify() {
	select {
	case l.readEvent <- struct{}{}:
	default:
	}
}

// writeEventNotify notify link is writable
func (l *Link) writeEventNotify() {
	select {
	case l.writeEvent <- struct{}{}:
	default:
	}
}

// pushBytes push some data to link.buf.
func (l *Link) pushBytes(p []byte) {
	l.bufLock.Lock()
	l.buf.Write(p)
	l.bufLock.Unlock()

	atomic.AddInt32(&l.bufSize, int32(len(p)))
	l.readEventNotify()
}

// pushPacket when manager recv a packet about this link,
// manager calls pushPacket to let link handles that packet.
func (l *Link) pushPacket(p *Packet) {
	switch p.CMD {
	case PSH:
		l.pushBytes(p.Payload)

	case ACK:
		// if ACK packet has error payload, it will be ignored.
		if p.PayloadLength != 2 {
			return
		}

		if atomic.AddInt32(&l.writeWind, int32(binary.BigEndian.Uint16(p.Payload))) > 0 {
			l.writeEventNotify()
		}

	case CLOSE:
		l.closeByPeer()
	}
}

func (l *Link) Read(p []byte) (n int, err error) {
	l.readDLLock.RLock()
	if time.Now().After(l.readDeadline) {
		l.readDLLock.RUnlock()
		return 0, iotest.ErrTimeout
	} else {
		l.readDLLock.RUnlock()
	}

	if len(p) == 0 {
		select {
		case <-l.ctx.Done():
			if atomic.LoadInt32(&l.bufSize) > 0 {
				return 0, nil
			}
			err = io.ErrClosedPipe

			l.eof.Do(func() {
				err = io.EOF
			})

			return

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
			case <-l.ctx.Done():
				// when link is closed, peer doesn't care about the ack because it won't send any packets again
			default:
				go l.sendACK(n)
			}

			return
		}

		select {
		case <-l.ctx.Done():
			err = io.ErrClosedPipe

			l.eof.Do(func() {
				err = io.EOF
			})

			return

		case <-l.readEvent:
		}
	}
}

func (l *Link) Write(p []byte) (int, error) {
	l.writeDLLock.RLock()
	if time.Now().After(l.writeDeadline) {
		l.writeDLLock.RUnlock()
		return 0, iotest.ErrTimeout
	} else {
		l.writeDLLock.RUnlock()
	}

	if len(p) == 0 {
		select {
		case <-l.ctx.Done():
			return 0, io.ErrClosedPipe
		default:
			return 0, nil
		}
	}

	if len(p) <= 65535 {
		select {
		case <-l.ctx.Done():
			return 0, io.ErrClosedPipe

		case <-l.writeEvent:
			if err := l.manager.writePacket(newPacket(l.ID, PSH, p)); err != nil {
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
			case <-l.ctx.Done():
				return 0, io.ErrClosedPipe

			case <-l.writeEvent:
				if err := l.manager.writePacket(packet); err != nil {
					return 0, link.ErrManagerClosed
				}

				if atomic.AddInt32(&l.writeWind, -int32(packet.PayloadLength)) > 0 {
					l.writeEventNotify()
				}
			}
		}
		return len(p), nil
	}
}

// Close close the link.
func (l *Link) Close() error {
	select {
	case <-l.ctx.Done():
		return nil
	default:
		l.ctxCloseFunc()
	}

	l.manager.removeLink(l.ID)

	if err := l.manager.writePacket(newPacket(l.ID, CLOSE, nil)); err != nil {
		return link.ErrManagerClosed
	}

	return nil
}

// closeByPeer when link is closed by peer, closeByPeer will be called.
func (l *Link) closeByPeer() {
	select {
	case <-l.ctx.Done():
		return
	default:
		l.ctxCloseFunc()
	}

	l.manager.removeLink(l.ID)
}

// sendACK check if n > 65535, if n > 65535 will send more then 1 ACK packet.
func (l *Link) sendACK(n int) {
	if n <= 65535 {
		ack := make([]byte, 2)
		binary.BigEndian.PutUint16(ack, uint16(n))
		l.manager.writePacket(newPacket(l.ID, ACK, ack))

	} else {
		var acks [][]byte
		for {
			ack := make([]byte, 2)
			binary.BigEndian.PutUint16(ack, uint16(65535))
			acks = append(acks, ack)

			n -= 65535
			if n <= 65535 {
				lastAck := make([]byte, 2)
				binary.BigEndian.PutUint16(ack, uint16(n))
				acks = append(acks, lastAck)
				break
			}
		}

		for _, ack := range acks {
			if err := l.manager.writePacket(newPacket(l.ID, ACK, ack)); err != nil {
				return
			}
		}
	}
}

func (l *Link) LocalAddr() net.Addr {
	return Addr{
		ID: strconv.Itoa(int(l.ID)),
	}
}

func (l *Link) RemoteAddr() net.Addr {
	return Addr{
		ID: strconv.Itoa(int(l.ID)),
	}
}

func (l *Link) SetDeadline(t time.Time) error {
	select {
	case <-l.ctx.Done():
		return link.ErrLinkClosed
	default:
	}

	l.readDLLock.Lock()
	l.writeDLLock.Lock()
	defer func() {
		l.readDLLock.Unlock()
		l.writeDLLock.Unlock()
	}()

	l.readDeadline = t
	l.writeDeadline = t
	return nil
}

func (l *Link) SetReadDeadline(t time.Time) error {
	select {
	case <-l.ctx.Done():
		return link.ErrLinkClosed
	default:
	}

	l.readDLLock.Lock()
	defer l.readDLLock.Unlock()

	l.readDeadline = t
	return nil
}

func (l *Link) SetWriteDeadline(t time.Time) error {
	select {
	case <-l.ctx.Done():
		return link.ErrLinkClosed
	default:
	}

	l.writeDLLock.Lock()
	defer l.writeDLLock.Unlock()

	l.writeDeadline = t
	return nil
}
