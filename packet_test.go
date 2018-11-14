package link

import (
	"io"
	"io/ioutil"
	"os"
	"reflect"
	"testing"
)

func Test_newPacket(t *testing.T) {
	type args struct {
		id      uint32
		cmd     uint8
		payload []byte
	}
	var tests []struct {
		name string
		args args
		want *Packet
	}

	testdataMap := map[string]struct {
		short  int
		middle int
		long   int
		length int
	}{
		"less-than-254":                 {short: 3, middle: 0, long: 0, length: 3},
		"254-length":                    {short: 254, middle: 254, long: 0, length: 254},
		"more-than-254-less-than-65535": {short: 254, middle: 17483, long: 0, length: 17483},
		"65535-length":                  {short: 254, middle: 65535, long: 0, length: 65535},
		"more-than-65535":               {short: 255, middle: 0, long: 137548, length: 137548},
	}

	for testdataFile, info := range testdataMap {
		b, err := ioutil.ReadFile("testdata/" + testdataFile)
		if err != nil {
			t.Fatal(err)
		}
		tests = append(tests, []struct {
			name string
			args args
			want *Packet
		}{
			{
				name: "PSH " + testdataFile,
				args: struct {
					id      uint32
					cmd     uint8
					payload []byte
				}{
					id:      PSH,
					cmd:     PSH,
					payload: b,
				},
				want: &Packet{
					Version:             Version,
					ID:                  PSH,
					CMD:                 PSH,
					shortPayloadLength:  uint8(info.short),
					middlePayloadLength: uint16(info.middle),
					longPayloadLength:   uint32(info.long),
					PayloadLength:       info.length,
					Payload:             b,
				},
			},

			{
				name: "ACK " + testdataFile,
				args: struct {
					id      uint32
					cmd     uint8
					payload []byte
				}{
					id:      ACK,
					cmd:     ACK,
					payload: b,
				},
				want: &Packet{
					Version:             Version,
					ID:                  ACK,
					CMD:                 ACK,
					shortPayloadLength:  uint8(info.short),
					middlePayloadLength: uint16(info.middle),
					longPayloadLength:   uint32(info.long),
					PayloadLength:       info.length,
					Payload:             b,
				},
			},

			{
				name: "CLOSE " + testdataFile,
				args: struct {
					id      uint32
					cmd     uint8
					payload []byte
				}{
					id:      CLOSE,
					cmd:     CLOSE,
					payload: b,
				},
				want: &Packet{
					Version:             Version,
					ID:                  CLOSE,
					CMD:                 CLOSE,
					shortPayloadLength:  uint8(info.short),
					middlePayloadLength: uint16(info.middle),
					longPayloadLength:   uint32(info.long),
					PayloadLength:       info.length,
					Payload:             b,
				},
			},

			{
				name: "PING " + testdataFile,
				args: struct {
					id      uint32
					cmd     uint8
					payload []byte
				}{
					id:      PING,
					cmd:     PING,
					payload: b,
				},
				want: &Packet{
					Version:             Version,
					ID:                  PING,
					CMD:                 PING,
					shortPayloadLength:  uint8(info.short),
					middlePayloadLength: uint16(info.middle),
					longPayloadLength:   uint32(info.long),
					PayloadLength:       info.length,
					Payload:             b,
				},
			},
		}...,
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newPacket(tt.args.id, tt.args.cmd, tt.args.payload); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newPacket() = %v,\n want %v", got, tt.want)
			}
		})
	}
}

func Test_decodeFrom(t *testing.T) {
	type args struct {
		r io.Reader
	}
	var tests []struct {
		name    string
		args    args
		want    *Packet
		wantErr bool
	}

	packetBinaryLess254, err := os.Open("testdata/binary/packet-binary-less-254")
	if err != nil {
		t.Fatal(err)
	}

	tests = append(tests, struct {
		name    string
		args    args
		want    *Packet
		wantErr bool
	}{
		name: "packet-binary-less-254",
		args: struct{ r io.Reader }{r: packetBinaryLess254},
		want: &Packet{
			Version:            Version,
			ID:                 128,
			CMD:                PSH,
			shortPayloadLength: 1,
			PayloadLength:      1,
			Payload:            []byte{1},
		},
		wantErr: false,
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeFrom(tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("decodeFrom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeFrom() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPacket_bytes(t *testing.T) {
	b, err := ioutil.ReadFile("testdata/binary/packet-binary-less-254")
	if err != nil {
		t.Fatal(err)
	}

	type fields struct {
		Version             uint8
		ID                  uint32
		CMD                 uint8
		shortPayloadLength  uint8
		middlePayloadLength uint16
		longPayloadLength   uint32
		PayloadLength       int
		Payload             []byte
	}
	tests := []struct {
		name   string
		fields fields
		want   []byte
	}{
		{
			name: "less-254",
			fields: struct {
				Version             uint8
				ID                  uint32
				CMD                 uint8
				shortPayloadLength  uint8
				middlePayloadLength uint16
				longPayloadLength   uint32
				PayloadLength       int
				Payload             []byte
			}{
				Version:            Version,
				ID:                 128,
				CMD:                PSH,
				shortPayloadLength: 1,
				PayloadLength:      1,
				Payload:            []byte{1},
			},
			want: b,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Packet{
				Version:             tt.fields.Version,
				ID:                  tt.fields.ID,
				CMD:                 tt.fields.CMD,
				shortPayloadLength:  tt.fields.shortPayloadLength,
				middlePayloadLength: tt.fields.middlePayloadLength,
				longPayloadLength:   tt.fields.longPayloadLength,
				PayloadLength:       tt.fields.PayloadLength,
				Payload:             tt.fields.Payload,
			}
			if got := p.bytes(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Packet.bytes() = %v, want %v", got, tt.want)
			}
		})
	}
}
