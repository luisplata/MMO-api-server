package server

// Pure helper tests for the wiring layer (PR4b): the conversion between
// the wire Vec2 and the sim's internal Vec2, the UDP bind-token
// classification, and the game.Entity → mmov1.EntityState mapping used by
// the interest fanout.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/game"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func almostEqual(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-4
}

func TestVecFromProto(t *testing.T) {
	cases := []struct {
		name string
		v    *mmov1.Vec2
		want game.Vec2
	}{
		{"nil yields zero", nil, game.Vec2{}},
		{"values copied", &mmov1.Vec2{X: 1.5, Z: -2.5}, game.Vec2{X: 1.5, Z: -2.5}},
		{"zero proto yields zero", &mmov1.Vec2{}, game.Vec2{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vecFromProto(tc.v)
			if got.X != tc.want.X || got.Z != tc.want.Z {
				t.Errorf("vecFromProto(%v) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestIsBindToken(t *testing.T) {
	cases := []struct {
		name           string
		payload, token []byte
		want           bool
	}{
		{"exact match is the bind", []byte("tok"), []byte("tok"), true},
		{"different payload is not", []byte("tok"), []byte("other"), false},
		{"nil token never matches", []byte("tok"), nil, false},
		{"empty payload does not match", nil, []byte("tok"), false},
		{"shorter payload is not a prefix match", []byte("t"), []byte("tok"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBindToken(tc.payload, tc.token); got != tc.want {
				t.Errorf("isBindToken(%q, %q) = %v, want %v", tc.payload, tc.token, got, tc.want)
			}
		})
	}
}

func TestEntityStateFromGame(t *testing.T) {
	e := &game.Entity{
		ID:       "p1",
		Pos:      game.Vec2{X: 1, Z: 2},
		Velocity: game.Vec2{X: 3, Z: 4},
		Yaw:      0.5,
	}
	got := entityStateFromGame(e)
	if got.Id != "p1" {
		t.Errorf("Id = %q, want p1", got.Id)
	}
	if got.Pos == nil || got.Pos.X != 1 || got.Pos.Z != 2 {
		t.Errorf("Pos = %v, want (1, 2)", got.Pos)
	}
	if got.Velocity == nil || got.Velocity.X != 3 || got.Velocity.Z != 4 {
		t.Errorf("Velocity = %v, want (3, 4)", got.Velocity)
	}
	if !almostEqual(got.Yaw, 0.5) {
		t.Errorf("Yaw = %v, want 0.5", got.Yaw)
	}
}
