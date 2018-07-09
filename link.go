package link

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

const writeWind = 768 * 1024

// Link impalement io.ReadWriteCloser.
type Link struct {
	ID uint32

	// use atomic instead of sync.Once to close read, write.
	// if use sync.Once, when closing read and closing write at the same time,
	// and they found write/read should be closed too, they will call once.Do(),
	// it will cause a dead lock because sync.Once use mutex.
	readCtx     chan struct{} // readCtx can recv means link read is closed
	writeCtx    chan struct{} // writeCtx can recv means link write is closed
	readClosed  uint32        // ensure close read once
	writeClosed uint32        // ensure close write once

	manager *Manager

	buf            bytes.Buffer
	bufSize        int32 // add it to improve performance, by using atomic instead of read buf.Len() with bufLock
	bufLock        sync.Mutex
	readEvent      chan struct{} // notify Read link has some data to be read, manager.readLoop and Read will notify it by call readEventNotify
	releaseBufOnce sync.Once

	// when writeWind < 0, Link.Write will be blocked
	writeWind  int32
	writeEvent chan struct{}

	rst int32
}

// newLink will create a Link, but the Link isn't created at other side,
// user should write some data to let other side create the link.
func newLink(id uint32, m *Manager) *Link {
	link := &Link{
		ID: id,

		readCtx:  make(chan struct{}),
		writeCtx: make(chan struct{}),

		manager: m,

		buf: *m.bufferPool.Get().(*bytes.Buffer),

		readEvent: make(chan struct{}, 1),

		writeWind:  writeWind,
		writeEvent: make(chan struct{}, 1),
	}

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

	case FIN:
		l.closeRead()

	case RST:
		l.errorClose()
	}
}

