package server

// P7 character-to-room routing tests. They pin the invariants: a character
// is owned by exactly one room, work resolves through that index, nothing
// crosses rooms, and unknown character ids are dropped.

import (
	"context"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/stats"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// startRoomOwner runs the single room-owner loop so simDo ops are serviced.
// The wake channel never fires: tests step rooms explicitly.
func startRoomOwner(t *testing.T, srv *Server) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.runSim(ctx, make(chan time.Time))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ctx
}

// joinRoom claims cid for roomID (production index) and registers it in the
// room's simulation through the room-owner op path, returning that room's sim.
func joinRoom(t *testing.T, srv *Server, ctx context.Context, cid string, roomID rooms.RoomID) *game.Simulation {
	t.Helper()
	if err := srv.bindCharacter(cid, roomID); err != nil {
		t.Fatalf("bindCharacter(%s, %s): %v", cid, roomID, err)
	}
	if _, err := srv.runner.ensureRoom(roomID); err != nil {
		t.Fatalf("ensureRoom(%s): %v", roomID, err)
	}
	if err := srv.simDo(ctx, roomID, +1, func(sim *game.Simulation) error {
		return sim.RegisterPlayer(cid, game.Vec2{}, "", stats.Stats{})
	}); err != nil {
		t.Fatalf("register %s in %s: %v", cid, roomID, err)
	}
	return srv.runner.room(roomID).sim
}

// TestRoomIsolation pins 7.1: two independent one-player rooms never leak
// movement, entities or spawn fanout from one into the other.
func TestRoomIsolation(t *testing.T) {
	t.Run("movement in room A never reaches room B", func(t *testing.T) {
		srv := newTestServer(t)
		ctx := startRoomOwner(t, srv)
		simA := joinRoom(t, srv, ctx, "pA", "room-a")
		simB := joinRoom(t, srv, ctx, "pB", "room-b")

		sim, ok := srv.simForCharacter("pA")
		if !ok || sim != simA {
			t.Fatalf("simForCharacter(pA) = %p, %v; want room A's sim %p", sim, ok, simA)
		}
		if err := sim.QueueInput("pA", &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5,
		}); err != nil {
			t.Fatalf("QueueInput: %v", err)
		}
		if err := srv.runner.stepAll(); err != nil {
			t.Fatalf("stepAll: %v", err)
		}

		a, _ := simA.Entity("pA")
		if a == nil || !almostEqual(a.Pos.X, 0.25) {
			t.Errorf("room A player pos.X = %v, want ~0.25", a)
		}
		b, _ := simB.Entity("pB")
		if b == nil || b.Pos.X != 0 || b.Pos.Z != 0 {
			t.Errorf("room B player pos = %v, want untouched (0, 0)", b)
		}
		if _, leaked := simB.Entity("pA"); leaked {
			t.Error("room A entity leaked into room B's simulation")
		}
	})

	t.Run("spawn fanout never crosses rooms", func(t *testing.T) {
		srv := newTestServer(t)
		ctx := startRoomOwner(t, srv)

		sinkB := mapPlayer(t, srv, "pB")
		sinkA := mapPlayer(t, srv, "pA")

		joinRoom(t, srv, ctx, "pA", "room-a")
		joinRoom(t, srv, ctx, "pA2", "room-a") // spawns for pA (same room)
		joinRoom(t, srv, ctx, "pB", "room-b")  // first in room B: no event
		srv.fanoutInterest()

		_, msg := decodeFrame(t, srv, sinkA.nextReliable(t))
		sp, ok := msg.(*mmov1.SpawnEntity)
		if !ok || sp.EntityId != "pA2" {
			t.Fatalf("room A target got %T %v, want SpawnEntity pA2", msg, msg)
		}
		sinkB.expectNoFrames(t)
	})

	t.Run("unknown character input is dropped", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		// Mapped and registered, but never bound to a room: the index has
		// no entry, so the input must be dropped, not defaulted.
		mapPlayer(t, srv, testCharID)
		if err := srv.sim.RegisterPlayer(testCharID, game.Vec2{}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}

		// Drive the WSS inbound path exactly as serveConn does: decode the
		// frame, then queue it for the session's room.
		frame := clientFrame(t, srv.reg, &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5,
		}, 0, 0)
		mi, _, ok := srv.decodeMoveInput(frame)
		if !ok {
			t.Fatal("decodeMoveInput rejected a valid MoveInput")
		}
		srv.queueMoveInput(sess, mi)

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity(testCharID)
		if e.Pos.X != 0 || e.Pos.Z != 0 {
			t.Errorf("unknown character input reached a room; pos = %v", e.Pos)
		}
	})
}

