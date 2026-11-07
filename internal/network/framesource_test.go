package network

// FrameSource seam tests (P1 inbound seam): LenPrefixSource must surface
// length-prefixed frames exactly as ReadFrame does — byte-for-byte — and
// report io.EOF only at a clean frame boundary. The session/server layer
// consumes frames through FrameSource.Next() so the transport becomes
// swappable (TCP length-prefix today, one-WS-message-per-frame in P8).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

// TestLenPrefixSourceNextReassembles: a stream carrying several frames
// written back-to-back must be split into the original frames, in order,
// byte-for-byte identical to what WriteFrame produced.
func TestLenPrefixSourceNextReassembles(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	frames := [][]byte{
		{0x4D, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0, 1, 0x0a, 0x01, 0x07}, // Hello envelope
		{0x01, 0x02, 0x03},
		bytes.Repeat([]byte{0xAB}, MaxTCPFrameSize), // exactly 64 KiB
		{}, // empty frame
	}
	writeErr := make(chan error, 1)
	go func() {
		for _, f := range frames {
			if err := WriteFrame(client, f); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	src := NewLenPrefixSource(server)
	for i, want := range frames {
		got, err := src.Next()
		if err != nil {
			t.Fatalf("Next frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d = %x, want %x", i, got, want)
		}
	}
	if werr := <-writeErr; werr != nil {
		t.Fatalf("WriteFrame: %v", werr)
	}
}

// TestLenPrefixSourceCleanEOF: an empty stream (peer closed between
// frames) must yield io.EOF, which the caller treats as a clean close.
func TestLenPrefixSourceCleanEOF(t *testing.T) {
	src := NewLenPrefixSource(bytes.NewReader(nil))
	if _, err := src.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("Next err = %v, want io.EOF", err)
	}
}

// TestLenPrefixSourceOversized: a length prefix > 64 KiB must fail with
// ErrFrameTooLarge (the caller terminates the connection).
func TestLenPrefixSourceOversized(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxTCPFrameSize+1)
	src := NewLenPrefixSource(bytes.NewReader(hdr[:]))
	if _, err := src.Next(); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("Next err = %v, want ErrFrameTooLarge", err)
	}
}

// TestLenPrefixSourceTruncated: a stream ending mid-payload must fail
// with ErrIncompleteFrame — never a partial frame.
func TestLenPrefixSourceTruncated(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100)
	stream := append(hdr[:], bytes.Repeat([]byte{0x55}, 50)...) // 50 of 100 bytes
	src := NewLenPrefixSource(bytes.NewReader(stream))
	if _, err := src.Next(); !errors.Is(err, ErrIncompleteFrame) {
		t.Errorf("Next err = %v, want ErrIncompleteFrame", err)
	}
}
