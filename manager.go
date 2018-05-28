package link

import (
    "context"
    "errors"
    "math"
    "net"

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

    link := &Link{
        ID:            synPacket.ID,
        Status:        SYN,
        ctx:           ctx,
        ctxCancelFunc: cancelFunc,
        lowLevel:      m.lowLevel,
        readableChan:  make(chan *Packet, 1000),
        writtenSize:   make(chan int64, 1000),
        semaphore:     semaphore.NewWeighted(math.MaxUint16),
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

    link := &Link{
        ID:            id,
        lowLevel:      m.lowLevel,
        Status:        ESTAB,
        semaphore:     semaphore.NewWeighted(math.MaxUint16),
        writtenSize:   make(chan int64, 1000),
        readableChan:  make(chan *Packet, 1000),
        ctx:           ctx,
        ctxCancelFunc: cancelFunc,
    }

    go link.read()

    return link, nil
}
