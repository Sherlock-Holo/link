package link

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

type writeRequest struct {
	packet  *Packet
	written chan struct{} // if written, close this chan
}

// Manager manager will manage some links.
type manager struct {
	conn net.Conn

	links sync.Map

	maxID   int32
	usedIDs map[uint32]bool

	ctx          context.Context // when ctx is closed, manager is closed
	ctxCloseFunc context.CancelFunc

	writes chan writeRequest // write chan limit link don't write too quickly

	acceptQueue chan *link // accept queue, call Accept() will get a waiting link

	timeoutTimer  *time.Timer // timer check if the manager is timeout
	timeout       time.Duration
	initTimerOnce sync.Once // make sure Timer init once

	keepaliveTicker *time.Ticker // ticker will send ping regularly

	cfg Config
}

// NewManager create a manager based on conn, if config is nil, will use DefaultConfig.
func NewManager(conn net.Conn, config Config) Manager {
	manager := &manager{
		conn: conn,

		maxID:   -1,
		usedIDs: make(map[uint32]bool),

		writes: make(chan writeRequest, 1),

		cfg: config,
	}

	manager.ctx, manager.ctxCloseFunc = context.WithCancel(context.Background())

	manager.acceptQueue = make(chan *link, manager.cfg.AcceptQueueSize)

	if manager.cfg.Mode == ClientMode && manager.cfg.KeepaliveInterval > 0 {
		manager.initTimerOnce.Do(func() {
			if config.KeepaliveInterval > 0 {
				manager.timeout = 2 * manager.cfg.KeepaliveInterval
				manager.keepaliveTicker = time.NewTicker(config.KeepaliveInterval)

				go manager.keepAlive()
			}
		})
	}

	go manager.readLoop()
	go manager.writeLoop()

	// debug
	/*go func() {
		for {
			time.Sleep(2 * time.Second)
			var count int
			manager.links.Range(func(_, _ interface{}) bool {
				count++
				return true
			})

			fmt.Println("links size", count)
		}
	}()*/

	return manager
}

// keepAlive send PING packet to other side to keepalive.
func (m *manager) keepAlive() {
	defer m.keepaliveTicker.Stop()

	writePing := func() error {
		ping := newPacket(127, PING, []byte{byte(m.cfg.KeepaliveInterval.Seconds())})
		return m.writePacket(ping)
	}

	for range m.keepaliveTicker.C {
		if err := writePing(); err != nil {
			if m.cfg.DebugLog {
				log.Printf("send ping failed: %+v", err)
			}
			return
		}
	}
}

// readPacket read a packet from the underlayer conn.
func (m *manager) readPacket() (*Packet, error) {
	return decodeFrom(m.conn)
}

// writePacket write a packet to other side over the underlayer conn.
func (m *manager) writePacket(p *Packet) error {
	req := writeRequest{
		packet:  p,
		written: make(chan struct{}),
	}

	select {
	case <-m.ctx.Done():
		return ErrManagerClosed
	case m.writes <- req:
	}

	select {
	case <-m.ctx.Done():
		return ErrManagerClosed
	case <-req.written:
		return nil
	}
}

// Close close the manager and close all links belong to this manager.
func (m *manager) Close() (err error) {
	select {
	case <-m.ctx.Done():
		return
	default:
		m.ctxCloseFunc()
	}

	m.links.Range(func(_, value interface{}) bool {
		value.(*link).closeByPeer()
		return true
	})

	if m.keepaliveTicker != nil {
		m.keepaliveTicker.Stop()
		select {
		case <-m.keepaliveTicker.C:
		default:
		}
	}

	return errors.WithStack(m.conn.Close())
}

// removeLink recv FIN and send FIN will remove link.
func (m *manager) removeLink(id uint32) {
	m.links.Delete(id)
}

