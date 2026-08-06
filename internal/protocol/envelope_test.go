package protocol

// Envelope codec tests (spec R2). The header layout is the design D1
// locked decision: big-endian [magic u16=0x4D4D][ver u16][type u16]
// [flags u8][seq u32] + protobuf payload. NOTE: the field sizes given in
// the spec/design prose (u16+u16+u16+u8+u32) sum to 11 bytes, not the
// "10-byte" label used in the prose — the explicit field layout wins.
//
// Coverage: S2.1 round-trip, S2.2 bad magic, S2.3 unknown type (via the
// registry), plus truncated/oversized structural rejections.

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
)

// TestEnvelopeHeaderLayout pins the exact big-endian wire bytes of the
// header, so the server and the Unity client agree on the byte layout.
func TestEnvelopeHeaderLayout(t *testing.T) {
	env := Envelope{
		Version: 0x0102,
		Type:    0x0304,
		Flags:   0xAB,
		Seq:     0x11223344,
		Payload: []byte{0x99},
	}
	frame, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := []byte{
		0x4D, 0x4D, // magic 0x4D4D
		0x01, 0x02, // ver 0x0102
		0x03, 0x04, // type 0x0304
		0xAB,                   // flags
		0x11, 0x22, 0x33, 0x44, // seq 0x11223344
		0x99, // payload
	}
	if !bytes.Equal(frame, want) {
		t.Errorf("frame bytes = %x, want %x", frame, want)
	}
	if len(frame) != HeaderSize+1 {
		t.Errorf("frame length = %d, want %d", len(frame), HeaderSize+1)
	}
}

// TestEnvelopeRoundTrip covers S2.1: known header values and payload
// must round-trip exactly through Encode -> DecodeEnvelope.
func TestEnvelopeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		env  Envelope
	}{
		{
			name: "minimal-empty-payload",
			env:  Envelope{Version: 1, Type: 1, Flags: 0, Seq: 0, Payload: nil},
		},
		{
			name: "all-flag-bits-high",
			env:  Envelope{Version: 1, Type: 10, Flags: FlagNeedsAck | FlagCompressed | FlagEncrypted, Seq: 0xDEADBEEF, Payload: []byte{0x0a, 0x05, 'h', 'e', 'l', 'l', 'o'}},
		},
		{
			name: "max-seq-and-1200-byte-payload",
			env:  Envelope{Version: 2, Type: 7, Flags: FlagEncrypted, Seq: 0xFFFFFFFF, Payload: bytes.Repeat([]byte{0xFF}, 1200)},
		},
		{
			name: "nonzero-seq-all-flags-off",
			env:  Envelope{Version: 1, Type: 5, Flags: 0, Seq: 42, Payload: []byte{0x00}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := tc.env.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := DecodeEnvelope(frame)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if got.Version != tc.env.Version {
				t.Errorf("Version = %d, want %d", got.Version, tc.env.Version)
			}
			if got.Type != tc.env.Type {
				t.Errorf("Type = %d, want %d", got.Type, tc.env.Type)
			}
			if got.Flags != tc.env.Flags {
				t.Errorf("Flags = %#x, want %#x", got.Flags, tc.env.Flags)
			}
			if got.Seq != tc.env.Seq {
				t.Errorf("Seq = %d, want %d", got.Seq, tc.env.Seq)
			}
			if !bytes.Equal(got.Payload, tc.env.Payload) {
				t.Errorf("Payload = %x, want %x", got.Payload, tc.env.Payload)
			}
		})
	}
}

// TestDecodeEnvelopeBadMagic covers S2.2: any frame whose magic is not
// 0x4D4D must fail with ErrBadMagic so the frame is dropped.
func TestDecodeEnvelopeBadMagic(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
	}{
		{"first-byte-wrong", []byte{0x4D, 0x00, 0, 1, 0, 1, 0, 0, 0, 0, 0}},
		{"second-byte-wrong", []byte{0x00, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0, 0}},
		{"both-bytes-wrong", []byte{0x00, 0x00, 0, 1, 0, 1, 0, 0, 0, 0, 0}},
		{"zero-frame", []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeEnvelope(tc.frame)
			if !errors.Is(err, ErrBadMagic) {
				t.Errorf("err = %v, want ErrBadMagic", err)
			}
		})
	}
}

// TestDecodeEnvelopeTruncated: any frame shorter than the 11-byte header
// must fail with ErrTruncated — never a partial parse.
func TestDecodeEnvelopeTruncated(t *testing.T) {
	full := Envelope{Version: 1, Type: 1, Flags: 0, Seq: 1, Payload: []byte{0x01}}.mustEncode(t)
	for n := 0; n < HeaderSize; n++ {
		t.Run("truncated-to-"+strconv.Itoa(n), func(t *testing.T) {
			_, err := DecodeEnvelope(full[:n])
			if !errors.Is(err, ErrTruncated) {
				t.Errorf("err = %v, want ErrTruncated", err)
			}
		})
	}
}

// TestDecodeEnvelopeOversized: a frame whose total size exceeds
// MaxFrameSize must fail with ErrOversized — the envelope-level cap that
// matches the TCP 64 KiB limit. The frame is hand-crafted because Encode
// itself refuses to build an oversized frame.
func TestDecodeEnvelopeOversized(t *testing.T) {
	big := make([]byte, MaxFrameSize+1)
	copy(big, []byte{0x4D, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0, 1})
	if len(big) <= MaxFrameSize {
		t.Fatalf("test frame (%d bytes) must exceed MaxFrameSize=%d", len(big), MaxFrameSize)
	}
	_, err := DecodeEnvelope(big)
	if !errors.Is(err, ErrOversized) {
		t.Errorf("err = %v, want ErrOversized", err)
	}
}

// TestDecodeEnvelopeOversizedEncode: encoding a payload that would push
// the frame over MaxFrameSize must also fail with ErrOversized.
func TestDecodeEnvelopeOversizedEncode(t *testing.T) {
	env := Envelope{Version: 1, Type: 1, Flags: 0, Seq: 1, Payload: bytes.Repeat([]byte{0x01}, MaxFrameSize)}
	if _, err := env.Encode(); !errors.Is(err, ErrOversized) {
		t.Errorf("err = %v, want ErrOversized", err)
	}
}

// TestDecodeEnvelopeUnknownType covers S2.3: a structurally valid frame
// carrying an unregistered type id fails with ErrUnknownType at the
// registry dispatch step.
func TestDecodeEnvelopeUnknownType(t *testing.T) {
	env := Envelope{Version: 1, Type: 99, Flags: 0, Seq: 1, Payload: nil}
	frame, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatalf("DecodeEnvelope(structural) = %v, want nil", err)
	}
	if _, err := NewWorldRegistry().DecodeMessage(*decoded); !errors.Is(err, ErrUnknownType) {
		t.Errorf("registry.Decode err = %v, want ErrUnknownType", err)
	}
}

// mustEncode is a helper for tests that construct frames they KNOW are
// valid; it fails the test on encode errors.
func (e Envelope) mustEncode(t *testing.T) []byte {
	t.Helper()
	frame, err := e.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return frame
}
