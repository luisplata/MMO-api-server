package server

// Interest fanout tests (spec R18/S18.2, design D4): spawn/despawn events
// drained from the simulation must be turned into SpawnEntity/
// DespawnEntity TCP frames addressed to each target player's connection,
// encoded with that target's negotiated wire version. Targets use
// net.Pipe (synchronous, no listener) with a reader goroutine draining
// the client end so writes never block the fanout.

import (
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// addTargetPlayer registers a player in the server map AND the
// simulation (the enterWorld state) with a net.Pipe conn, returning the
// client end for reading frames.
func addTargetPlayer(t *testing.T, srv *Server, id string) net.Conn {
	t.Helper()
	clientConn := mapPlayerWithConn(t, srv, id)
	if err := srv.sim.RegisterPlayer(id, game.Vec2{}); err != nil {
		t.Fatalf("RegisterPlayer(%s): %v", id, err)
	}
	return clientConn
}

// mapPlayerWithConn registers a player in the server map only (no sim
// entry) with a net.Pipe conn.
func mapPlayerWithConn(t *testing.T, srv *Server, id string) net.Conn {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	sess := newInWorldSession(t, srv.reg, id)
	addTestPlayer(t, srv, id, sess)
	srv.mu.Lock()
	srv.players[id].tcp = serverConn
	srv.mu.Unlock()
	return clientConn
}

func TestFanoutInterest(t *testing.T) {
	t.Run("spawn event sends SpawnEntity to the target", func(t *testing.T) {
		srv := newTestServer(t)
		clientConn := addTargetPlayer(t, srv, "p1")
		frames := startFrameReader(t, clientConn)

		// Register p2 in the sim; the target p1 sees it → SpawnEvent.
		if err := srv.sim.RegisterPlayer("p2", game.Vec2{X: 10, Z: 20}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		srv.fanoutInterest()

		env, msg := decodeFrame(t, srv, nextFrame(t, frames))
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
		clientConn := addTargetPlayer(t, srv, "p1")
		frames := startFrameReader(t, clientConn)

		if err := srv.sim.RegisterPlayer("p2", game.Vec2{X: 10, Z: 20}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}
		srv.fanoutInterest()
		nextFrame(t, frames) // consume the spawn frame

		if err := srv.sim.RemovePlayer("p2"); err != nil {
			t.Fatalf("RemovePlayer: %v", err)
		}
		srv.fanoutInterest()

		_, msg := decodeFrame(t, srv, nextFrame(t, frames))
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
		// p1 is mapped (with a conn) but NOT a sim player, so p2's
		// spawn event targets only the unmapped sim player pGhost.
		clientConn := mapPlayerWithConn(t, srv, "p1")
		frames := startFrameReader(t, clientConn)

		srv.sim.RegisterPlayer("pGhost", game.Vec2{X: 1, Z: 2}) // first → no event
		srv.sim.RegisterPlayer("p2", game.Vec2{X: 1, Z: 2})     // SpawnEvent{p2 -> [pGhost]}
		srv.fanoutInterest()

		// The event was really emitted and drained...
		if events := srv.sim.TakeInterestEvents(); len(events) != 0 {
			t.Errorf("fanout must drain events, %d left", len(events))
		}
		// ...and nothing was written to the mapped player.
		expectNoFrame(t, frames)
	})

	t.Run("spawn of unknown subject skipped", func(t *testing.T) {
		srv := newTestServer(t)
		clientConn := addTargetPlayer(t, srv, "p1")
		frames := startFrameReader(t, clientConn)

		// A SpawnEvent for a subject with no entity (e.g. a despawned
		// player) must not dereference nil state.
		srv.fanoutEvent(world.InterestEvent{
			Kind:    world.SpawnEvent,
			Subject: "gone",
			Targets: []string{"p1"},
		}, "p1")

		expectNoFrame(t, frames)
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

// nextFrame waits for the next frame from the reader goroutine.
func nextFrame(t *testing.T, frames <-chan []byte) []byte {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatalf("frame reader closed before delivering")
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for a frame")
		return nil
	}
}

// expectNoFrame asserts that no frame arrives within a short window.
func expectNoFrame(t *testing.T, frames <-chan []byte) {
	t.Helper()
	select {
	case frame := <-frames:
		t.Fatalf("unexpected frame written: %v", frame)
	case <-time.After(100 * time.Millisecond):
	}
}

// startFrameReader drains frames from the client end of a net.Pipe so
// writes to the server end never block (net.Pipe is synchronous). The
// channel closes when the conn closes.
func startFrameReader(t *testing.T, conn net.Conn) <-chan []byte {
	t.Helper()
	ch := make(chan []byte, 8)
	go func() {
		for {
			frame, err := network.ReadFrame(conn)
			if err != nil {
				close(ch)
				return
			}
			ch <- frame
		}
	}()
	return ch
}
