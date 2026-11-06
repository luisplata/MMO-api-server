package server

// Non-fatal write path (design P3, spec "Sink no fatal"): a peer whose
// outbound Sink write fails MUST tear down only its own connection — the
// simulation keeps stepping and the other peers keep receiving. Both the
// spawn/despawn fanout (reliable) and the tick snapshots (realtime) route
// through the per-player Sink, so the bounded queue absorbs the write and
// a failure closes only that player's connection.

import (
	"errors"
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

		// Healthy peer A: a recording sink.
		sessA := newInWorldSession(t, srv.reg, "alice")
		sinkA := addTestPlayer(t, srv, "pA", sessA)
		if err := srv.sim.RegisterPlayer("pA", game.Vec2{X: 1}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pA): %v", err)
		}

		// Failing peer B: a sink whose reliable write fails; its close
		// callback records that the conn was torn down.
		bClosed := make(chan struct{})
		sessB := newInWorldSession(t, srv.reg, "bob")
		sinkB := network.NewSink(
			func([]byte) error { return errors.New("broken socket") },
			func([]byte) error { return nil },
			func() error { close(bClosed); return nil },
		)
		t.Cleanup(func() { _ = sinkB.Close() })
		if err := srv.bindCharacter("pB", DefaultRoom); err != nil {
			t.Fatalf("bindCharacter(pB): %v", err)
		}
		srv.mu.Lock()
		srv.players["pB"] = &player{sess: sessB, sink: sinkB}
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
		_, firstMsg := decodeFrame(t, srv, sinkA.nextReliable(t))
		if sp, ok := firstMsg.(*mmov1.SpawnEntity); !ok || sp.EntityId != "pB" {
			t.Fatalf("peer A first frame = %T, want SpawnEntity pB", firstMsg)
		}
		_, msg := decodeFrame(t, srv, sinkA.nextReliable(t))
		sp, ok := msg.(*mmov1.SpawnEntity)
		if !ok || sp.EntityId != "pC" {
			t.Fatalf("peer A second frame = %T, want SpawnEntity pC", msg)
		}

		// B was torn down by its failing sink (only B).
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

		// Healthy peer A with a recording sink.
		sessA := newInWorldSession(t, srv.reg, "alice")
		sinkA := addTestPlayer(t, srv, "pA", sessA)
		if err := srv.sim.RegisterPlayer("pA", game.Vec2{X: 1}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer(pA): %v", err)
		}

		// Failing peer B: the realtime (snapshot) write fails.
		bClosed := make(chan struct{})
		sessB := newInWorldSession(t, srv.reg, "bob")
		sinkB := network.NewSink(
			func([]byte) error { return nil },
			func([]byte) error { return errors.New("broken socket") },
			func() error { close(bClosed); return nil },
		)
		t.Cleanup(func() { _ = sinkB.Close() })
		if err := srv.bindCharacter("pB", DefaultRoom); err != nil {
			t.Fatalf("bindCharacter(pB): %v", err)
		}
		srv.mu.Lock()
		srv.players["pB"] = &player{sess: sessB, sink: sinkB}
		srv.mu.Unlock()
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

		// B's connection closed by the failed snapshot write.
		select {
		case <-bClosed:
		case <-time.After(2 * time.Second):
			t.Fatal("failing peer's connection was not closed")
		}

		// A kept receiving snapshots (peer keeps receiving).
		_, msg := decodeFrame(t, srv, sinkA.nextRealtime(t))
		if _, ok := msg.(*mmov1.Snapshot); !ok {
			t.Fatalf("peer A realtime frame = %T, want *mmov1.Snapshot", msg)
		}
	})
}
