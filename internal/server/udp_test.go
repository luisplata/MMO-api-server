package server

// UDP routing tests (spec R13, S13.1/S13.2, R15): the wiring must
// classify each inbound datagram — first packet carrying the token binds
// the session (no input), a MoveInput from the bound peer is queued into
// the simulation, and anything else (garbage, foreign peers) is ignored.
// No real UDP sockets: routeUDP is fed directly and the sim is stepped to
// prove inputs actually took effect.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/game"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestRouteUDP(t *testing.T) {
	t.Run("token datagram binds and queues no input", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		addTestPlayer(t, srv, "alice", sess)
		if err := srv.sim.RegisterPlayer("alice", game.Vec2{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		addr := fakeAddr("10.0.0.1:9000")

		srv.routeUDP(sess.UDPToken(), addr)

		if !sess.UDPBound() {
			t.Fatalf("token datagram must bind the session (S13.1)")
		}
		if sess.UDPPeer().String() != addr.String() {
			t.Errorf("bound peer = %v, want %v", sess.UDPPeer(), addr)
		}
		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity("alice")
		if e.Pos.X != 0 || e.Pos.Z != 0 {
			t.Errorf("bind datagram must not queue input; entity moved to %v", e.Pos)
		}
	})

	t.Run("move input from bound peer moves the entity", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		addTestPlayer(t, srv, "alice", sess)
		if err := srv.sim.RegisterPlayer("alice", game.Vec2{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		addr := fakeAddr("10.0.0.1:9000")
		srv.routeUDP(sess.UDPToken(), addr) // bind first

		frame := clientFrame(t, srv.reg, &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5, Yaw: 1.5,
		}, 0, 0)
		srv.routeUDP(frame, addr)

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity("alice")
		if !almostEqual(e.Pos.X, 0.25) {
			t.Errorf("pos.X = %v, want ~0.25 (speed 5 * 50ms dt)", e.Pos.X)
		}
		if e.Pos.Z != 0 {
			t.Errorf("pos.Z = %v, want 0", e.Pos.Z)
		}
		if !almostEqual(e.Yaw, 1.5) {
			t.Errorf("yaw = %v, want 1.5", e.Yaw)
		}
	})

	t.Run("garbage datagram ignored", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		addTestPlayer(t, srv, "alice", sess)
		if err := srv.sim.RegisterPlayer("alice", game.Vec2{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		addr := fakeAddr("10.0.0.1:9000")
		srv.routeUDP(sess.UDPToken(), addr)

		srv.routeUDP([]byte("not an envelope"), addr)

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity("alice")
		if e.Pos.X != 0 || e.Pos.Z != 0 {
			t.Errorf("garbage must be ignored; entity moved to %v", e.Pos)
		}
	})

	t.Run("input from foreign peer ignored", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		addTestPlayer(t, srv, "alice", sess)
		if err := srv.sim.RegisterPlayer("alice", game.Vec2{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		srv.routeUDP(sess.UDPToken(), fakeAddr("10.0.0.1:9000")) // bound peer A

		frame := clientFrame(t, srv.reg, &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5, Yaw: 1.5,
		}, 0, 0)
		srv.routeUDP(frame, fakeAddr("203.0.113.9:9999")) // peer B

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity("alice")
		if e.Pos.X != 0 || e.Pos.Z != 0 {
			t.Errorf("foreign peer input must be ignored (S13.2); entity moved to %v", e.Pos)
		}
	})
}
