package server

// The realtime inbound seam on WSS: every frame is sniffed with
// decodeMoveInput (a MoveInput is routed to the room sim, anything else
// falls through to the lifecycle state machine), then queueMoveInput
// addresses the input to the room that owns the character.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/stats"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestDecodeMoveInput(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		name        string
		frame       []byte
		wantOK      bool
		wantVersion uint16
	}{
		{
			name:        "move input carries its envelope version",
			frame:       clientFrame(t, srv.reg, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1}, Speed: 5}, 0, 0),
			wantOK:      true,
			wantVersion: testVersion,
		},
		{
			name:        "another message type is not a move input",
			frame:       clientFrame(t, srv.reg, &mmov1.Hello{ProtoVer: testVersion}, 0, 0),
			wantOK:      false,
			wantVersion: 0,
		},
		{
			name:        "garbage bytes are not a move input",
			frame:       []byte("not an envelope"),
			wantOK:      false,
			wantVersion: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mi, version, ok := srv.decodeMoveInput(tc.frame)
			if ok != tc.wantOK {
				t.Fatalf("decodeMoveInput ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return // the version is only meaningful for a decoded MoveInput
			}
			if version != tc.wantVersion {
				t.Errorf("version = %d, want %d", version, tc.wantVersion)
			}
			if mi == nil || mi.GetSeq() != 1 {
				t.Errorf("decoded MoveInput = %v, want seq 1", mi)
			}
		})
	}
}

func TestQueueMoveInput(t *testing.T) {
	t.Run("owned character moves in its room", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		if err := srv.bindCharacter(testCharID, DefaultRoom); err != nil {
			t.Fatalf("bindCharacter: %v", err)
		}
		if err := srv.sim.RegisterPlayer(testCharID, game.Vec2{}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}

		srv.queueMoveInput(sess, &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5, Yaw: 1.5,
		})

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity(testCharID)
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

	t.Run("character without a room is dropped", func(t *testing.T) {
		srv := newTestServer(t)
		sess := newInWorldSession(t, srv.reg, "alice")
		// Registered in the legacy sim but never bound: no room owns it.
		if err := srv.sim.RegisterPlayer(testCharID, game.Vec2{}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer: %v", err)
		}

		srv.queueMoveInput(sess, &mmov1.MoveInput{
			Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5,
		})

		if err := srv.sim.Step(); err != nil {
			t.Fatalf("Step: %v", err)
		}
		e, _ := srv.sim.Entity(testCharID)
		if e.Pos.X != 0 || e.Pos.Z != 0 {
			t.Errorf("unowned character input reached a room; pos = %v", e.Pos)
		}
	})
}
