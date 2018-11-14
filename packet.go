package link

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	Version                    = 3
	HeaderWithoutPayloadLength = 1 + 4 + 1

	PSH   = 128
	CLOSE = 64
	PING  = 32
	ACK   = 16 // 2 bytes data, uint16, the other side has read [uint16] bytes data
)

// [header 1 + 4 + 1 + indefinite length bytes] [payload]
type Packet struct {
	Version uint8

	ID uint32

	// CMD 1 byte
	// PSH    0b1000,0000
	// CLOSE  0b0100,0000
	// PING   0b0010,0000
	// ACK    0b0001,0000
	// RSV    0b0000,0000
	CMD uint8

	// When len(payload) < 254, use shortPayloadLength.
	// When 254 <= len(payload) <= 65535, use middlePayloadLength.
	// Otherwise use longPayloadLength.
	shortPayloadLength  uint8
	middlePayloadLength uint16
	longPayloadLength   uint32
	PayloadLength       int
	Payload             []byte
}

// newPacket create a new packet.
// if cmd is FIN, RST, payload is nil.
func newPacket(id uint32, cmd uint8, payload []byte) *Packet {
	packet := Packet{
		Version: Version,
		ID:      id,
	}

	switch cmd {
	case PSH:
		packet.CMD = PSH

	case CLOSE:
		packet.CMD = CLOSE

	case PING:
		packet.CMD = PING

	case ACK:
		packet.CMD = ACK

	default:
		panic(fmt.Sprintf("not allowed cmd code %d", cmd))
	}

	if payload != nil {
		packet.Payload = payload
		packet.PayloadLength = len(payload)

		switch {
		case packet.PayloadLength < 254:
			packet.shortPayloadLength = uint8(packet.PayloadLength)

		case 254 <= packet.PayloadLength && packet.PayloadLength <= 65535:
			packet.shortPayloadLength = 254
			packet.middlePayloadLength = uint16(packet.PayloadLength)

		default:
			packet.shortPayloadLength = 255
			packet.longPayloadLength = uint32(packet.PayloadLength)
		}
	}

	return &packet
}

// bytes encode packet to []byte.
func (p *Packet) bytes() []byte {
	var b []byte
	switch {
	case p.PayloadLength < 254:
		b = make([]byte, HeaderWithoutPayloadLength+1, HeaderWithoutPayloadLength+1+p.PayloadLength)

	case 254 <= p.PayloadLength && p.PayloadLength <= 65535:
		b = make([]byte, HeaderWithoutPayloadLength+2, HeaderWithoutPayloadLength+2+p.PayloadLength)

	default:
		b = make([]byte, HeaderWithoutPayloadLength+4, HeaderWithoutPayloadLength+4+p.PayloadLength)
	}

	b[0] = p.Version
	binary.BigEndian.PutUint32(b[1:5], p.ID)

	var cmdByte uint8

	switch p.CMD {
	case PSH:
		cmdByte |= 1 << 7

	case CLOSE:
		cmdByte |= 1 << 6

	case PING:
		cmdByte |= 1 << 5

	case ACK:
		cmdByte |= 1 << 4
	}

	b[5] = cmdByte
	b[6] = p.shortPayloadLength

	switch {
	case p.shortPayloadLength < 254:

	case p.shortPayloadLength == 254:
		binary.BigEndian.PutUint16(b[7:], p.middlePayloadLength)

	default:
		binary.BigEndian.PutUint32(b[7:], p.longPayloadLength)
	}

	if p.Payload != nil {
		b = append(b, p.Payload...)
	}

	return b
}

// decode decode a packet from []byte.
func decodeFrom(r io.Reader) (*Packet, error) {
	b := make([]byte, HeaderWithoutPayloadLength+1)

	if _, err := io.ReadFull(r, b); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, ErrLinkClosed
		}
		return nil, err
	}

	p := new(Packet)

	p.Version = b[0]

	if p.Version != Version {
		return nil, ErrVersion{
			Receive:     p.Version,
			NeedVersion: Version,
		}
	}

	p.ID = binary.BigEndian.Uint32(b[1:5])

	cmdByte := b[5]

	if cmdByte&(1<<7) != 0 {
		p.CMD = PSH
	}

	if cmdByte&(1<<6) != 0 {
		p.CMD = CLOSE
	}

	if cmdByte&(1<<5) != 0 {
		p.CMD = PING
	}

	if cmdByte&(1<<4) != 0 {
		p.CMD = ACK
	}

	p.shortPayloadLength = b[6]

	switch {
	case p.shortPayloadLength < 254:
		p.PayloadLength = int(p.shortPayloadLength)
		p.Payload = make([]byte, p.PayloadLength)

	case p.shortPayloadLength == 254:
		b = b[:2]

		if _, err := io.ReadFull(r, b); err != nil {
			if err == io.ErrUnexpectedEOF {
				return nil, ErrLinkClosed
			}
			return nil, err
		}

		p.middlePayloadLength = binary.BigEndian.Uint16(b)
		p.PayloadLength = int(p.middlePayloadLength)
		p.Payload = make([]byte, p.PayloadLength)

	default:
		b = b[:4]

		if _, err := io.ReadFull(r, b); err != nil {
			if err == io.ErrUnexpectedEOF {
				return nil, ErrLinkClosed
			}
			return nil, err
		}

		p.longPayloadLength = binary.BigEndian.Uint32(b)
		p.PayloadLength = int(p.longPayloadLength)
		p.Payload = make([]byte, p.PayloadLength)
	}

	if _, err := io.ReadFull(r, p.Payload); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, ErrLinkClosed
		}
		return nil, err
	}

	return p, nil
}
