package network

// Fuzz targets for the framing codecs (spec R3/R4). Framing must never
// panic, must only return the documented sentinel errors, and must
// round-trip whatever payload it accepts.

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// FuzzReadFrameNeverPanics feeds arbitrary byte streams into the TCP
// frame reader. Every accepted frame must round-trip through WriteFrame.
func FuzzReadFrameNeverPanics(f *testing.F) {
	seeds := [][]byte{
		{},                                // clean EOF
		{0x00},                            // truncated header
		{0x00, 0x00},                      // truncated header
		{0x00, 0x00, 0x00, 0x01, 0xAB},    // 1-byte frame
		{0x00, 0x01, 0x00, 0x00, 1, 2, 3}, // 65536-length prefix (valid bound)
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, stream []byte) {
		payload, err := ReadFrame(bytes.NewReader(stream))
		if err != nil {
			if !errors.Is(err, ErrFrameTooLarge) &&
				!errors.Is(err, ErrIncompleteFrame) &&
				!errors.Is(err, io.EOF) {
				t.Fatalf("unexpected error %v for stream %x", err, stream)
			}
			return
		}
		// Re-encoding the payload must reproduce the exact bytes ReadFrame
		// consumed: the header (4 B) + payload. The stream may carry more
		// frames after the first, so compare the consumed prefix.
		var buf bytes.Buffer
		if err := WriteFrame(&buf, payload); err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		consumed := 4 + len(payload)
		if !bytes.Equal(buf.Bytes(), stream[:consumed]) {
			t.Fatalf("frame round-trip mismatch: %x != %x", buf.Bytes(), stream[:consumed])
		}
	})
}

// FuzzWriteReadFrameNeverPanics feeds arbitrary payloads through
// WriteFrame then ReadFrame over an in-memory stream.
func FuzzWriteReadFrameNeverPanics(f *testing.F) {
	seeds := [][]byte{
		nil,
		{0x00},
		bytes.Repeat([]byte{0x4D}, 100),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, payload); err != nil {
			if !errors.Is(err, ErrFrameTooLarge) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read self-written frame: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round-trip mismatch: %d bytes, want %d", len(got), len(payload))
		}
	})
}

// FuzzValidateUDPFrameNeverPanics feeds arbitrary frame sizes through the
// UDP validator — it must only ever accept ≤ 1200 B.
func FuzzValidateUDPFrameNeverPanics(f *testing.F) {
	seeds := []int{0, 1, 1199, 1200, 1201, 1500}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, size int) {
		if size < 0 {
			return
		}
		frame := make([]byte, size)
		err := ValidateUDPFrame(frame)
		if size <= MaxUDPFrameSize && err != nil {
			t.Fatalf("ValidateUDPFrame(%d) = %v, want nil", size, err)
		}
		if size > MaxUDPFrameSize && !errors.Is(err, ErrDatagramTooLarge) {
			t.Fatalf("ValidateUDPFrame(%d) = %v, want ErrDatagramTooLarge", size, err)
		}
	})
}
