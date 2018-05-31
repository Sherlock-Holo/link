package link

import (
    "bytes"
    "context"
    "encoding/binary"
    "errors"
    "log"
    "net"
    "sync"

    "github.com/satori/go.uuid"
    "golang.org/x/sync/semaphore"
)

type Manager struct {
    lowLevel net.Conn
    m        map[uuid.UUID]*Link

    err error

    writeChan chan *Packet

    acceptChan chan *Link
}

func NewManager(conn net.Conn) *Manager {
    manager := Manager{
        lowLevel:   conn,
        m:          make(map[uuid.UUID]*Link),
        writeChan:  make(chan *Packet, 10000),
        acceptChan: make(chan *Link, 1000),
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
                for _, link := range manager.m {
                    go func(link *Link) {
                        link.Status = LowLevelErr
                        link.ctxCancelFunc()
                        link.cond.Signal()
                    }(link)
                }
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
                    for _, link := range manager.m {
                        go func(link *Link) {
                            link.Status = LowLevelErr
                            link.ctxCancelFunc()
                            link.cond.Signal()
                        }(link)
                    }
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
            for _, link := range manager.m {
                go func(link *Link) {
                    link.Status = RST
                    link.ctxCancelFunc()
                    link.cond.Signal()
                }(link)
            }
            break
        }

        link, ok := manager.m[packet.ID]

        if ok {
            link.readChan <- packet
        } else {
            locker := sync.Mutex{}
            locker.Lock()

            ctx, cancelFunc := context.WithCancel(context.Background())

            link := Link{
                ID:            packet.ID,
                Status:        ESTAB,
                manager:       manager,
                readChan:      make(chan *Packet, 1000),
                readBuf:       bytes.NewBuffer(nil),
                locker:        locker,
                cond:          sync.NewCond(&locker),
                semaphore:     semaphore.NewWeighted(maxBufSize),
                ctx:           ctx,
                ctxCancelFunc: cancelFunc,
            }

            link.readChan <- packet

            manager.m[link.ID] = &link
            manager.acceptChan <- &link
        }
    }
}

func (manager *Manager) write() {
    for packet := range manager.writeChan {
        _, err := manager.lowLevel.Write(packet.Bytes())
        if err != nil {
            log.Println(err)
            manager.err = LowLevelErr
            for _, link := range manager.m {
                go func(link *Link) {
                    link.Status = LowLevelErr
                    link.ctxCancelFunc()
                    link.cond.Signal()
                }(link)
            }
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
        ID:            uid,
        Status:        ESTAB,
        manager:       manager,
        readChan:      make(chan *Packet, 1000),
        readBuf:       bytes.NewBuffer(nil),
        locker:        locker,
        cond:          sync.NewCond(&locker),
        semaphore:     semaphore.NewWeighted(maxBufSize),
        ctx:           ctx,
        ctxCancelFunc: cancelFunc,
    }

    if b != nil {
        link.Write(b)
    }
    manager.m[link.ID] = &link
    go link.read()
    return &link, nil
}
