package network

// UDP framing (spec R4). One datagram carries exactly one frame; a UDP
// frame must not exceed MaxUDPFrameSize (1200 B) so it stays under the
// typical 1500 B MTU and is never IP-fragmented. Larger messages must go
// over TCP (spec R9 — channel separation).

import (
	"errors"
	"net"
)

const (
	// MaxUDPFrameSize caps a UDP frame at 1200 B (spec R4): MTU-safe,
	// leaving room for IP + UDP headers under a 1500 B MTU.
	MaxUDPFrameSize = 1200

	// udpReadBufferSize is MaxUDPFrameSize + 1 so a peer violating the
	// limit can be detected on read (a longer datagram fills the buffer
	// and ReadFrom truncates to its length — n > MaxUDPFrameSize flags it).
	udpReadBufferSize = MaxUDPFrameSize + 1
)

// ErrDatagramTooLarge is returned when a UDP frame exceeds
// MaxUDPFrameSize. The frame is rejected whole — never fragmented or
// partially sent (spec S4.2).
var ErrDatagramTooLarge = errors.New("network: udp datagram exceeds max size")

// ValidateUDPFrame reports whether frame fits in a single MTU-safe
// datagram (≤ MaxUDPFrameSize).
func ValidateUDPFrame(frame []byte) error {
	if len(frame) > MaxUDPFrameSize {
		return ErrDatagramTooLarge
	}
	return nil
}

// SendDatagram writes frame as exactly one datagram to addr. Frames
// larger than MaxUDPFrameSize are rejected before any write (spec S4.2).
func SendDatagram(pc net.PacketConn, addr net.Addr, frame []byte) error {
	if err := ValidateUDPFrame(frame); err != nil {
		return err
	}
	_, err := pc.WriteTo(frame, addr)
	return err
}

// ReadDatagram reads one datagram and returns its frame and source
// address. A datagram larger than MaxUDPFrameSize (a protocol-violating
// peer) is rejected with ErrDatagramTooLarge.
func ReadDatagram(pc net.PacketConn) ([]byte, net.Addr, error) {
	buf := make([]byte, udpReadBufferSize)
	n, addr, err := pc.ReadFrom(buf)
	if err != nil {
		return nil, nil, err
	}
	if n > MaxUDPFrameSize {
		return nil, nil, ErrDatagramTooLarge
	}
	return buf[:n], addr, nil
}
