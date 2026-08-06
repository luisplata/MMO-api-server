package session

// UDP token binding (spec R13).
//
// The first UDP datagram carries the raw udpToken issued at AuthResponse
// (the datagram payload IS the token — the v1 contract has no BindRequest
// message, so no envelope wraps it). On an exact match the source address
// is bound to the authenticated TCP session; wrong or unknown tokens are
// ignored and no binding occurs. After a successful bind the session
// accepts datagrams only from that single peer (one session ↔ one UDP
// peer) — typed dispatch of MoveInput/snapshots lands in PR4.

import (
	"bytes"
	"net"
)

// HandleUDP processes one inbound UDP datagram (spec R13).
//
// First packet: if its payload equals the session's udpToken, the source
// address becomes the bound peer (S13.1). Anything else is ignored — a
// wrong or unknown token must never bind and never disturb the session
// (S13.2). A not-yet-authenticated session has no token, so no packet can
// match (an empty payload does not match a nil token).
//
// After a successful bind, datagrams from the bound peer are accepted
// (PR4 dispatches MoveInput there) and datagrams from any other address
// are ignored, leaving the binding untouched.
//
// A closed session rejects all datagrams with ErrClosed.
func (s *Session) HandleUDP(payload []byte, addr net.Addr) error {
	if s.state.IsTerminal() {
		return ErrClosed
	}
	if !s.udpBound {
		if len(s.udpToken) == 0 || !bytes.Equal(payload, s.udpToken) {
			return nil // wrong/unknown token: ignored (S13.2)
		}
		s.udpBound = true
		s.udpPeer = addr
		return nil
	}
	// Bound: only the single bound peer is accepted.
	if addr.String() != s.udpPeer.String() {
		return nil
	}
	return nil
}

// UDPPeer returns the bound UDP address, or nil when no peer is bound.
func (s *Session) UDPPeer() net.Addr {
	if !s.udpBound {
		return nil
	}
	return s.udpPeer
}
