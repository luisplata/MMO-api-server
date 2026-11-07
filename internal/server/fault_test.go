package server

// Non-fatal write path (design P3, spec "Sink no fatal"): a peer whose
// outbound Sink write fails MUST tear down only its own connection — the
// simulation keeps stepping and the other peers keep receiving. Before
// P3 the spawn/despawn fanout wrote the target's raw TCP conn inline (a
// synchronous, unbounded write on the sim-owner goroutine) and enterWorld
// returned the raw WriteFrame error; a broken client could block the tick
// or leave its conn half-torn. After P3 both paths route through
// Sink.Reliable, so the bounded queue absorbs the write and a failure
// closes only that conn.

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/stats"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestFailingSinkIsNonFatal(t *testing.T) {
	t.Run("reliable fanout failure closes only that conn", func(t *testing.T) {
		srv := newTestServer(t)

		// Healthy peer A: production-faithful sink over net.Pipe.
		clientA := mapPlayerWithConn(t, srv, "pA")
		framesA := startFrameReader(t, clientA)
		if err := srv.sim.RegisterPlayer("pA", game.Vec2{X: 1}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pA): %v", err)
		}

		// Failing peer B: a sink whose reliable write fails (a broken
		// conn); its closeConn records that the conn was torn down.
		bClosed := make(chan struct{})
		serverB, clientB := net.Pipe()
		_ = clientB.Close() // a broken client end: any direct write fails
		defer serverB.Close()
		sessB := newInWorldSession(t, srv.reg, "pB")
		sinkB := network.NewSink(
			func([]byte) error { return errors.New("broken tcp") },
			func([]byte) error { return nil },
			func() error { close(bClosed); return serverB.Close() },
		)
		t.Cleanup(func() { _ = sinkB.Close() })
		srv.mu.Lock()
		srv.players["pB"] = &player{sess: sessB, tr: &mockTransport{}, tcp: serverB, sink: sinkB}
		srv.mu.Unlock()
		if err := srv.sim.RegisterPlayer("pB", game.Vec2{X: 2}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pB): %v", err)
		}

		// A third entity spawns: both A and B are targeted.
		if err := srv.sim.RegisterPlayer("pC", game.Vec2{X: 3}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pC): %v", err)
		}
		srv.fanoutInterest()

		// A still receives both spawns (peer keeps receiving): pB first,
		// then pC — the healthy peer is unaffected by B's failing sink.
		_, firstMsg := decodeFrame(t, srv, nextFrame(t, framesA))
		if sp, ok := firstMsg.(*mmov1.SpawnEntity); !ok || sp.EntityId != "pB" {
			t.Fatalf("peer A first frame = %T, want SpawnEntity pB", firstMsg)
		}
		_, msg := decodeFrame(t, srv, nextFrame(t, framesA))
		sp, ok := msg.(*mmov1.SpawnEntity)
		if !ok || sp.EntityId != "pC" {
			t.Fatalf("peer A second frame = %T, want SpawnEntity pC", msg)
		}

		// B's conn was torn down by its failing sink (only B).
		select {
		case <-bClosed:
		case <-time.After(2 * time.Second):
			t.Fatal("failing peer's connection was not closed")
		}

		// The tick keeps stepping: a peer write failure never reaches Step.
		if err := srv.sim.Step(); err != nil {
			t.Fatalf("simulation aborted after peer write failure: %v", err)
		}
	})

	t.Run("realtime snapshot failure keeps the tick stepping", func(t *testing.T) {
		srv := newTestServer(t)

		// Healthy peer A bound to a UDP peer.
		sessA := newInWorldSession(t, srv.reg, "alice")
		addTestPlayer(t, srv, "pA", sessA)
		if err := sessA.HandleUDP(sessA.UDPToken(), fakeAddr("10.0.0.1:9000")); err != nil {
			t.Fatalf("bind A: %v", err)
		}
		if err := srv.sim.RegisterPlayer("pA", game.Vec2{X: 1}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pA): %v", err)
		}

		// Failing peer B: the realtime (snapshot) write fails.
		bClosed := make(chan struct{})
		sessB := newInWorldSession(t, srv.reg, "bob")
		sinkB := network.NewSink(
			func([]byte) error { return nil },
			func([]byte) error { return errors.New("broken udp") },
			func() error { close(bClosed); return nil },
		)
		t.Cleanup(func() { _ = sinkB.Close() })
		srv.mu.Lock()
		srv.players["pB"] = &player{sess: sessB, tr: &mockTransport{}, sink: sinkB}
		srv.mu.Unlock()
		if err := sessB.HandleUDP(sessB.UDPToken(), fakeAddr("10.0.0.2:9000")); err != nil {
			t.Fatalf("bind B: %v", err)
		}
		if err := srv.sim.RegisterPlayer("pB", game.Vec2{X: 2}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pB): %v", err)
		}

		// Step several ticks: each must return nil even though B's
		// snapshot write fails asynchronously.
		for i := 0; i < 3; i++ {
			if err := srv.sim.Step(); err != nil {
				t.Fatalf("Step %d aborted the simulation: %v", i, err)
			}
		}

		// B's conn closed by the failed snapshot write.
		select {
		case <-bClosed:
		case <-time.After(2 * time.Second):
			t.Fatal("failing peer's connection was not closed")
		}

		// A kept receiving snapshots (peer keeps receiving).
		pc := srv.udp.(*fakePacketConn)
		writes := waitForWrites(t, pc, 1)
		if writes[0].addr.String() != "10.0.0.1:9000" {
			t.Errorf("snapshot datagram addr = %v, want A's bound peer", writes[0].addr)
		}
	})
}
