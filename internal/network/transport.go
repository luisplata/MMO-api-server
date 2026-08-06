// Package network provides the transport seams between the session
// layer and the wire: the Transport interface (mocked by session tests)
// and the TCP/UDP framing codecs (spec R3/R4).
package network

// Transport is the session layer's seam to the network. A session drives
// the connection exclusively through this interface, so tests can mock
// it with an in-memory implementation. SendTCP carries connection
// lifecycle, handshake, auth and reliable commands; SendUDP carries
// MoveInput and position snapshots (spec R9 — channel separation).
type Transport interface {
	// SendTCP writes one length-prefixed frame on the reliable channel.
	SendTCP(frame []byte) error
	// SendUDP writes one datagram on the lossy channel (≤ MaxUDPFrameSize).
	SendUDP(frame []byte) error
	// Close tears down both channels and releases any UDP binding.
	Close() error
}
