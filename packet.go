package link

import (
    "encoding/binary"
    "fmt"

    "strings"
)

const (
    HeaderLength = 4 + 2 + 2
)

type PacketHeader []byte

func (h PacketHeader) ID() uint32 {
    return binary.BigEndian.Uint32(h[:4])
}

func (h PacketHeader) PayloadLength() int {
    return int(binary.BigEndian.Uint16(h[6:]))
}

// [header 20 bytes] [payload <=65535 bytes]
type Packet struct {
    ID uint32

    // status 2 bytes
    SYN bool // 0b1000,0000,0000,0000
    ACK bool // 0b0100,0000,0000,0000
    PSH bool // 0b0010,0000,0000,0000
    FIN bool // 0b0001,0000,0000,0000
    RST bool // 0b0000,1000,0000,0000

    Length  uint16
    Payload []byte
}

func newPacket(id uint32, status string, payload []byte) *Packet {
    packet := Packet{
        ID: id,
    }

    switch strings.ToUpper(status) {
    case "SYN":
        packet.SYN = true

    case "ACK":
        packet.ACK = true

    case "PSH":
        packet.PSH = true

    case "FIN":
        packet.FIN = true

    case "RST":
        packet.RST = true

    default:
        panic("not allow status " + status)
    }

    if payload != nil {
        packet.Payload = payload
        packet.Length = uint16(len(payload))
    }

    return &packet
}

func (p *Packet) Bytes() []byte {
    /*b := make([]byte, 0, HeaderLength)

    b = append(b, p.ID.Bytes()...)*/
    b := make([]byte, 4)
    binary.BigEndian.PutUint32(b, p.ID)

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

    p.ID = binary.BigEndian.Uint32(b[:4])

    status := binary.BigEndian.Uint16(b[4:6])

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

    p.Length = binary.BigEndian.Uint16(b[6:8])

    if p.Length != 0 {
        p.Payload = b[8:]
    }

    return p, nil
}
