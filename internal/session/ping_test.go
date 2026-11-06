package session

import (
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func sendPing(t *testing.T, s *Session, reg *protocol.Registry, nonce uint64, clientTime int64) {
	t.Helper()
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.Ping{Nonce: nonce, ClientTime: clientTime}, 0, 0)); err != nil {
		t.Fatalf("Ping(nonce=%d): %v", nonce, err)
	}
}

func TestPingEchoesNonceAndClientTime(t *testing.T) {
	cases := []struct {
		name       string
		nonce      uint64
		clientTime int64
	}{
		{"zero", 0, 0},
		{"populated", 0xDEADBEEF, 1_782_912_345_678},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tr, _ := newTestSession(t, nil)
			reg := protocol.NewWorldRegistry()
			inWorld(t, s, reg)
			before := len(tr.tcpFrames)

			sendPing(t, s, reg, tc.nonce, tc.clientTime)

			env, msg := sentFrame(t, reg, tr, before)
			pong, ok := msg.(*mmov1.Pong)
			if !ok {
				t.Fatalf("Ping reply = %T, want *mmov1.Pong", msg)
			}
			if pong.Nonce != tc.nonce || pong.ClientTime != tc.clientTime {
				t.Errorf("Pong = {%d %d}, want {%d %d}", pong.Nonce, pong.ClientTime, tc.nonce, tc.clientTime)
			}
			if env.Type != 20 || env.Flags&protocol.FlagNeedsAck != 0 {
				t.Errorf("Pong envelope = type %d flags %d, want type 20 without needs-ack", env.Type, env.Flags)
			}
			if s.State() != StateInWorld || tr.closed {
				t.Errorf("Ping changed the session (state=%s closed=%v)", s.State(), tr.closed)
			}
		})
	}
}

func TestPingRateLimitedWithoutClosing(t *testing.T) {
	s, tr, clock := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)
	before := len(tr.tcpFrames)

	for i := 0; i < pingsPerSecond; i++ {
		sendPing(t, s, reg, uint64(i+1), int64(i+1))
	}
	if got := len(tr.tcpFrames) - before; got != pingsPerSecond {
		t.Fatalf("answered %d/%d pings in the first second", got, pingsPerSecond)
	}

	// Excess probes are dropped silently: no Pong, no close, no state change.
	for i := 0; i < 10; i++ {
		sendPing(t, s, reg, uint64(100+i), int64(100+i))
	}
	if got := len(tr.tcpFrames) - before; got != pingsPerSecond {
		t.Errorf("answered %d pings after flood, want %d", got, pingsPerSecond)
	}
	if s.State() != StateInWorld || tr.closed {
		t.Errorf("ping flood perturbed the session (state=%s closed=%v)", s.State(), tr.closed)
	}

	clock.Advance(time.Second)
	sendPing(t, s, reg, 999, 999)
	if got := len(tr.tcpFrames) - before; got != pingsPerSecond+1 {
		t.Errorf("answered %d pings after the window rolled, want %d", got, pingsPerSecond+1)
	}
}
