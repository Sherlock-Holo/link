package link

import (
    "encoding/binary"
    "fmt"

    "github.com/satori/go.uuid"
)

const (
    HeaderLength = 20
)

// [header 20 bytes] [payload <=65535 bytes]
type Packet struct {
    ID uuid.UUID

    // status 2 bytes
    SYN bool // 0b1000,0000,0000,0000
    ACK bool // 0b0100,0000,0000,0000
    PSH bool // 0b0010,0000,0000,0000
    FIN bool // 0b0001,0000,0000,0000
    RST bool // 0b0000,1000,0000,0000

    Length  uint16
    Payload []byte
}

func (p *Packet) Bytes() []byte {
    b := make([]byte, 0, HeaderLength)

    b = append(b, p.ID.Bytes()...)

    var status uint16

    if p.SYN {
        status |= 1 << 15
    }

    if p.ACK {
        status |= 1 << 14
    }

    if p.PSH {
        status |= 1 << 13
    }

    if p.FIN {
        status |= 1 << 12
    }

    if p.RST {
        status |= 1 << 11
    }

    statusBytes := make([]byte, 2)
    binary.BigEndian.PutUint16(statusBytes, status)
    b = append(b, statusBytes...)

    length := make([]byte, 2)
    binary.BigEndian.PutUint16(length, p.Length)
    b = append(b, length...)

    b = append(b, p.Payload...)

    return b
}

func Decode(b []byte) (*Packet, error) {
    if len(b) < HeaderLength {
        return nil, fmt.Errorf("not enough data, length %d", len(b))
    }

    p := new(Packet)

    id, err := uuid.FromBytes(b[:16])
    if err != nil {
        return nil, err
    }
    p.ID = id

    status := binary.BigEndian.Uint16(b[16:18])

    if status&(1<<15) != 0 {
        p.SYN = true
    }

    if status&(1<<14) != 0 {
        p.ACK = true
    }

    if status&(1<<13) != 0 {
        p.PSH = true
    }

    if status&(1<<12) != 0 {
        p.FIN = true
    }

    if status&(1<<11) != 0 {
        p.RST = true
    }

    p.Length = binary.BigEndian.Uint16(b[18:20])

    p.Payload = b[20:]

    return p, nil
}