// TestDuplicateCharacterRejected pins 7.2: one character lives in at most one
// room; the second tab is rejected and its teardown never evicts the first.
func TestDuplicateCharacterRejected(t *testing.T) {
	srv := newTestServer(t)
	ctx := startRoomOwner(t, srv)

	sess1 := newInWorldSession(t, srv.reg, "alice")
	sink1 := newTestSink(16)
	t.Cleanup(func() { _ = sink1.sink.Close() })
	if err := srv.enterWorldWithSink(ctx, nil, sess1, &mockTransport{}, sink1.sink, "room-a"); err != nil {
		t.Fatalf("first enterWorldIn: %v", err)
	}

	sess2 := newInWorldSession(t, srv.reg, "alice")
	sink2 := newTestSink(4)
	t.Cleanup(func() { _ = sink2.sink.Close() })
	err := srv.enterWorldWithSink(ctx, nil, sess2, &mockTransport{}, sink2.sink, "room-b")
	if err == nil {
		t.Fatal("second tab with the same character must be rejected")
	}
	// serveConn always tears the failed session down; that must be a no-op
	// for the room/player owned by the first tab.
	srv.cleanupConn(ctx, nil, sess2)

	room, ok := srv.roomOfCharacter(testCharID)
	if !ok || room != "room-a" {
		t.Fatalf("character room = %q, %v; want room-a", room, ok)
	}
	if srv.runner.room("room-b") != nil {
		t.Error("rejected duplicate must not create room-b")
	}
	if e, ok := srv.runner.room("room-a").sim.Entity(testCharID); !ok || e == nil {
		t.Error("rejected duplicate must not remove the first tab's entity")
	}
	srv.mu.Lock()
	p := srv.players[testCharID]
	srv.mu.Unlock()
	if p == nil || p.sess != sess1 {
		t.Error("rejected duplicate must not replace the first tab's player")
	}
	if err := srv.bindCharacter(testCharID, "room-a"); err == nil {
		t.Error("re-binding an owned character (same room) must fail")
	}
	if err := srv.bindCharacter("", "room-a"); err != ErrEmptyCharacterID {
		t.Errorf("empty character bind = %v, want ErrEmptyCharacterID", err)
	}
	if _, ok := srv.simForCharacter("ghost"); ok {
		t.Error("unknown character must not resolve to a simulation")
	}
}

// TestScopedLeaveDespawn pins 7.3: a leave removes only the owning room's
// entity and despawns only that room's members; the empty room is destroyed
// while other rooms and players survive.
func TestScopedLeaveDespawn(t *testing.T) {
	srv := newTestServer(t)
	ctx := startRoomOwner(t, srv)

	clientB := mapPlayer(t, srv, "pB")
	clientA := mapPlayer(t, srv, "pA")
	clientA2 := mapPlayer(t, srv, "pA2")

	joinRoom(t, srv, ctx, "pA", "room-a")
	joinRoom(t, srv, ctx, "pA2", "room-a")
	joinRoom(t, srv, ctx, "pB", "room-b")
	srv.fanoutInterest()
	// pA2's join spawns it for pA; pA2 itself is not a target yet.
	_, msg := decodeFrame(t, srv, clientA.nextReliable(t))
	if sp, ok := msg.(*mmov1.SpawnEntity); !ok || sp.EntityId != "pA2" {
		t.Fatalf("initial fanout = %T, want SpawnEntity pA2", msg)
	}

	if err := srv.simDo(ctx, "room-a", -1, func(sim *game.Simulation) error {
		return sim.RemovePlayer("pA")
	}); err != nil {
		t.Fatalf("leave pA: %v", err)
	}
	srv.fanoutInterest()

	_, delMsg := decodeFrame(t, srv, clientA2.nextReliable(t))
	dp, ok := delMsg.(*mmov1.DespawnEntity)
	if !ok || dp.EntityId != "pA" {
		t.Fatalf("room A target got %T %v, want DespawnEntity pA", delMsg, delMsg)
	}
	clientB.expectNoFrames(t) // room B saw nothing

	if err := srv.simDo(ctx, "room-a", -1, func(sim *game.Simulation) error {
		return sim.RemovePlayer("pA2")
	}); err != nil {
		t.Fatalf("leave pA2: %v", err)
	}
	srv.fanoutInterest()

	if srv.runner.room("room-a") != nil {
		t.Error("emptied room-a must be destroyed")
	}
	roomB := srv.runner.room("room-b")
	if roomB == nil {
		t.Fatal("room-b must survive room-a's teardown")
	}
	if _, ok := roomB.sim.Entity("pB"); !ok {
		t.Error("room-b entity must not be removed by another room's leave")
	}
	if room, ok := srv.roomOfCharacter("pB"); !ok || room != "room-b" {
		t.Errorf("pB room = %q, %v; want room-b", room, ok)
	}
}
