package link

import (
	"context"
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

	interval time.Duration // keepalive interval

	timeoutTimer  *time.Timer // timer check if the manager is timeout
	timeout       time.Duration
	initTimerOnce sync.Once // make sure Timer init once

	keepaliveTicker *time.Ticker // ticker will send ping regularly

	debugLog bool

	mode mode // manager run mode
}

// NewManager create a manager based on conn, if config is nil, will use DefaultConfig.
func NewManager(conn net.Conn, config Config) Manager {
	manager := &manager{
		conn: conn,

		maxID:   -1,
		usedIDs: make(map[uint32]bool),

		writes: make(chan writeRequest, 1),
	}

	manager.ctx, manager.ctxCloseFunc = context.WithCancel(context.Background())

	manager.debugLog = config.DebugLog

	manager.mode = config.Mode

	manager.acceptQueue = make(chan *link, config.AcceptQueueSize)

	if manager.mode == ClientMode && manager.interval > 0 {
		manager.initTimerOnce.Do(func() {
			if config.KeepaliveInterval > 0 {
				manager.interval = config.KeepaliveInterval
				manager.timeout = 2 * manager.interval

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
		ping := newPacket(127, PING, []byte{byte(m.interval.Seconds())})
		return m.writePacket(ping)
	}

	for range m.keepaliveTicker.C {
		if err := writePing(); err != nil {
			if m.debugLog {
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
				if m.debugLog {
					log.Printf("set read dealine failed: %+v", errors.WithStack(err))
				}
				m.Close()
				return
			}
		}

		packet, err := m.readPacket()
		if err != nil {
			if m.debugLog {
				log.Printf("read packet failed: %+v", err)
			}
			m.Close()
			return
		}

		switch packet.CMD {
		case NEW:
			if !(m.usedIDs[packet.ID]) {
				link := newLink(packet.ID, m, m.mode)
				m.usedIDs[packet.ID] = true
				m.links.Store(link.ID, link)

				link.pushPacket(packet)

				m.acceptQueue <- link
			}

		/*case PSH:
		if l, ok := m.links.Load(packet.ID); ok {
			l.(*link).pushPacket(packet)
		} else {
			// check id is used or not,
			// make sure don't miss id and don't reopen a closed link.
			if !(m.usedIDs[packet.ID]) {
				link := dial(packet.ID, m)
				m.usedIDs[packet.ID] = true
				m.links.Store(link.ID, link)

				link.pushPacket(packet)

				m.acceptQueue <- link
			}
		}*/

		case PSH, ACK, CLOSE:
			if l, ok := m.links.Load(packet.ID); ok {
				l.(*link).pushPacket(packet)
			}

		case PING:
			m.initTimerOnce.Do(func() {
				m.interval = time.Duration(packet.Payload[0]) * time.Second
				m.timeout = 2 * m.interval

				m.keepaliveTicker = time.NewTicker(m.interval)
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
			bytes := req.packet.bytes()
			if _, err := m.conn.Write(bytes); err != nil {
				if m.debugLog {
					log.Println("manager writeLoop:", err)
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
	link := newLink(uint32(atomic.AddInt32(&m.maxID, 1)), m, m.mode)

	select {
	case <-m.ctx.Done():
		return nil, errors.New("manager closed")

	default:
		m.links.Store(link.ID, link)

		newP := newPacket(link.ID, NEW, b)
		if err := m.writePacket(newP); err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			m.Close()
			return nil, errors.WithStack(ctx.Err())

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
		return nil, errors.New("broken manager")
	case l = <-m.acceptQueue:
		ackNewPacket := newPacket(l.ID, ACK, nil)
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
