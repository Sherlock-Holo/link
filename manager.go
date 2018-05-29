package link

import (
    "bytes"
    "context"
    "errors"
    "net"
    "sync"

    "github.com/satori/go.uuid"
    "golang.org/x/sync/semaphore"
)

type Manager struct {
    lowLevel net.Conn
    accepted bool
}

func NewManager(conn net.Conn) *Manager {
    return &Manager{lowLevel: conn}
}

func (m *Manager) Accept() (*Link, error) {
    var (
        syn    = make([]byte, HeaderLength)
        length = 0
    )

    for length < HeaderLength {
        n, err := m.lowLevel.Read(syn[length:])
        if err != nil {
            return nil, err
        }
        length += n
    }

    synPacket, err := Decode(syn)
    if err != nil {
        return nil, err
    }

    if !synPacket.SYN {
        return nil, errors.New("handshake failed")
    }

    ctx, cancelFunc := context.WithCancel(context.Background())

    mutex := sync.Mutex{}
    mutex.Lock()

    link := &Link{
        ID:            synPacket.ID,
        Status:        SYN,
        lowLevel:      m.lowLevel,
        readBuf:       bytes.NewBuffer(nil),
        locker:        mutex,
        cond:          sync.NewCond(&mutex),
        semaphore:     semaphore.NewWeighted(maxBufSize),
        ctx:           ctx,
        ctxCancelFunc: cancelFunc,
    }

    ackPacket := Packet{
        ID:  link.ID,
        SYN: true,
        ACK: true,
    }

    _, err = link.lowLevel.Write(ackPacket.Bytes())
    if err != nil {
        return nil, err
    }

    link.Status = ESTAB
    go link.read()

    return link, nil
}

func (m *Manager) Connect(id uuid.UUID) (*Link, error) {
    synPacket := Packet{
        ID:  id,
        SYN: true,
    }

    _, err := m.lowLevel.Write(synPacket.Bytes())
    if err != nil {
        return nil, err
    }

    var (
        ack    = make([]byte, HeaderLength)
        length = 0
    )

    for length < HeaderLength {
        n, err := m.lowLevel.Read(ack[length:])
        if err != nil {
            return nil, err
        }
        length += n
    }

    ackPacket, err := Decode(ack)
    if err != nil {
        return nil, err
    }

    if ackPacket.SYN != true || ackPacket.ACK != true {
        return nil, errors.New("handshake failed")
    }

    ctx, cancelFunc := context.WithCancel(context.Background())

    mutex := sync.Mutex{}
    mutex.Lock()

    link := &Link{
        ID:            id,
        Status:        ESTAB,
        lowLevel:      m.lowLevel,
        readBuf:       bytes.NewBuffer(nil),
        locker:        mutex,
        cond:          sync.NewCond(&mutex),
        semaphore:     semaphore.NewWeighted(maxBufSize),
        ctx:           ctx,
        ctxCancelFunc: cancelFunc,
    }

    go link.read()

    return link, nil
}