func (l *Link) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		select {
		case <-l.readCtx:
			if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
				return 0, net.OpError{
					Net: "link",
					Op:  "Read",
					Err: errors.New("link reset by peer"),
				}.Err
			} else {
				if l.bufSize > 0 {
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
			case <-l.readCtx:
				// when link read closed or RST, other side doesn't care about the ack
				// because it won't send any packets again
			default:
				go l.sendACK(n)
			}

			return
		}

		select {
		case <-l.readCtx:
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
		case <-l.writeCtx:
			return 0, io.ErrClosedPipe
		default:
			return 0, nil
		}
	}

	if len(p) <= 65535 {
		select {
		case <-l.writeCtx:
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
			case <-l.writeCtx:
				return 0, io.ErrClosedPipe

			case <-l.writeEvent:
				if err := l.manager.writePacket(packet); err != nil {
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

// Close close the link.
// When the link read is closed by manager, Close will send FIN packet and then remove itself from manager.
// When the link read is not closed, Close will send RST packet and then remove itself from manager.
// When thi link is closed, Close do nothing.
func (l *Link) Close() error {
	if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
		return nil
	}

	defer func() {
		l.manager.removeLink(l.ID)

		l.releaseBuf()
	}()

	// dry writeEvent
	select {
	case <-l.writeEvent:
	default:
	}

	/*select {
	case <-l.readCtx:
		select {
		case <-l.writeCtx:
			return nil

		default:
			close(l.writeCtx)
			go l.manager.writePacket(newPacket(l.ID, FIN, nil))
			return nil
		}
	default:
		// ensure when other side is waiting for writing cancel the write
		close(l.readCtx)

		select {
		case <-l.writeCtx:
		default:
			close(l.writeCtx)
		}

		atomic.StoreInt32(&l.rst, 1)

		go l.manager.writePacket(newPacket(l.ID, RST, nil))
		return nil
	}*/
	// if true, means read is not closed, read will be closed
	if atomic.CompareAndSwapUint32(&l.readClosed, 0, 1) {
		close(l.readCtx)

		if atomic.CompareAndSwapUint32(&l.writeClosed, 0, 1) {
			close(l.writeCtx)
		}

		go l.manager.writePacket(newPacket(l.ID, RST, nil))

		return nil
	}

	// read is closed
	// if true, means write is not closed, write will be closed
	if atomic.CompareAndSwapUint32(&l.writeClosed, 0, 1) {
		close(l.writeCtx)
		go l.manager.writePacket(newPacket(l.ID, FIN, nil))
		return nil
	}

	// read write are closed
	return nil
}

// errorClose when manager recv a RST packet about this link,
// call errClose to close this link immediately and remove this link from manager.
func (l *Link) errorClose() {
	atomic.StoreInt32(&l.rst, 1)

	// dry writeEvent
	select {
	case <-l.writeEvent:
	default:
	}

	/*select {
	case <-l.readCtx:
	default:
		close(l.readCtx)
	}*/
	if atomic.CompareAndSwapUint32(&l.readClosed, 0, 1) {
		close(l.readCtx)
	}

	/*select {
	case <-l.writeCtx:
	default:
		close(l.writeCtx)
	}*/
	if atomic.CompareAndSwapUint32(&l.writeClosed, 0, 1) {
		close(l.writeCtx)
	}

	l.manager.removeLink(l.ID)

	l.releaseBuf()
}

// closeRead when recv FIN, link read should be closed.
func (l *Link) closeRead() {
	/*select {
	// link read closed but haven't call closeRead, means Close() or errClose() called.
	case <-l.readCtx:
	default:
		close(l.readCtx)

		select {
		case <-l.writeCtx:
			l.manager.removeLink(l.ID)

			l.releaseBuf()

		default:
		}
	}*/

	// if true, means read is not closed, read will be closed
	// if false, read closed but haven't call closeRead, means Close() or errClose() called
	if atomic.CompareAndSwapUint32(&l.readClosed, 0, 1) {
		close(l.readCtx)

		// if true, means write is closed
		if atomic.CompareAndSwapUint32(&l.writeClosed, 1, 1) {
			l.manager.removeLink(l.ID)

			l.releaseBuf()
		}
	}
}

// CloseWrite will close the link write and send FIN packet,
// if link read is closed, link will be closed.
func (l *Link) CloseWrite() error {
	if atomic.CompareAndSwapInt32(&l.rst, 1, 1) {
		return nil
	}

	// dry writeEvent
	select {
	case <-l.writeEvent:
	default:
	}

	/*select {
	case <-l.writeCtx:
		select {
		case <-l.readCtx:
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
		close(l.writeCtx)

		select {
		case <-l.readCtx:
			l.manager.removeLink(l.ID)

			l.releaseBuf()

		default:
		}

		go l.manager.writePacket(newPacket(l.ID, FIN, nil))
		return nil
	}*/

	// if true, means write is not closed, write will be closed
	if atomic.CompareAndSwapUint32(&l.writeClosed, 0, 1) {
		close(l.writeCtx)

		// if true, means read is closed
		if atomic.CompareAndSwapUint32(&l.readClosed, 1, 1) {
			l.manager.removeLink(l.ID)

			l.releaseBuf()
		}

		go l.manager.writePacket(newPacket(l.ID, FIN, nil))
		return nil
	}

	// write is closed
	// if true, means read is closed
	if atomic.CompareAndSwapUint32(&l.readClosed, 1, 1) {
		return net.OpError{
			Net: "link",
			Op:  "CloseWrite",
			Err: errors.New("close write on a closed link"),
		}.Err
	}

	return net.OpError{
		Net: "link",
		Op:  "CloseWrite",
		Err: errors.New("close write on a write closed link"),
	}.Err
}

// managerClosed when manager is closed, call managerClosed to close link,
// link will like recv a RST packet.
func (l *Link) managerClosed() {
	l.errorClose()
}

// releaseBuf release the link.Buf to the bufferPool.
func (l *Link) releaseBuf() {
	l.releaseBufOnce.Do(func() {
		l.buf.Reset()
		l.manager.bufferPool.Put(&l.buf)
	})
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
