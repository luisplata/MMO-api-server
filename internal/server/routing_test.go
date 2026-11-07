package server

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/stats"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

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

type discardConn struct{}

func (discardConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (discardConn) Write(p []byte) (int, error)      { return len(p), nil }
func (discardConn) Close() error                     { return nil }
func (discardConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (discardConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (discardConn) SetDeadline(time.Time) error      { return nil }
func (discardConn) SetReadDeadline(time.Time) error  { return nil }
func (discardConn) SetWriteDeadline(time.Time) error { return nil }

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

		clientB := mapPlayerWithConn(t, srv, "pB")
		framesB := startFrameReader(t, clientB)
		clientA := mapPlayerWithConn(t, srv, "pA")
		framesA := startFrameReader(t, clientA)

		joinRoom(t, srv, ctx, "pA", "room-a")
		joinRoom(t, srv, ctx, "pA2", "room-a") // spawns for pA (same room)
		joinRoom(t, srv, ctx, "pB", "room-b")  // first in room B: no event
		srv.fanoutInterest()

		_, msg := decodeFrame(t, srv, nextFrame(t, framesA))
		sp, ok := msg.(*mmov1.SpawnEntity)
		if !ok || sp.EntityId != "pA2" {
			t.Fatalf("room A target got %T %v, want SpawnEntity pA2", msg, msg)
		}
		expectNoFrame(t, framesB)
	})

	t.Run("unknown character input is dropped", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		// Mapped and registered, but never bound to a room: the index has
		// no entry, so the input must be dropped, not defaulted.
		clientConn := mapPlayerWithConn(t, srv, testCharID)
		_ = startFrameReader(t, clientConn)
		if err := srv.sim.RegisterPlayer(testCharID, game.Vec2{}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		addr := fakeAddr("10.0.0.1:9000")
		srv.routeUDP(sess.UDPToken(), addr)
		srv.routeUDP(clientFrame(t, srv.reg, &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5,
		}, 0, 0), addr)

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity(testCharID)
		if e.Pos.X != 0 || e.Pos.Z != 0 {
			t.Errorf("unknown character input reached a room; pos = %v", e.Pos)
		}
	})
}

func TestDuplicateCharacterRejected(t *testing.T) {
	srv := newTestServer(t)
	ctx := startRoomOwner(t, srv)

	sess1 := newInWorldSession(t, srv.reg, "alice")
	if err := srv.enterWorldIn(ctx, discardConn{}, sess1, &mockTransport{}, "room-a"); err != nil {
		t.Fatalf("first enterWorldIn: %v", err)
	}

	sess2 := newInWorldSession(t, srv.reg, "alice")
	conn2 := discardConn{}
	err := srv.enterWorldIn(ctx, conn2, sess2, &mockTransport{}, "room-b")
	if err == nil {
		t.Fatal("second tab with the same character must be rejected")
	}
	srv.cleanupConn(ctx, conn2, sess2)

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

	clientB := mapPlayerWithConn(t, srv, "pB")
	framesB := startFrameReader(t, clientB)
	clientA := mapPlayerWithConn(t, srv, "pA")
	framesA := startFrameReader(t, clientA)
	clientA2 := mapPlayerWithConn(t, srv, "pA2")
	framesA2 := startFrameReader(t, clientA2)

	joinRoom(t, srv, ctx, "pA", "room-a")
	joinRoom(t, srv, ctx, "pA2", "room-a")
	joinRoom(t, srv, ctx, "pB", "room-b")
	srv.fanoutInterest()
	// pA2's join spawns it for pA; pA2 itself is not a target yet.
	_, msg := decodeFrame(t, srv, nextFrame(t, framesA))
	if sp, ok := msg.(*mmov1.SpawnEntity); !ok || sp.EntityId != "pA2" {
		t.Fatalf("initial fanout = %T, want SpawnEntity pA2", msg)
	}

	if err := srv.simDo(ctx, "room-a", -1, func(sim *game.Simulation) error {
		return sim.RemovePlayer("pA")
	}); err != nil {
		t.Fatalf("leave pA: %v", err)
	}
	srv.fanoutInterest()

	_, delMsg := decodeFrame(t, srv, nextFrame(t, framesA2))
	dp, ok := delMsg.(*mmov1.DespawnEntity)
	if !ok || dp.EntityId != "pA" {
		t.Fatalf("room A target got %T %v, want DespawnEntity pA", delMsg, delMsg)
	}
	expectNoFrame(t, framesB) // room B saw nothing

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
