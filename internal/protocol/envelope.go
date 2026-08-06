// Package protocol envelope codec (design D1, spec R2).
//
// Wire layout (big-endian):
//
//	[magic u16=0x4D4D][ver u16][type u16][flags u8][seq u32] + payload
//
// The explicit field widths in the locked D1 decision are u16+u16+u16+
// u8+u32, which sum to an 11-byte header — the "10-byte" label used in
// spec/design prose is a miscount; the explicit layout is authoritative
// and this codec follows it exactly.
//
// Flags (v1): bit0 needs-ack, bit1 compressed, bit2 encrypted (reserved).
package protocol

import (
	"encoding/binary"
	"errors"
)

const (
	// Magic identifies the envelope header (0x4D4D, ASCII "MM").
	Magic uint16 = 0x4D4D

	// HeaderSize is the fixed envelope header length: magic(2) + ver(2) +
	// type(2) + flags(1) + seq(4) = 11 bytes.
	HeaderSize = 2 + 2 + 2 + 1 + 4

	// MaxFrameSize caps a whole envelope frame (header + payload) at
	// 64 KiB — the same bound the TCP framing layer enforces. A larger
	// envelope can never be valid on any v1 channel.
	MaxFrameSize = 64 * 1024
)

// Envelope flag bits (design D1). Bit2 (encrypted) is reserved in v1.
const (
	FlagNeedsAck   uint8 = 1 << 0
	FlagCompressed uint8 = 1 << 1
	FlagEncrypted  uint8 = 1 << 2
)

// Errors returned by the envelope codec.
var (
	ErrBadMagic  = errors.New("protocol: bad envelope magic")
	ErrTruncated = errors.New("protocol: truncated envelope header")
	ErrOversized = errors.New("protocol: envelope frame exceeds max size")
)

// Envelope is the fixed header + opaque protobuf payload carried by one
// frame. Type is the registry type id that selects the message schema.
type Envelope struct {
	Version uint16
	Type    uint16
	Flags   uint8
	Seq     uint32
	Payload []byte
}

// Encode serializes the envelope to its big-endian wire form:
// header + payload. It fails with ErrOversized if the total frame would
// exceed MaxFrameSize.
func (e Envelope) Encode() ([]byte, error) {
	if len(e.Payload) > MaxFrameSize-HeaderSize {
		return nil, ErrOversized
	}
	frame := make([]byte, HeaderSize+len(e.Payload))
	binary.BigEndian.PutUint16(frame[0:], Magic)
	binary.BigEndian.PutUint16(frame[2:], e.Version)
	binary.BigEndian.PutUint16(frame[4:], e.Type)
	frame[6] = e.Flags
	binary.BigEndian.PutUint32(frame[7:], e.Seq)
	copy(frame[HeaderSize:], e.Payload)
	return frame, nil
}

// DecodeEnvelope parses a raw frame into an Envelope. It validates the
// magic (ErrBadMagic), the header length (ErrTruncated) and the total
// frame size (ErrOversized). Type registration is NOT checked here —
// dispatch against the registry is the registry's job (spec S2.3).
func DecodeEnvelope(frame []byte) (*Envelope, error) {
	if len(frame) < HeaderSize {
		return nil, ErrTruncated
	}
	if len(frame) > MaxFrameSize {
		return nil, ErrOversized
	}
	if binary.BigEndian.Uint16(frame[0:]) != Magic {
		return nil, ErrBadMagic
	}
	return &Envelope{
		Version: binary.BigEndian.Uint16(frame[2:]),
		Type:    binary.BigEndian.Uint16(frame[4:]),
		Flags:   frame[6],
		Seq:     binary.BigEndian.Uint32(frame[7:]),
		Payload: frame[HeaderSize:],
	}, nil
}