// readLoop read packet forever until manager is closed.
func (m *manager) readLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		if m.timeout > 0 {
			if err := m.conn.SetReadDeadline(time.Now().Add(m.timeout)); err != nil {
				if m.cfg.DebugLog {
					log.Printf("set read dealine failed: %+v", errors.WithStack(err))
				}
				m.Close()
				return
			}
		}

		packet, err := m.readPacket()
		if err != nil {
			if m.cfg.DebugLog {
				log.Printf("read packet failed: %+v", err)
			}
			m.Close()
			return
		}

		switch packet.CMD {
		case NEW:
			if !(m.usedIDs[packet.ID]) {
				link := newLink(packet.ID, m, m.cfg.Mode)
				m.usedIDs[packet.ID] = true
				m.links.Store(link.ID, link)

				link.pushPacket(packet)

				m.acceptQueue <- link
			}

		case PSH, ACK, CLOSE, ACPT:
			if l, ok := m.links.Load(packet.ID); ok {
				l.(*link).pushPacket(packet)
			}

		case PING:
			m.initTimerOnce.Do(func() {
				m.cfg.KeepaliveInterval = time.Duration(packet.Payload[0]) * time.Second
				m.timeout = 2 * m.cfg.KeepaliveInterval

				m.keepaliveTicker = time.NewTicker(m.cfg.KeepaliveInterval)
				go m.keepAlive()
			})
		}
	}
}

// writeLoop write packet to other side forever until manager is closed.
func (m *manager) writeLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case req := <-m.writes:
			if m.cfg.KeepaliveInterval != 0 {
				if err := m.conn.SetWriteDeadline(time.Now().Add(m.cfg.KeepaliveInterval)); err != nil {
					if m.cfg.DebugLog {
						log.Printf("manager set write deadline failed: %+v", errors.WithStack(err))
					}
					m.Close()
					return
				}
			}

			if _, err := m.conn.Write(req.packet.bytes()); err != nil {
				if m.cfg.DebugLog {
					log.Printf("manager writeLoop failed: %+v", errors.WithStack(err))
				}
				m.Close()
				return
			}

			close(req.written)
		}
	}
}

// Dial create a Link, if manager is closed, err != nil.
func (m *manager) Dial(ctx context.Context) (Link, error) {
	return m.DialData(ctx, nil)
}

func (m *manager) DialData(ctx context.Context, b []byte) (Link, error) {
	link := newLink(uint32(atomic.AddInt32(&m.maxID, 1)), m, m.cfg.Mode)

	select {
	case <-m.ctx.Done():
		return nil, ErrManagerClosed

	default:
		m.links.Store(link.ID, link)

		// tell readableBufSize
		buf := make([]byte, 4+len(b))
		binary.BigEndian.PutUint32(buf, uint32(m.cfg.ReadBufSize))

		// write optional data
		copy(buf[4:], b)

		newP := newPacket(link.ID, NEW, buf)
		if err := m.writePacket(newP); err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			var err error
			if ctx.Err() == context.DeadlineExceeded {
				err = ErrTimeout
			} else {
				err = errors.WithStack(err)
			}

			return nil, err

		case <-link.dialCtx.Done():
			return link, nil
		}
	}
}

// Accept accept a new Link, if manager is closed, err != nil.
func (m *manager) Accept() (Link, error) {
	var l *link
	select {
	case <-m.ctx.Done():
		return nil, ErrManagerClosed

	case l = <-m.acceptQueue:
		readableBufSize := make([]byte, 4)
		size := atomic.LoadInt32(&l.readableBufSize)
		if size < 0 {
			size = 0
		}
		binary.BigEndian.PutUint32(readableBufSize, uint32(size))

		ackNewPacket := newPacket(l.ID, ACPT, readableBufSize)
		if err := m.writePacket(ackNewPacket); err != nil {
			return nil, err
		}
		return l, nil
	}
}

// IsClosed return if the manager closed or not.
func (m *manager) IsClosed() bool {
	select {
	case <-m.ctx.Done():
		return true
	default:
		return false
	}
}
