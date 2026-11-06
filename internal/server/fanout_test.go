package server

// Interest fanout tests (spec R18/S18.2, design D4): spawn/despawn events
// drained from the simulation must be turned into SpawnEntity/
// DespawnEntity frames addressed to each target player's sink, encoded
// with that target's negotiated wire version. Targets use recording sinks,
// so no socket or listener is needed.

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/stats"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// mapPlayer registers a player in the server map only (no sim entry) with
// a recording outbound Sink (design P3) and returns it.
func mapPlayer(t *testing.T, srv *Server, id string) *testSink {
	t.Helper()
	sess := newInWorldSession(t, srv.reg, id)
	sink := newTestSink(16)
	t.Cleanup(func() { _ = sink.sink.Close() })
	srv.mu.Lock()
	srv.players[id] = &player{sess: sess, sink: sink.sink}
	srv.mu.Unlock()
	return sink
}

// addTargetPlayer registers a player in the server map AND the
// simulation (the enterWorld state) with a recording sink, returning that
// sink for reading frames.
func addTargetPlayer(t *testing.T, srv *Server, id string) *testSink {
	t.Helper()
	sink := mapPlayer(t, srv, id)
	if err := srv.sim.RegisterPlayer(id, game.Vec2{}, "", stats.Stats{}); err != nil {
		t.Fatalf("RegisterPlayer(%s): %v", id, err)
	}
	return sink
}

func TestFanoutInterest(t *testing.T) {
	t.Run("spawn event sends SpawnEntity to the target", func(t *testing.T) {
		srv := newTestServer(t)
		sink := addTargetPlayer(t, srv, "p1")

		// Register p2 in the sim; the target p1 sees it → SpawnEvent.
		if err := srv.sim.RegisterPlayer("p2", game.Vec2{X: 10, Z: 20}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		srv.fanoutInterest()

		env, msg := decodeFrame(t, srv, sink.nextReliable(t))
		if env.Version != testVersion {
			t.Errorf("envelope version = %d, want target's negotiated %d", env.Version, testVersion)
		}
		sp, ok := msg.(*mmov1.SpawnEntity)
		if !ok {
			t.Fatalf("frame carries %T, want *mmov1.SpawnEntity", msg)
		}
		if sp.EntityId != "p2" {
			t.Errorf("SpawnEntity.EntityId = %q, want p2", sp.EntityId)
		}
		if sp.State == nil || sp.State.Pos == nil || sp.State.Pos.X != 10 || sp.State.Pos.Z != 20 {
			t.Errorf("SpawnEntity.State = %v, want p2 at (10, 20)", sp.State)
		}
	})

	t.Run("despawn event sends DespawnEntity to the target", func(t *testing.T) {
		srv := newTestServer(t)
		sink := addTargetPlayer(t, srv, "p1")

		if err := srv.sim.RegisterPlayer("p2", game.Vec2{X: 10, Z: 20}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		srv.fanoutInterest()
		sink.nextReliable(t) // consume the spawn frame

		if err := srv.sim.RemovePlayer("p2"); err != nil {
			t.Fatalf("RemovePlayer: %v", err)
		}
		srv.fanoutInterest()

		_, msg := decodeFrame(t, srv, sink.nextReliable(t))
		dp, ok := msg.(*mmov1.DespawnEntity)
		if !ok {
			t.Fatalf("frame carries %T, want *mmov1.DespawnEntity", msg)
		}
		if dp.EntityId != "p2" {
			t.Errorf("DespawnEntity.EntityId = %q, want p2", dp.EntityId)
		}
	})

	t.Run("unknown target skipped without writing", func(t *testing.T) {
		srv := newTestServer(t)
		// p1 is mapped but NOT a sim player, so p2's spawn event targets
		// only the unmapped sim player pGhost.
		sink := mapPlayer(t, srv, "p1")

		srv.sim.RegisterPlayer("pGhost", game.Vec2{X: 1, Z: 2}, "", stats.Stats{}) // first → no event
		srv.sim.RegisterPlayer("p2", game.Vec2{X: 1, Z: 2}, "", stats.Stats{})     // SpawnEvent{p2 -> [pGhost]}
		srv.fanoutInterest()

		// The event was really emitted and drained...
		if events := srv.sim.TakeInterestEvents(); len(events) != 0 {
			t.Errorf("fanout must drain events, %d left", len(events))
		}
		// ...and nothing was written to the mapped player.
		sink.expectNoFrames(t)
	})

	t.Run("spawn of unknown subject skipped", func(t *testing.T) {
		srv := newTestServer(t)
		sink := addTargetPlayer(t, srv, "p1")

		// A SpawnEvent for a subject with no entity (e.g. a despawned
		// player) must not dereference nil state.
		srv.fanoutEvent(srv.runner.room(DefaultRoom), world.InterestEvent{
			Kind:    world.SpawnEvent,
			Subject: "gone",
			Targets: []string{"p1"},
		}, "p1")

		sink.expectNoFrames(t)
	})
}

// decodeFrame decodes a raw envelope frame into its typed message.
func decodeFrame(t *testing.T, srv *Server, frame []byte) (*protocol.Envelope, proto.Message) {
	t.Helper()
	env, err := protocol.DecodeEnvelope(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	msg, err := srv.reg.DecodeMessage(*env)
	if err != nil {
		t.Fatalf("registry decode: %v", err)
	}
	return env, msg
}
