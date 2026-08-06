package network

// TCP framing (spec R3). A frame is [4-byte BE uint32 length][payload]
// with payload ≤ 64 KiB; the reader reassembles the stream by length
// prefix.

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	// MaxTCPFrameSize caps a TCP frame payload at 64 KiB (spec R3).
	MaxTCPFrameSize = 64 * 1024

	// tcpLengthPrefixSize is the size of the BE uint32 length prefix.
	tcpLengthPrefixSize = 4
)

// Errors returned by the TCP framing layer.
var (
	// ErrFrameTooLarge is returned when a length prefix or payload
	// exceeds MaxTCPFrameSize. The caller MUST terminate the connection
	// (spec S3.2) — the stream cannot be safely resynchronized.
	ErrFrameTooLarge = errors.New("network: tcp frame exceeds max size")

	// ErrIncompleteFrame is returned when the stream ends mid-header or
	// mid-payload (spec S3.3). A partial frame is never surfaced.
	ErrIncompleteFrame = errors.New("network: incomplete tcp frame")
)

// WriteFrame writes payload to w as a length-prefixed TCP frame. It
// fails with ErrFrameTooLarge (writing nothing) when payload exceeds
// MaxTCPFrameSize.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxTCPFrameSize {
		return ErrFrameTooLarge
	}
	var hdr [tcpLengthPrefixSize]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	// Skip the payload write when empty: some streams (e.g. net.Pipe)
	// block on a zero-length write because the peer never reads 0 bytes.
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one length-prefixed frame from r, returning exactly
// the payload bytes declared by the prefix. It returns:
//
//	io.EOF               — clean close at a frame boundary
//	ErrIncompleteFrame   — stream ended mid-header or mid-payload
//	ErrFrameTooLarge     — declared length > MaxTCPFrameSize (close conn)
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [tcpLengthPrefixSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return nil, ErrIncompleteFrame
		}
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxTCPFrameSize {
		return nil, ErrFrameTooLarge
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, ErrIncompleteFrame
		}
		return nil, err
	}
	return payload, nil
}
