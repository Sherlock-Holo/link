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

/*type Manager struct {
    lowLevel net.Conn
    m        *sync.Map

    err error

    writeChan       chan *Packet
    writeChanClosed bool
    writeChanMutex  sync.Mutex

    acceptChan chan *Link

    once *sync.Once
}

func NewManager(conn net.Conn) *Manager {
    manager := Manager{
        lowLevel: conn,
        m:        &sync.Map{},

        writeChan: make(chan *Packet, 10000),

        acceptChan: make(chan *Link, 1000),

        once: new(sync.Once),
    }

    go manager.read()
    go manager.write()
    return &manager
}

func (manager *Manager) read() {
    for {
        var (
            b      = make([]byte, HeaderLength)
            length int
        )
        for length < HeaderLength {
            n, err := manager.lowLevel.Read(b[length:])

            // notify all link: manager is error
            if err != nil {
                log.Println(err)
                manager.err = LowLevelErr

                manager.once.Do(func() {
                    manager.writeChanMutex.Lock()
                    close(manager.writeChan)
                    manager.writeChanClosed = true
                    manager.writeChanMutex.Unlock()

                    manager.m.Range(func(_, value interface{}) bool {
                        link := value.(*Link)
                        go func() {
                            link.statusLock.Lock()
                            link.Status = LowLevelErr
                            link.writeCtxCancelFunc()
                            link.cond.Signal()
                            link.statusLock.Unlock()
                        }()
                        return true
                    })
                })
                break
            }
            length += n
        }

        payloadLength := binary.BigEndian.Uint16(b[HeaderLength-2:])

        if payloadLength != 0 {
            length = 0
            payload := make([]byte, payloadLength)

            for length < int(payloadLength) {
                n, err := manager.lowLevel.Read(payload[length:])

                // notify all link: manager is error
                if err != nil {
                    log.Println(err)
                    manager.err = LowLevelErr

                    manager.once.Do(func() {
                        manager.writeChanMutex.Lock()
                        close(manager.writeChan)
                        manager.writeChanClosed = true
                        manager.writeChanMutex.Unlock()

                        manager.m.Range(func(_, value interface{}) bool {
                            link := value.(*Link)
                            go func() {
                                link.statusLock.Lock()
                                link.Status = LowLevelErr
                                link.writeCtxCancelFunc()
                                link.cond.Signal()
                                link.statusLock.Unlock()
                            }()
                            return true
                        })
                    })
                    break
                }
                length += n
            }

            b = append(b, payload...)
        }

        packet, err := Decode(b)

        // notify all link: manager is error
        if err != nil {
            log.Println(err)
            manager.err = err

            manager.once.Do(func() {
                manager.writeChanMutex.Lock()
                close(manager.writeChan)
                manager.writeChanClosed = true
                manager.writeChanMutex.Unlock()

                manager.m.Range(func(_, value interface{}) bool {
                    link := value.(*Link)
                    go func() {
                        link.statusLock.Lock()
                        link.Status = LowLevelErr
                        link.writeCtxCancelFunc()
                        link.cond.Signal()
                        link.statusLock.Unlock()
                    }()
                    return true
                })
            })
            break
        }

        locker := sync.Mutex{}
        locker.Lock()

        ctx, cancelFunc := context.WithCancel(context.Background())

        newLink := Link{
            ID:     packet.ID,
            Status: ESTAB,

            manager:  manager,
            readChan: make(chan *Packet, 1000),
            buf:  bytes.NewBuffer(nil),
            cond:     sync.NewCond(&locker),

            semaphore:          semaphore.NewWeighted(maxBufSize),
            writeCtx:           ctx,
            writeCtxCancelFunc: cancelFunc,
        }

        value, loaded := manager.m.LoadOrStore(packet.ID, &newLink)

        if loaded {
            link := value.(*Link)
            if !link.readClosed {
                link.readChan <- packet
            } else {
                close(link.readChan)
                manager.m.Delete(link.ID)
            }

        } else {
            newLink.readChan <- packet
            manager.acceptChan <- &newLink
        }
    }
}

func (manager *Manager) write() {
    for packet := range manager.writeChan {
        _, err := manager.lowLevel.Write(packet.Bytes())
        if err != nil {
            log.Println(err)
            manager.err = LowLevelErr

            manager.once.Do(func() {
                manager.writeChanMutex.Lock()
                close(manager.writeChan)
                manager.writeChanClosed = true
                manager.writeChanMutex.Unlock()

                manager.m.Range(func(_, value interface{}) bool {
                    link := value.(*Link)
                    go func() {
                        link.statusLock.Lock()
                        link.Status = LowLevelErr
                        link.writeCtxCancelFunc()
                        link.cond.Signal()
                        link.statusLock.Unlock()
                    }()
                    return true
                })
            })
            break
        }
    }
}

func (manager *Manager) Accept() (*Link, error) {
    if manager.err != nil {
        return nil, manager.err
    }

    link := <-manager.acceptChan
    if link == nil {
        return nil, errors.New("manager error")
    }
    go link.read()
    return link, nil
}

func (manager *Manager) NewLink(b []byte) (*Link, error) {
    if manager.err != nil {
        return nil, manager.err
    }

    uid, _ := uuid.NewV4()
    locker := sync.Mutex{}
    locker.Lock()
    ctx, cancelFunc := context.WithCancel(context.Background())

    link := Link{
        ID:     uid,
        Status: ESTAB,

        manager:  manager,
        readChan: make(chan *Packet, 1000),

        buf: bytes.NewBuffer(nil),
        cond:    sync.NewCond(&locker),

        semaphore:          semaphore.NewWeighted(maxBufSize),
        writeCtx:           ctx,
        writeCtxCancelFunc: cancelFunc,
    }

    if manager.err != nil {
        return nil, manager.err
    }

    if b != nil {
        _, err := link.Write(b)
        if err != nil {
            return nil, err
        }
    }

    manager.m.Store(link.ID, &link)
    go link.read()
    return &link, nil
}
*/
