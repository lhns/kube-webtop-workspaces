// Package wayland is a minimal Wayland wire client, just enough to watch input
// idleness through ext-idle-notify-v1 (no cgo, no external library).
package wayland

import (
	"encoding/binary"
	"errors"
	"io"
)

// Message is one wire message: a request or event on an object.
type Message struct {
	Object uint32
	Opcode uint16
	Args   []byte
}

// Encoder builds message arguments.
type Encoder struct{ b []byte }

func (e *Encoder) Uint(v uint32) *Encoder {
	e.b = binary.LittleEndian.AppendUint32(e.b, v)
	return e
}

// String writes length (incl. NUL), the bytes, a NUL, and padding to 4.
func (e *Encoder) String(s string) *Encoder {
	e.Uint(uint32(len(s) + 1))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
	for len(e.b)%4 != 0 {
		e.b = append(e.b, 0)
	}
	return e
}

func (e *Encoder) Bytes() []byte { return e.b }

// Marshal returns the message with its 8-byte header (object, size<<16|opcode).
func (m Message) Marshal() []byte {
	out := make([]byte, 0, 8+len(m.Args))
	out = binary.LittleEndian.AppendUint32(out, m.Object)
	out = binary.LittleEndian.AppendUint32(out, uint32(8+len(m.Args))<<16|uint32(m.Opcode))
	return append(out, m.Args...)
}

// ReadMessage reads one message.
func ReadMessage(r io.Reader) (Message, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Message{}, err
	}
	w := binary.LittleEndian.Uint32(h[4:])
	size := int(w >> 16)
	if size < 8 || size%4 != 0 {
		return Message{}, errors.New("wayland: bad message size")
	}
	m := Message{Object: binary.LittleEndian.Uint32(h[:4]), Opcode: uint16(w), Args: make([]byte, size-8)}
	if _, err := io.ReadFull(r, m.Args); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Decoder reads message arguments; the first malformed read sets Err.
type Decoder struct {
	b   []byte
	Err error
}

func NewDecoder(b []byte) *Decoder { return &Decoder{b: b} }

func (d *Decoder) Uint() uint32 {
	if d.Err != nil || len(d.b) < 4 {
		d.Err = errors.New("wayland: short argument")
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b)
	d.b = d.b[4:]
	return v
}

func (d *Decoder) String() string {
	n := int(d.Uint())
	padded := (n + 3) &^ 3
	if d.Err != nil || n < 1 || len(d.b) < padded {
		d.Err = errors.New("wayland: bad string")
		return ""
	}
	s := string(d.b[:n-1])
	d.b = d.b[padded:]
	return s
}
