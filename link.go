package link

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/xerrors"
)

const (
	ackPayloadLength = 4
)

// Link impalement io.ReadWriteCloser.
type link struct {
	ID uint32

	ctx          context.Context
	ctxCloseFunc context.CancelFunc

	manager *manager

	buf             *bytes.Buffer
	readableBufSize int32 // add it to improve performance, by using atomic instead of read buf.Len() with bufLock
	bufLock         sync.Mutex
	readEvent       chan struct{} // notify Read link has some data to be read, manager.readLoop and Read may notify it by call readAvailable

	// when writeWind < 0, Link.Write will be blocked
	writeWind  int32
	writeEvent chan struct{}

	// rst int32
	eof sync.Once

	readDeadline  atomic.Value // time.Time
	writeDeadline atomic.Value // time.Time

	dialCtx     context.Context
	dialCtxFunc context.CancelFunc

	closeOnce sync.Once
}

// newLink will create a Link, but the Link isn't created at other side,
// user should write some data to let other side create the link.
func newLink(id uint32, m *manager, mode mode) *link {
	link := &link{
		ID: id,

		manager: m,

		buf:             bytes.NewBuffer(make([]byte, 0, m.cfg.ReadBufSize)),
		readableBufSize: m.cfg.ReadBufSize,

		readEvent:  make(chan struct{}, 1),
		writeEvent: make(chan struct{}, 1),
	}

	link.ctx, link.ctxCloseFunc = context.WithCancel(m.ctx)

	link.readDeadline.Store(time.Time{})
	link.writeDeadline.Store(time.Time{})

	if mode == ClientMode {
		link.dialCtx, link.dialCtxFunc = context.WithCancel(context.Background())
	}

	link.writeAvailable()

	return link
}

// readAvailable notify link is readable
func (l *link) readAvailable() {
	select {
	case l.readEvent <- struct{}{}:
	default:
	}
}

// writeAvailable notify link is writable
func (l *link) writeAvailable() {
	select {
	case l.writeEvent <- struct{}{}:
	default:
	}
}

// pushBytes push some data to link.buf.
func (l *link) pushBytes(p []byte) {
	if len(p) == 0 {
		return
	}

	l.bufLock.Lock()
	l.buf.Write(p)
	l.bufLock.Unlock()

	atomic.AddInt32(&l.readableBufSize, -int32(len(p)))
	l.readAvailable()
}

// pushPacket when manager recv a packet about this link,
// manager calls pushPacket to let link handles that packet.
func (l *link) pushPacket(p *Packet) {
	switch p.CMD {
	case PSH:
		l.pushBytes(p.Payload)

	case NEW:
		atomic.StoreInt32(&l.writeWind, int32(binary.BigEndian.Uint32(p.Payload[:4])))
		l.pushBytes(p.Payload[4:])

	case ACK:
		atomic.StoreInt32(&l.writeWind, int32(binary.BigEndian.Uint32(p.Payload)))
		if atomic.LoadInt32(&l.writeWind) > 0 {
			l.writeAvailable()
		}

	case CLOSE:
		l.closeByPeer()

	case ACPT:
		l.dialCtxFunc()
		atomic.StoreInt32(&l.writeWind, int32(binary.BigEndian.Uint32(p.Payload)))
	}
}

func (l *link) Read(p []byte) (n int, err error) {
	readDeadline := l.readDeadline.Load().(time.Time)
	if !readDeadline.IsZero() && time.Now().After(readDeadline) {
		return 0, xerrors.Errorf("link read failed: %w", ErrTimeout)
	}

	// we should not pass a 0 length buffer into Read(p []byte), if so will always return (0, nil)
	if len(p) == 0 {
		return 0, nil
	}

	for {
		l.bufLock.Lock()
		n, _ = l.buf.Read(p)
		l.bufLock.Unlock()

		if n > 0 {
			atomic.AddInt32(&l.readableBufSize, int32(n))

			select {
			case <-l.ctx.Done():
				// when link is closed, peer doesn't care about the ack because it won't send any packets again
			default:
				go l.sendACK()
			}

			return
		}

		timeoutCtx := context.Background()
		readDeadline = l.readDeadline.Load().(time.Time)
		if !readDeadline.IsZero() {
			timeoutCtx, _ = context.WithTimeout(context.Background(), readDeadline.Sub(time.Now()))
		}

		select {
		case <-l.ctx.Done():
			err = xerrors.Errorf("link read failed: %w", io.ErrClosedPipe)

			l.eof.Do(func() {
				err = io.EOF
			})

			if err == io.EOF {
				return
			}

			select {
			case <-l.manager.ctx.Done():
				return 0, xerrors.Errorf("link read failed: %w", ErrManagerClosed)
			default:
			}

			return

		case <-l.readEvent:
		// wait for peer writing data

		case <-timeoutCtx.Done():
			return 0, xerrors.Errorf("link read failed: %w", ErrTimeout)
		}
	}
}

