package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type writeRequest struct {
	packet  *Packet
	written chan struct{} // if written, close this chan
}

// Manager manager will manage some links.
type Manager struct {
	conn io.ReadWriteCloser

	links     map[uint32]*Link
	linksLock sync.Mutex

	maxID   int32
	usedIDs map[uint32]bool

	ctx           context.Context    // ctx.Done() can recv means manager is closed
	ctxCancelFunc context.CancelFunc // close the manager
	ctxLock       sync.Mutex         // ensure manager close one time

	writes chan writeRequest // write chan limit link don't write too qiuckly

	acceptQueue chan *Link // accept queue, call Accept() will get a waiting link

	interval time.Duration // keepalive interval

	timeoutTimer *time.Timer // timer check if the manager is timeout
	timeout      time.Duration
	initTimer    sync.Once // make sure Timer init once

	keepaliveTicker *time.Ticker // ticker will send ping regularly
}

// NewManager create a manager based on conn, if config is nil, will use DefaultConfig.
func NewManager(conn io.ReadWriteCloser, config *Config) *Manager {
	ctx, cancelFunc := context.WithCancel(context.Background())

	manager := &Manager{
		conn: conn,

		links: make(map[uint32]*Link),

		maxID:   -1,
		usedIDs: make(map[uint32]bool),

		ctx:           ctx,
		ctxCancelFunc: cancelFunc,

		writes: make(chan writeRequest, 1),
	}

	if config == nil {
		config = DefaultConfig
	}

	// manager.writes = make(chan writeRequest, config.WriteRequests)
	manager.acceptQueue = make(chan *Link, config.AcceptQueueSize)

	if config.KeepaliveInterval > 0 {
		manager.interval = config.KeepaliveInterval

		manager.keepaliveTicker = time.NewTicker(config.KeepaliveInterval)

		go manager.keepAlive()
	}

	go manager.readLoop()
	go manager.writeLoop()

	// debug
	/*go func() {
		for {
			time.Sleep(2 * time.Second)
			manager.linksLock.Lock()
			fmt.Println("links size", len(manager.links), manager.links)
			manager.linksLock.Unlock()
		}
	}()*/

	return manager
}

// keepAlive send PING packet to other side to keepalive.
func (m *Manager) keepAlive() {
	defer m.keepaliveTicker.Stop()

	for range m.keepaliveTicker.C {
		ping := newPacket(127, PING, []byte{byte(m.interval.Seconds())})
		if err := m.writePacket(ping); err != nil {
			log.Println("send ping failed", err)
			return
		}
	}
}

// readPacket read a packet from the underlayer conn.
func (m *Manager) readPacket() (*Packet, error) {
	header := make(PacketHeader, HeaderLength)
	if _, err := io.ReadFull(m.conn, header); err != nil {
		return nil, fmt.Errorf("manager read packet header: %s", err)
	}

	if header.version() != Version {
		return nil, VersionErr{header.version()}
	}

	var payload []byte

	if length := header.payloadLength(); length != 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(m.conn, payload); err != nil {
			return nil, fmt.Errorf("manager read packet payload: %s", err)
		}
	}

	packet, err := decode(append(header, payload...))
	if err != nil {
		return nil, fmt.Errorf("manager read packet decode: %s", err)
	}

	return packet, nil
}

// writePacket write a packet to other side over the underlayer conn.
func (m *Manager) writePacket(p *Packet) error {
	req := writeRequest{
		packet:  p,
		written: make(chan struct{}),
	}

	select {
	case <-m.ctx.Done():
		return io.ErrClosedPipe
	case m.writes <- req:
	}

	select {
	case <-m.ctx.Done():
		return io.ErrClosedPipe
	case <-req.written:
		return nil
	}
}

// Close close the manager and close all links belong to this manager.
func (m *Manager) Close() error {
	m.ctxLock.Lock()

	select {
	case <-m.ctx.Done():
		m.ctxLock.Unlock()
		return nil
	default:
		m.ctxCancelFunc()
		m.ctxLock.Unlock()

		m.linksLock.Lock()
		for _, link := range m.links {
			link.managerClosed()
		}
		m.linksLock.Unlock()

		if m.keepaliveTicker != nil {
			m.keepaliveTicker.Stop()
			select {
			case <-m.keepaliveTicker.C:
			default:
			}
		}

		if m.timeoutTimer != nil {
			m.timeoutTimer.Stop()
		}

		return m.conn.Close()
	}
}

// removeLink recv FIN and send FIN will remove link.
func (m *Manager) removeLink(id uint32) {
	delete(m.links, id)
}

// readLoop read packet forever until manager is closed.
func (m *Manager) readLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		packet, err := m.readPacket()
		if err != nil {
			log.Println(err)
			m.Close()
			return
		}

		if m.timeoutTimer != nil {
			m.timeoutTimer.Stop()
			m.timeoutTimer.Reset(m.timeout)
		}

		switch packet.CMD {
		case PSH:
			m.linksLock.Lock()
			if link, ok := m.links[packet.ID]; ok {
				link.pushPacket(packet)

			} else {
				// check id is used or not,
				// make sure don't miss id and don't reopen a closed link.
				if !(m.usedIDs[uint32(packet.ID)]) {
					link := newLink(packet.ID, m)
					m.usedIDs[uint32(packet.ID)] = true
					m.links[link.ID] = link

					link.pushPacket(packet)

					m.acceptQueue <- link
				}
			}

			m.linksLock.Unlock()

		case ACK, FIN, RST:
			m.linksLock.Lock()
			if link, ok := m.links[packet.ID]; ok {
				m.linksLock.Unlock()
				link.pushPacket(packet)
				continue // skip the following Unlock()
			}
			m.linksLock.Unlock()

		case PING:
			timeout := 3 * time.Duration(packet.Payload[0]) * time.Second
			m.timeout = timeout

			m.initTimer.Do(func() {
				m.timeoutTimer = time.AfterFunc(timeout, func() {
					log.Println("manager timeout")
					m.Close()
				})

				log.Println("init manager timer")
			})

			m.timeoutTimer.Stop()
			m.timeoutTimer.Reset(timeout)
		}
	}
}

// writeLoop write packet to other side forever until manager is closed.
func (m *Manager) writeLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case req := <-m.writes:
			_, err := m.conn.Write(req.packet.bytes())
			if err != nil {
				log.Println("manager writeLoop:", err)
				m.Close()
				return
			}
			close(req.written)
		}
	}
}

// NewLink create a Link, if manager is closed, err != nil.
func (m *Manager) NewLink() (link *Link, err error) {
	link = newLink(uint32(atomic.AddInt32(&m.maxID, 1)), m)

	select {
	case <-m.ctx.Done():
		return nil, errors.New("manager closed")
	default:
		m.linksLock.Lock()
		m.links[link.ID] = link
		m.linksLock.Unlock()
		return link, nil
	}
}

// Accept accept a new Link, if manager is closed, err != nil.
func (m *Manager) Accept() (link *Link, err error) {
	select {
	case <-m.ctx.Done():
		return nil, errors.New("broken manager")
	case link = <-m.acceptQueue:
		return link, nil
	}
}

// IsClosed return if the manager closed or not.
func (m *Manager) IsClosed() bool {
	select {
	case <-m.ctx.Done():
		return true
	default:
		return false
	}
}
