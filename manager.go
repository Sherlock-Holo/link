package link

import (
    "io"
    "github.com/satori/go.uuid"
    "sync"
    "fmt"
    "context"
    "log"
    "encoding/binary"
)

type Manager struct {
    conn io.ReadWriteCloser

    links     map[uuid.UUID]*Link
    linksLock sync.Mutex

    ctx           context.Context
    ctxCancelFunc context.CancelFunc

    writes chan *Packet

    acceptQueue chan *Link
}

func NewManager(conn io.ReadWriteCloser) *Manager {
    ctx, cancelFunc := context.WithCancel(context.Background())

    manager := &Manager{
        conn: conn,

        links: make(map[uuid.UUID]*Link),

        ctx:           ctx,
        ctxCancelFunc: cancelFunc,

        writes: make(chan *Packet, 1000),

        acceptQueue: make(chan *Link, 1000),
    }
    go manager.readLoop()
    go manager.writeLoop()
    return manager
}

func (m *Manager) readPacket() (*Packet, error) {
    header := make(PacketHeader, HeaderLength)
    if _, err := io.ReadFull(m.conn, header); err != nil {
        return nil, fmt.Errorf("manager read packet header: %s", err)
    }

    var payload []byte

    if header.PayloadLength() != 0 {
        payload = make([]byte, header.PayloadLength())
        if _, err := io.ReadFull(m.conn, payload); err != nil {
            return nil, fmt.Errorf("manager read packet payload: %s", err)
        }
    }

    packet, err := Decode(append(header, payload...))
    if err != nil {
        return nil, fmt.Errorf("manager decode packet failed: %s", err)
    }

    return packet, nil
}

func (m *Manager) writePacket(p *Packet) {
    select {
    case <-m.ctx.Done():
    default:
        m.writes <- p
    }
}

func (m *Manager) removeLink(id uuid.UUID) {
    delete(m.links, id)
}

func (m *Manager) readLoop() {
    for {
        select {
        case m.ctx.Done():
            return
        default:
        }

        packet, err := m.readPacket()
        if err != nil {
            log.Println(err)
            m.close()

            for _, link := range m.links {
                link.close()
            }
            return
        }

        switch {
        case packet.PSH:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                link.pushBytes(packet.Payload)
            } else {
                link := newLink(packet.ID, m)
                link.pushBytes(packet.Payload)
                m.acceptQueue <- link
            }
            m.linksLock.Unlock()

        case packet.ACK:
            if link, ok := m.links[packet.ID]; ok {
                link.ack(binary.BigEndian.Uint32(packet.Payload))
            }

        case packet.FIN:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                link.close()
                m.removeLink(link.ID)
            }
            m.linksLock.Unlock()
        }
    }
}

func (m *Manager) writeLoop() {
    for {
        select {
        case <-m.ctx.Done():
            return

        case packet := <-m.writes:
            _, err := m.conn.Write(packet.Bytes())
            if err != nil {
                log.Printf("manager writeLoop: %s", err)
                m.close()
                for _, link := range m.links {
                    link.close()
                }
                return
            }
        }
    }
}

func (m *Manager) close() error {
    m.ctxCancelFunc()
    return nil
}
