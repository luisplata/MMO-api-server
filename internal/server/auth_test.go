package server

// devAuthenticator tests (design: v1 dev-mode auth). Enabled accepts any
// credentials and returns username as the player id with a fixed spawn;
// disabled rejects everything as a placeholder for real auth.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestDevAuthenticator(t *testing.T) {
	cases := []struct {
		name      string
		auth      devAuthenticator
		username  string
		password  string
		wantID    string
		wantErr   bool
		wantSpawn *mmov1.Vec3
	}{
		{
			name:      "enabled accepts any credentials",
			auth:      devAuthenticator{enabled: true, spawn: game.Vec2{X: 5, Z: 6}},
			username:  "carol",
			password:  "anything",
			wantID:    "carol",
			wantSpawn: &mmov1.Vec3{X: 5, Y: 0, Z: 6},
		},
		{
			name:      "enabled rejects empty username",
			auth:      devAuthenticator{enabled: true, spawn: game.Vec2{}},
			username:  "",
			password:  "pw",
			wantID:    "",
			wantErr:   true,
			wantSpawn: nil,
		},
		{
			name:      "disabled rejects all",
			auth:      devAuthenticator{enabled: false},
			username:  "carol",
			password:  "pw",
			wantID:    "",
			wantErr:   true,
			wantSpawn: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, spawn, err := tc.auth.Authenticate(tc.username, tc.password)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Authenticate(%q, %q) = (%q, %v, nil), want error", tc.username, tc.password, id, spawn)
				}
				return
			}
			if err != nil {
				t.Fatalf("Authenticate(%q, %q): %v, want success", tc.username, tc.password, err)
			}
			if id != tc.wantID {
				t.Errorf("player id = %q, want %q", id, tc.wantID)
			}
			if spawn == nil || spawn.X != tc.wantSpawn.X || spawn.Y != tc.wantSpawn.Y || spawn.Z != tc.wantSpawn.Z {
				t.Errorf("spawn = %v, want (%v, %v, %v)", spawn, tc.wantSpawn.X, tc.wantSpawn.Y, tc.wantSpawn.Z)
			}
		})
	}
}

// TestDevAuthenticatorResolvesSpawnY pins CTH-3: the authenticator
// resolves the spawn Y from the active map, so AuthResponse.SpawnPos
// carries the terrain height at the configured spawn XZ. A nil resolver
// keeps Y = 0 (flat default).
func TestDevAuthenticatorResolvesSpawnY(t *testing.T) {
	h, err := world.DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	cases := []struct {
		name    string
		heights world.HeightResolver
		spawn   game.Vec2
		wantY   float32
	}{
		{"hill peak at spawn", h, game.Vec2{X: 100, Z: 200}, 25},
		{"hill flank", h, game.Vec2{X: 120, Z: 200}, h.HeightAt(120, 200)},
		{"flat base", h, game.Vec2{X: 0, Z: 0}, 0},
		{"no resolver stays flat", nil, game.Vec2{X: 100, Z: 200}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := devAuthenticator{enabled: true, spawn: tc.spawn, heights: tc.heights}
			_, spawn, err := d.Authenticate("carol", "pw")
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			if spawn == nil || spawn.Y != tc.wantY {
				t.Errorf("spawnPos = %v, want Y %v", spawn, tc.wantY)
			}
		})
	}
}
