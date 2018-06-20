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
	Version      = 2
	HeaderLength = 1 + 4 + 1 + 2

	PSH  = 128
	FIN  = 64
	PING = 32
	ACK  = 16 // 2 bytes data, uint16, the other size has read [uint16] bytes data
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
	// PSH  0b1000,0000
	// FIN  0b0100,0000
	// PING 0b0010,0000
	// ACK  0b0001,0000
	// RSV  0b0000,0000
	CMD uint8

	PayloadLength uint16
	Payload       []byte
}

func newPacket(id uint32, status string, payload []byte) *Packet {
	packet := Packet{
		Version: Version,
		ID:      id,
	}

	switch strings.ToUpper(status) {
	case "PSH":
		packet.CMD = PSH

	case "FIN":
		packet.CMD = FIN

	case "PING":
		packet.CMD = PING

	case "ACK":
		packet.CMD = ACK

	default:
		panic("not allow status " + status)
	}

	if payload != nil {
		packet.Payload = payload
		packet.PayloadLength = uint16(len(payload))
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

	var cmdByte uint8

	switch p.CMD {
	case PSH:
		cmdByte |= 1 << 7

	case FIN:
		cmdByte |= 1 << 6

	case PING:
		cmdByte |= 1 << 5

	case ACK:
		cmdByte |= 1 << 4
	}

	b = append(b, cmdByte)

	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, p.PayloadLength)
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

	cmdByte := b[5]

	if cmdByte&(1<<7) != 0 {
		p.CMD = PSH
	}

	if cmdByte&(1<<6) != 0 {
		p.CMD = FIN
	}

	if cmdByte&(1<<5) != 0 {
		p.CMD = PING
	}

	if cmdByte&(1<<4) != 0 {
		p.CMD = ACK
	}

	p.PayloadLength = binary.BigEndian.Uint16(b[6:8])

	if p.PayloadLength != 0 {
		p.Payload = b[8:]
	}

	return p, nil
}