func (l *link) Write(p []byte) (int, error) {
	writeDeadline := l.writeDeadline.Load().(time.Time)
	if !writeDeadline.IsZero() && time.Now().After(writeDeadline) {
		return 0, xerrors.Errorf("link write failed: %w", ErrTimeout)
	}

	// we should not pass a 0 length buffer into Write(p []byte), if so will always return (0, nil)
	if len(p) == 0 {
		return 0, nil
	}

	select {
	case <-l.ctx.Done():
		select {
		case <-l.manager.ctx.Done():
			return 0, xerrors.Errorf("link write failed: %w", ErrManagerClosed)
		default:
		}

		return 0, xerrors.Errorf("link write failed: %w", io.ErrClosedPipe)

	case <-l.writeEvent:
		if err := l.manager.writePacket(newPacket(l.ID, PSH, p)); err != nil {
			return 0, xerrors.Errorf("link write failed: %w", err)
		}

		if atomic.AddInt32(&l.writeWind, -int32(len(p))) > 0 {
			l.writeAvailable()
		}

		return len(p), nil
	}
}

// Close close the link.
func (l *link) Close() (err error) {
	select {
	case <-l.ctx.Done():
		// fast path
		return xerrors.Errorf("link closed failed: %w", ErrLinkClosed)

	default:
	}

	err = ErrLinkClosed

	l.closeOnce.Do(func() {
		l.ctxCloseFunc()

		l.manager.removeLink(l.ID)

		err = l.manager.writePacket(newPacket(l.ID, CLOSE, nil))
	})

	if err != nil {
		return xerrors.Errorf("link closed failed: %w", err)
	}
	return nil
}

// closeByPeer when link is closed by peer, closeByPeer will be called.
func (l *link) closeByPeer() {
	l.ctxCloseFunc()
	l.manager.removeLink(l.ID)
}

// sendACK check if n > 65535, if n > 65535 will send more then 1 ACK packet.
func (l *link) sendACK() {
	buf := make([]byte, ackPayloadLength)
	size := atomic.LoadInt32(&l.readableBufSize)
	if size < 0 {
		size = 0
	}
	binary.BigEndian.PutUint32(buf, uint32(size))
	l.manager.writePacket(newPacket(l.ID, ACK, buf))
}

func (l *link) LocalAddr() net.Addr {
	return Addr{
		ID: strconv.Itoa(int(l.ID)),
	}
}

func (l *link) RemoteAddr() net.Addr {
	return Addr{
		ID: strconv.Itoa(int(l.ID)),
	}
}

func (l *link) SetDeadline(t time.Time) error {
	select {
	case <-l.ctx.Done():
		return xerrors.Errorf("link set deadline failed: %w", ErrLinkClosed)
	default:
	}

	l.readDeadline.Store(t)
	l.writeDeadline.Store(t)
	return nil
}

func (l *link) SetReadDeadline(t time.Time) error {
	select {
	case <-l.ctx.Done():
		return xerrors.Errorf("link set read deadline failed: %w", ErrLinkClosed)
	default:
	}

	l.readDeadline.Store(t)
	return nil
}

func (l *link) SetWriteDeadline(t time.Time) error {
	select {
	case <-l.ctx.Done():
		return xerrors.Errorf("link set write deadline failed: %w", ErrLinkClosed)
	default:
	}

	l.writeDeadline.Store(t)
	return nil
}
