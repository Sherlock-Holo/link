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

    acceptQueue chan *Link
}

func NewManager(conn io.ReadWriteCloser) *Manager {
    manager := &Manager{
        conn: conn,

        links: make(map[uuid.UUID]*Link),

        die: make(chan struct{}),

        writes: make(chan *Packet, 1000),

        acceptQueue: make(chan *Link, 1000),
    }

    go manager.readLoop()
    go manager.writeLoop()
    return manager
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
    /*m.dieLock.Lock()
    defer m.dieLock.Unlock()*/

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
            return
        }

        packet, err := m.readPacket()
        if err != nil {
            log.Println(LowLevelErr)
            select {
            case <-m.die:
            default:
                close(m.die)
            }
            // close all links
            m.linksLock.Lock()
            for _, link := range m.links {
                link.rst()
            }
            m.linksLock.Unlock()
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
                m.links[packet.ID] = link
                m.acceptQueue <- link
            }
            m.linksLock.Unlock()

        case packet.FIN:
            m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                if err := link.CloseRead(); err != nil {
                    log.Printf("link %s CloseRead: %s", link.ID.String(), err)
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
            // m.linksLock.Lock()
            if link, ok := m.links[packet.ID]; ok {
                link.ack(binary.BigEndian.Uint32(packet.Payload))
            }
            // m.linksLock.Unlock()
        }
    }
}

func (m *Manager) writeLoop() {
    for {
        select {
        case <-m.die:
            return

        case packet := <-m.writes:
            if _, err := m.conn.Write(packet.Bytes()); err != nil {
                log.Println(err)
                select {
                case <-m.die:
                default:
                    close(m.die)
                }
                // close all links
                m.linksLock.Lock()
                for _, link := range m.links {
                    link.rst()
                }
                m.linksLock.Unlock()
                return
            }
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

func (m *Manager) NewLink() (*Link, error) {
    id, _ := uuid.NewV4()
    link := newLink(id, m)

    select {
    case <-m.die:
        return nil, io.ErrClosedPipe

    default:
        m.linksLock.Lock()
        m.links[link.ID] = link
        m.linksLock.Unlock()
        return link, nil
    }
}

func (m *Manager) Accept() (*Link, error) {
    select {
    case <-m.die:
        return nil, io.ErrClosedPipe

    case link := <-m.acceptQueue:
        return link, nil
    }
}
