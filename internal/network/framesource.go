package network

// Inbound transport seam (design P1): the session/server read loop
// consumes frames through FrameSource instead of a raw net.Conn, so the
// transport is swappable — LenPrefixSource for TCP today, a one-WS-
// message-per-frame source for WSS in a later slice.

import "io"

// FrameSource is the inbound counterpart to Transport. Next returns the
// next complete frame, or an error. A clean close at a frame boundary is
// reported as io.EOF; a protocol violation (oversize frame, truncated
// stream) surfaces the framing error so the caller can terminate the
// connection.
type FrameSource interface {
	// Next returns the next frame's payload bytes, or an error.
	Next() ([]byte, error)
}

// LenPrefixSource reads length-prefixed TCP frames (spec R3) from an
// io.Reader, reassembling the stream by its 4-byte BE uint32 prefix with
// payload capped at MaxTCPFrameSize. It delegates to ReadFrame, so the
// on-wire bytes are byte-for-byte identical to the pre-seam read path.
type LenPrefixSource struct {
	r io.Reader
}

// NewLenPrefixSource wraps r as a length-prefixed FrameSource.
func NewLenPrefixSource(r io.Reader) *LenPrefixSource {
	return &LenPrefixSource{r: r}
}

// Next returns the next length-prefixed frame, or the framing error.
func (s *LenPrefixSource) Next() ([]byte, error) {
	return ReadFrame(s.r)
}

// compile-time assertion: LenPrefixSource satisfies FrameSource.
var _ FrameSource = (*LenPrefixSource)(nil)
