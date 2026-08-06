package network

// TCP framing tests (spec R3). Frames are [4-byte BE uint32 length
// prefix][payload] with payload ≤ 64 KiB and stream reassembly by length
// prefix.
//
// Coverage: S3.1 round-trip, S3.2 oversized -> ErrFrameTooLarge (caller
// terminates the connection), S3.3 truncated stream -> ErrIncompleteFrame
// (never a corrupt frame).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
)

// TestWriteReadFrameRoundTrip covers S3.1: a payload ≤ 64 KiB written to
// a stream is reassembled exactly. Uses an in-memory pipe so the read
// side is a real blocking stream.
func TestWriteReadFrameRoundTrip(t *testing.T) {
	payloads := map[string][]byte{
		"empty":         {},
		"single-byte":   {0x00},
		"hello-payload": {0x4D, 0x4D, 0, 1, 0, 1, 0, 0, 0, 0, 1, 0x0a, 0x01, 0x07},
		"exactly-64KiB": bytes.Repeat([]byte{0xAB}, MaxTCPFrameSize),
		"near-64KiB":    bytes.Repeat([]byte{0x01}, MaxTCPFrameSize-1),
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			writeErr := make(chan error, 1)
			go func() {
				writeErr <- WriteFrame(client, payload)
			}()

			got, err := ReadFrame(server)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if werr := <-writeErr; werr != nil {
				t.Fatalf("WriteFrame: %v", werr)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("reassembled %x, want %x", got, payload)
			}
		})
	}
}

// TestReadFrameMultiFrameReassembly: several frames written back-to-back
// on one stream must be split back into the original frames, in order.
func TestReadFrameMultiFrameReassembly(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	frames := [][]byte{
		{1},
		bytes.Repeat([]byte{2}, 100),
		bytes.Repeat([]byte{3}, MaxTCPFrameSize),
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

	for i, want := range frames {
		got, err := ReadFrame(server)
		if err != nil {
			t.Fatalf("ReadFrame frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d reassembled %x, want %x", i, got, want)
		}
	}
	if werr := <-writeErr; werr != nil {
		t.Fatalf("WriteFrame: %v", werr)
	}
}

// TestReadFrameOversizedLength covers S3.2: a length prefix > 64 KiB must
// fail with ErrFrameTooLarge so the caller terminates the connection.
func TestReadFrameOversizedLength(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxTCPFrameSize+1)
	stream := bytes.NewReader(hdr[:])

	if _, err := ReadFrame(stream); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("err = %v, want ErrFrameTooLarge", err)
	}
}

// TestWriteFrameOversized covers S3.2 from the write side: writing a
// payload > 64 KiB must fail with ErrFrameTooLarge and write nothing.
func TestWriteFrameOversized(t *testing.T) {
	var buf bytes.Buffer
	err := WriteFrame(&buf, bytes.Repeat([]byte{0x01}, MaxTCPFrameSize+1))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("err = %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("oversized write left %d bytes, want 0", buf.Len())
	}
}

// TestReadFrameTruncatedHeader covers S3.3: a stream ending mid-header
// raises ErrIncompleteFrame — never a corrupt frame.
func TestReadFrameTruncatedHeader(t *testing.T) {
	for n := 1; n < 4; n++ {
		t.Run("header-truncated-to-"+strconv.Itoa(n), func(t *testing.T) {
			stream := bytes.NewReader(bytes.Repeat([]byte{0x00}, n))
			if _, err := ReadFrame(stream); !errors.Is(err, ErrIncompleteFrame) {
				t.Errorf("err = %v, want ErrIncompleteFrame", err)
			}
		})
	}
}

// TestReadFrameTruncatedPayload covers S3.3: a stream ending mid-payload
// raises ErrIncompleteFrame.
func TestReadFrameTruncatedPayload(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100)
	stream := bytes.NewReader(append(hdr[:], bytes.Repeat([]byte{0x55}, 50)...)) // 50 of 100 bytes

	if _, err := ReadFrame(stream); !errors.Is(err, ErrIncompleteFrame) {
		t.Errorf("err = %v, want ErrIncompleteFrame", err)
	}
}

// TestReadFrameCleanEOF: an empty stream (peer closed between frames)
// must return io.EOF, which the caller treats as a clean close — not an
// incomplete frame.
func TestReadFrameCleanEOF(t *testing.T) {
	if _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}
