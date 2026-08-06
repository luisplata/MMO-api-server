package protocol

// Fuzz targets for the envelope codec (spec R2). The codec must never
// panic, must only return the documented sentinel errors, and must
// round-trip whatever payload it accepts.

import (
	"bytes"
	"errors"
	"testing"
)

// FuzzDecodeEnvelopeNeverPanics feeds arbitrary bytes into the envelope
// decoder. Any accepted frame must decode without panicking and must
// re-encode to the identical byte sequence (the codec is its own inverse
// on the bytes it accepts).
func FuzzDecodeEnvelopeNeverPanics(f *testing.F) {
	seeds := [][]byte{
		{},                                      // truncated (0 bytes)
		{0x4D},                                  // truncated (1 byte)
		{0x4D, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0},    // truncated (header minus last byte)
		{0x4D, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0, 1}, // minimal valid frame
		{0x00, 0x00, 0, 1, 0, 1, 0, 0, 0, 0, 1}, // bad magic
		{0x4D, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0, 1, 0x0a, 0x01, 0x07}, // valid + Hello payload
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, frame []byte) {
		env, err := DecodeEnvelope(frame)
		if err != nil {
			if !errors.Is(err, ErrBadMagic) &&
				!errors.Is(err, ErrTruncated) &&
				!errors.Is(err, ErrOversized) {
				t.Fatalf("unexpected error %v for frame %x", err, frame)
			}
			return // rejected frames must simply be dropped (S2.2/S2.3)
		}
		// Accepted frames must round-trip byte-for-byte.
		re, err := env.Encode()
		if err != nil {
			t.Fatalf("re-encode accepted frame %x: %v", frame, err)
		}
		if !bytes.Equal(re, frame) {
			t.Fatalf("round-trip mismatch: %x != %x", re, frame)
		}
	})
}

// FuzzEncodeNeverPanics feeds arbitrary Envelope values into Encode.
// Encode must never panic; on success the frame must start with the
// magic and decode back to the same header fields.
func FuzzEncodeNeverPanics(f *testing.F) {
	seeds := []Envelope{
		{},
		{Version: 1, Type: 1, Flags: 0, Seq: 0, Payload: nil},
		{Version: 1, Type: 10, Flags: FlagNeedsAck | FlagEncrypted, Seq: 42, Payload: []byte{1, 2, 3}},
	}
	for _, s := range seeds {
		f.Add(s.Version, s.Type, s.Flags, s.Seq, s.Payload)
	}
	f.Fuzz(func(t *testing.T, version, typ uint16, flags uint8, seq uint32, payload []byte) {
		env := Envelope{Version: version, Type: typ, Flags: flags, Seq: seq, Payload: payload}
		frame, err := env.Encode()
		if err != nil {
			if !errors.Is(err, ErrOversized) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		got, err := DecodeEnvelope(frame)
		if err != nil {
			t.Fatalf("decode of self-encoded frame: %v", err)
		}
		if got.Version != version || got.Type != typ || got.Flags != flags || got.Seq != seq {
			t.Fatalf("header mismatch: got %+v, want (%d,%d,%#x,%d)", got, version, typ, flags, seq)
		}
	})
}
