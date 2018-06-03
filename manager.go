package link

import (
    "io"
    "github.com/satori/go.uuid"
    "sync"
    "log"
    "encoding/binary"
)

type Manager struct {
    conn io.ReadWriteCloser

    links     map[uuid.UUID]*Link
    linksLock sync.Mutex

    die     chan struct{}
    dieLock sync.Mutex

    writes chan *Packet
}

func NewManager(conn io.ReadWriteCloser) *Manager {
    return &Manager{
        conn: conn,

        links: make(map[uuid.UUID]*Link),

        die: make(chan struct{}),

        writes: make(chan *Packet, 1000),
    }
}

func (m *Manager) readPacket() (*Packet, error) {
    header := PacketHeader(make([]byte, HeaderLength))
    _, err := io.ReadFull(m.conn, header)
    if err != nil {
        return nil, err
    }

    var payload []byte

    if header.PayloadLength() != 0 {
        payload = make([]byte, header.PayloadLength())
        _, err = io.ReadFull(m.conn, payload)
        if err != nil {
            return nil, err
        }
    }

    packet, err := Decode(append(header, payload...))
    if err != nil {
        return nil, err
    }

    return packet, nil
}

func (m *Manager) writePacket(p *Packet) error {
    m.dieLock.Lock()
    defer m.dieLock.Unlock()

    select {
    case <-m.die:
        return io.ErrUnexpectedEOF

    default:
        m.writes <- p
        return nil
    }
}

func (m *Manager) readLoop() {
    for {
        if m.IsClosed() {
            break
        }

        packet, err := m.readPacket()
        if err != nil {
            log.Println(LowLevelErr)
            // close all links
        }

        switch {
        case packet.PSH:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                link.pushBytes(packet.Payload)
            } else {
                link := newLink(packet.ID, m)
                link.pushBytes(packet.Payload)
                m.links[packet.ID] = link
            }
            m.linksLock.Unlock()

        case packet.FIN:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                if err := link.CloseRead(); err != nil {
                    log.Printf("readLoop: %s", err)
                }
            }
            m.linksLock.Unlock()

        case packet.RST:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                link.rst()
            }
            m.linksLock.Unlock()

        case packet.ACK:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                link.ack(binary.BigEndian.Uint32(packet.Payload))
            }
            m.linksLock.Unlock()
        }
    }
}

func (m *Manager) IsClosed() bool {
    select {
    case <-m.die:
        return true

    default:
        return false
    }
}

func (m *Manager) removeLink(id uuid.UUID) {
    delete(m.links, id)
}
