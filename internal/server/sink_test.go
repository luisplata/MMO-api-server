package server

// Snapshot sink tests: SendSnapshot must deliver a per-player snapshot
// only to the addressed player's sink — never to an unknown player and
// never to every connected player. Delivery is async: the player's
// outbound Sink (design P2) writes the frame on its own goroutine, so a
// slow peer cannot stall the sim-owner goroutine.

import (
	"testing"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestSendSnapshot(t *testing.T) {
	snap := &mmov1.Snapshot{
		Seq: 42,
		Entities: []*mmov1.EntityState{
			{Id: testCharID, Pos: &mmov1.Vec3{X: 1, Y: 0, Z: 2}, Velocity: &mmov1.Vec2{X: 3, Z: 4}, Yaw: 0.5},
		},
	}

	cases := []struct {
		name     string
		playerID string // the snapshot target
		want     int    // expected realtime frames written to the registered player
	}{
		{"unknown player skipped", "ghost", 0},
		{"registered player receives snapshot", testCharID, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			sess := newInWorldSession(t, srv.reg, "alice")
			ts := addTestPlayer(t, srv, testCharID, sess)

			if err := srv.SendSnapshot(tc.playerID, snap); err != nil {
				t.Fatalf("SendSnapshot: %v", err)
			}

			if tc.want == 0 {
				// Addressed to an unknown player: the registered player's
				// sink must stay silent.
				ts.expectNoFrames(t)
				return
			}

			env, msg := decodeFrame(t, srv, ts.nextRealtime(t))
			if env.Version != testVersion {
				t.Errorf("envelope version = %d, want negotiated %d", env.Version, testVersion)
			}
			got, ok := msg.(*mmov1.Snapshot)
			if !ok {
				t.Fatalf("frame carries %T, want *mmov1.Snapshot", msg)
			}
			if got.Seq != 42 {
				t.Errorf("snapshot seq = %d, want 42", got.Seq)
			}
			if len(got.Entities) != 1 || got.Entities[0].Id != testCharID || got.Entities[0].Pos.X != 1 {
				t.Errorf("snapshot entities = %v, want %s at x=1", got.Entities, testCharID)
			}
		})
	}
}
