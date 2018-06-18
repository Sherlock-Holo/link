package link

import (
	"encoding/binary"
	"fmt"
	"strings"
)

type VersionErr struct {
	Version uint8
}

func (e VersionErr) Error() string {
	return fmt.Sprintf("packet version %d, expect %d", e.Version, Version)
}

const (
	Version      = 1
	HeaderLength = 1 + 4 + 1 + 2
)

type PacketHeader []byte

func (h PacketHeader) Version() uint8 {
	return h[0]
}

func (h PacketHeader) ID() uint32 {
	return binary.BigEndian.Uint32(h[1:5])
}

func (h PacketHeader) PayloadLength() int {
	return int(binary.BigEndian.Uint16(h[6:]))
}

// [header 1 + 4 + 1 + 2 bytes] [payload <=65535 bytes]
type Packet struct {
	Version uint8

	ID uint32

	// status 1 bytes
	PSH  bool // 0b1000,0000
	FIN  bool // 0b0100,0000
	PING bool // 0b0010,0000
	// RSV       0b0000,0000

	Length  uint16
	Payload []byte
}

func newPacket(id uint32, status string, payload []byte) *Packet {
	packet := Packet{
		Version: Version,
		ID:      id,
	}

	switch strings.ToUpper(status) {
	case "PSH":
		packet.PSH = true

	case "FIN":
		packet.FIN = true

	case "PING":
		packet.PING = true

	default:
		panic("not allow status " + status)
	}

	if payload != nil {
		packet.Payload = payload
		packet.Length = uint16(len(payload))
	}

	return &packet
}

func split(id uint32, p []byte) []*Packet {
	if len(p) <= 65536 {
		return []*Packet{newPacket(id, "PSH", p)}
	}

	var ps []*Packet

	for len(p) > 65535 {
		ps = append(ps, newPacket(id, "PSH", p))
		p = p[65535:]
	}
	ps = append(ps, newPacket(id, "PSH", p)) // append last data which size <= 65535
	return ps
}

func (p *Packet) Bytes() []byte {
	b := make([]byte, 1+4)
	b[0] = p.Version
	binary.BigEndian.PutUint32(b[1:], p.ID)

	var status uint8

	if p.PSH {
		status |= 1 << 7
	}

	if p.FIN {
		status |= 1 << 6
	}

	if p.PING {
		status |= 1 << 5
	}

	b = append(b, status)

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

	p.Version = b[0]

	p.ID = binary.BigEndian.Uint32(b[1:5])

	status := b[5]

	if status&(1<<7) != 0 {
		p.PSH = true
	}

	if status&(1<<6) != 0 {
		p.FIN = true
	}

	if status&(1<<5) != 0 {
		p.PING = true
	}

	p.Length = binary.BigEndian.Uint16(b[6:8])

	if p.Length != 0 {
		p.Payload = b[8:]
	}

	return p, nil
}
