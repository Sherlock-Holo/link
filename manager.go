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

const (
	maxBucketSize = 1 << 18
)

type writeRequest struct {
	packet  *Packet
	written chan struct{} // if written, close this chan
}

type Manager struct {
	conn io.ReadWriteCloser

	links     map[uint32]*Link
	linksLock sync.Mutex

	maxID   int32
	usedIDs map[uint32]bool

	bucket      int32         // read bucket, only manager readLoop and link.Read will modify it.
	bucketEvent chan struct{} // every time recv PSH and bucket is bigger than 0, will notify, link.Read will modify.

	ctx           context.Context    // ctx.Done() can recv means manager is closed
	ctxCancelFunc context.CancelFunc // close the manager
	ctxLock       sync.Mutex         // ensure manager close one time

	writes chan writeRequest

	acceptQueue chan *Link

	timeout         time.Duration
	timeoutTimer    *time.Timer
	keepAliveTicker *time.Ticker
}

func NewManager(conn io.ReadWriteCloser, config *Config) *Manager {
	ctx, cancelFunc := context.WithCancel(context.Background())

	manager := &Manager{
		conn: conn,

		links: make(map[uint32]*Link),

		maxID:   -1,
		usedIDs: make(map[uint32]bool),

		bucket:      maxBucketSize,
		bucketEvent: make(chan struct{}, 1),

		ctx:           ctx,
		ctxCancelFunc: cancelFunc,
	}

	if config == nil {
		config = DefaultConfig
	}

	manager.writes = make(chan writeRequest, config.WriteRequests)
	manager.acceptQueue = make(chan *Link, config.AcceptQueueSize)

	manager.timeout = config.Timeout

	manager.timeoutTimer = time.AfterFunc(config.Timeout, func() {
		manager.Close()
	})

	manager.keepAliveTicker = time.NewTicker(config.Timeout / 2)

	manager.bucketNotify()

	go manager.readLoop()
	go manager.writeLoop()
	go manager.keepAlive()
	return manager
}

func (m *Manager) keepAlive() {
	defer m.keepAliveTicker.Stop()

	for range m.keepAliveTicker.C {
		ping := newPacket(127, "PING", nil)
		if err := m.writePacket(ping); err != nil {
			log.Println("send ping failed")
			return
		}
	}
}

func (m *Manager) bucketNotify() {
	select {
	case m.bucketEvent <- struct{}{}:
	default:
	}
}

func (m *Manager) readPacket() (*Packet, error) {
	header := make(PacketHeader, HeaderLength)
	if _, err := io.ReadFull(m.conn, header); err != nil {
		return nil, fmt.Errorf("manager read packet header: %s", err)
	}

	if header.Version() != Version {
		return nil, VersionErr{header.Version()}
	}

	var payload []byte

	if length := header.PayloadLength(); length != 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(m.conn, payload); err != nil {
			return nil, fmt.Errorf("manager read packet payload: %s", err)
		}
	}

	packet, err := Decode(append(header, payload...))
	if err != nil {
		return nil, fmt.Errorf("manager read packet decode: %s", err)
	}

	return packet, nil
}

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

		return m.conn.Close()
	}
}

func (m *Manager) returnToken(n int) {
	if atomic.AddInt32(&m.bucket, int32(n)) > 0 {
		m.bucketNotify()
	}
}

// recv FIN and send FIN will remove link
func (m *Manager) removeLink(id uint32) {
	delete(m.links, id)
}

func (m *Manager) readLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.bucketEvent:
		}

		packet, err := m.readPacket()
		if err != nil {
			log.Println(err)
			m.Close()
			return
		}

		m.timeoutTimer.Reset(m.timeout)

		switch packet.CMD {
		case PSH:
			m.linksLock.Lock()
			if link, ok := m.links[packet.ID]; ok {
				link.pushBytes(packet.Payload)
			} else {
				// check id is used or not,
				// make sure don't miss id and don't reopen a closed link.
				if !(m.usedIDs[uint32(packet.ID)]) {
					link := newLink(packet.ID, m)
					m.usedIDs[uint32(packet.ID)] = true
					m.links[link.ID] = link
					link.pushBytes(packet.Payload)
					m.acceptQueue <- link
				}
			}
			m.linksLock.Unlock()
			if atomic.AddInt32(&m.bucket, -int32(packet.Length)) > 0 {
				m.bucketNotify()
			}

		case FIN:
			m.linksLock.Lock()
			if link, ok := m.links[packet.ID]; ok {
				link.close()
			}
			m.linksLock.Unlock()

		case PING:
		}
	}
}

func (m *Manager) writeLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case req := <-m.writes:
			_, err := m.conn.Write(req.packet.Bytes())
			if err != nil {
				log.Println("manager writeLoop:", err)
				m.Close()
				return
			}
			close(req.written)
		}
	}
}

func (m *Manager) NewLink() (*Link, error) {
	link := newLink(uint32(atomic.AddInt32(&m.maxID, 1)), m)

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

func (m *Manager) Accept() (*Link, error) {
	select {
	case <-m.ctx.Done():
		return nil, errors.New("broken manager")
	case link := <-m.acceptQueue:
		return link, nil
	}
}
