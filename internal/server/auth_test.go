package server

// devAuthenticator tests (design: v1 dev-mode auth). Enabled accepts any
// credentials and returns username as the player id with a fixed spawn;
// disabled rejects everything as a placeholder for real auth.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/game"
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
		wantSpawn *mmov1.Vec2
	}{
		{
			name:      "enabled accepts any credentials",
			auth:      devAuthenticator{enabled: true, spawn: game.Vec2{X: 5, Z: 6}},
			username:  "carol",
			password:  "anything",
			wantID:    "carol",
			wantSpawn: &mmov1.Vec2{X: 5, Z: 6},
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
			if spawn == nil || spawn.X != tc.wantSpawn.X || spawn.Z != tc.wantSpawn.Z {
				t.Errorf("spawn = %v, want (%v, %v)", spawn, tc.wantSpawn.X, tc.wantSpawn.Z)
			}
		})
	}
}
