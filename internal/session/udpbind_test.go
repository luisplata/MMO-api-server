package session

// UDP binding tests (spec R13) over the mocked transport.
//
// Coverage: S13.1 the first UDP packet carrying the udpToken binds the
// source address to the authenticated session; S13.2 a wrong or unknown
// token is ignored and no binding occurs; plus the one-session ↔
// one-UDP-peer rule and binding release on Close (S11.2).

import (
	"errors"
	"net"
	"testing"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// udpAddr builds a synthetic UDP peer address.
func udpAddr(ip string, port int) net.Addr {
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
}

// tokenFromAuth extracts the udpToken from the AuthResponse frame the
// session sent — the token a real client would read off the wire.
func tokenFromAuth(t *testing.T, reg *protocol.Registry, tr *mockTransport) []byte {
	t.Helper()
	_, msg := sentFrame(t, reg, tr, 1)
	ar, ok := msg.(*mmov1.AuthResponse)
	if !ok {
		t.Fatalf("frame 1 = %T, want AuthResponse", msg)
	}
	if !ar.Ok || len(ar.UdpToken) == 0 {
		t.Fatalf("AuthResponse ok=%v token=%x, want an ok auth with a token", ar.Ok, ar.UdpToken)
	}
	return ar.UdpToken
}

// TestUDPBindFirstPacketCarriesToken covers S13.1: the first UDP packet
// carrying the session's token binds its source address, and the session
// then reports the bound peer.
func TestUDPBindFirstPacketCarriesToken(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)
	token := tokenFromAuth(t, reg, tr)

	peer := udpAddr("192.0.2.10", 40000)
	if err := s.HandleUDP(token, peer); err != nil {
		t.Fatalf("HandleUDP(bind): %v", err)
	}
	if !s.UDPBound() {
		t.Errorf("session must be bound after the first token-carrying packet")
	}
	got := s.UDPPeer()
	if got == nil || got.String() != peer.String() {
		t.Errorf("UDPPeer = %v, want %v", got, peer)
	}
	if s.State() != StateInWorld {
		t.Errorf("binding must not change the lifecycle state (state = %s)", s.State())
	}
}

// TestUDPBindWrongTokenIgnored covers S13.2: a packet with a wrong or
// unknown token is dropped and no binding occurs.
func TestUDPBindWrongTokenIgnored(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)
	token := tokenFromAuth(t, reg, tr)

	// A wrong token.
	if err := s.HandleUDP([]byte("not-the-token"), udpAddr("192.0.2.11", 40001)); err != nil {
		t.Fatalf("HandleUDP(wrong token): %v", err)
	}
	if s.UDPBound() || s.UDPPeer() != nil {
		t.Errorf("wrong token must be ignored (bound=%v peer=%v)", s.UDPBound(), s.UDPPeer())
	}

	// A truncated/unknown token.
	if err := s.HandleUDP(token[:3], udpAddr("192.0.2.12", 40002)); err != nil {
		t.Fatalf("HandleUDP(truncated token): %v", err)
	}
	if s.UDPBound() || s.UDPPeer() != nil {
		t.Errorf("truncated token must be ignored (bound=%v peer=%v)", s.UDPBound(), s.UDPPeer())
	}

	// The real token still binds afterwards — an ignored packet never
	// consumes the token.
	if err := s.HandleUDP(token, udpAddr("192.0.2.13", 40003)); err != nil {
		t.Fatalf("HandleUDP(real token after misses): %v", err)
	}
	if !s.UDPBound() {
		t.Errorf("the real token must bind even after ignored packets")
	}
}

// TestUDPBeforeAuthIgnored: without a token there is nothing to match,
// so any pre-auth UDP packet is ignored — even an empty one (an empty
// payload must not match a not-yet-issued token).
func TestUDPBeforeAuthIgnored(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	sendHello(t, s, reg) // authenticated? no — only handshaking

	if err := s.HandleUDP([]byte{1, 2, 3}, udpAddr("192.0.2.20", 40010)); err != nil {
		t.Fatalf("HandleUDP before auth: %v", err)
	}
	if err := s.HandleUDP([]byte{}, udpAddr("192.0.2.21", 40011)); err != nil {
		t.Fatalf("HandleUDP(empty) before auth: %v", err)
	}
	if s.UDPBound() || s.UDPPeer() != nil {
		t.Errorf("pre-auth packets must never bind (bound=%v peer=%v)", s.UDPBound(), s.UDPPeer())
	}
	if tr.closed {
		t.Errorf("an ignored UDP packet must not close the session")
	}
}

// TestUDPOnePeerPerSession pins the one-session ↔ one-UDP-peer rule:
// after a successful bind, datagrams from any other address are ignored
// and the original binding is untouched.
func TestUDPOnePeerPerSession(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)
	token := tokenFromAuth(t, reg, tr)

	bound := udpAddr("192.0.2.30", 40020)
	if err := s.HandleUDP(token, bound); err != nil {
		t.Fatalf("HandleUDP(bind): %v", err)
	}

	// A datagram from a foreign address is ignored — the binding stays
	// with the first peer even if it carries the token.
	foreign := udpAddr("192.0.2.31", 40021)
	if err := s.HandleUDP(token, foreign); err != nil {
		t.Fatalf("HandleUDP(foreign peer): %v", err)
	}
	if got := s.UDPPeer(); got == nil || got.String() != bound.String() {
		t.Errorf("UDPPeer = %v, want the first bound peer %v (one session ↔ one peer)", got, bound)
	}

	// A datagram from the bound peer is accepted (nil, no error) — this
	// is where MoveInput dispatch lands in PR4.
	if err := s.HandleUDP([]byte{0x01}, bound); err != nil {
		t.Errorf("HandleUDP(bound peer) err = %v, want nil", err)
	}
	if s.State() != StateInWorld || tr.closed {
		t.Errorf("accepted datagrams must not disturb the session (state=%s closed=%v)", s.State(), tr.closed)
	}
}

// TestUDPBindAfterClose: a closed session rejects UDP with ErrClosed and
// its binding has been released (S11.2).
func TestUDPBindAfterClose(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)
	token := tokenFromAuth(t, reg, tr)

	peer := udpAddr("192.0.2.40", 40030)
	if err := s.HandleUDP(token, peer); err != nil {
		t.Fatalf("HandleUDP(bind): %v", err)
	}
	if !s.UDPBound() {
		t.Fatalf("setup: session should be bound")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.UDPBound() || s.UDPPeer() != nil {
		t.Errorf("Close must release the UDP binding (bound=%v peer=%v)", s.UDPBound(), s.UDPPeer())
	}
	if err := s.HandleUDP(token, peer); !errors.Is(err, ErrClosed) {
		t.Errorf("HandleUDP after Close err = %v, want ErrClosed", err)
	}
}
