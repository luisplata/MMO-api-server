package session

// Accessor tests for the wiring seam added for PR4b (cmd/server): the
// session exposes the negotiated wire version, the authenticated player
// id and the spawn position so the server layer can register the player
// in the simulation and encode frames with the right envelope version.
//
// Behavior pinned here:
//   - WireVersion is 0-less but pre-negotiation it reports the server's
//     max supported version (the same fallback the handshake uses when
//     sending VersionMismatch); after Hello it reports the negotiated
//     version.
//   - PlayerID / SpawnPos are empty/nil until a successful AuthRequest
//     and are NOT populated by a failed auth (the session closes).
//
// The helpers (newTestSession, sendHello, inWorld, mockTransport) live in
// session_test.go and are shared because both files are in package
// session.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// assertSpawn compares two Vec3 pointers by value (nil-aware) instead of
// by identity, so the accessor could legally copy and still pass.
func assertSpawn(t *testing.T, got, want *mmov1.Vec3) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("SpawnPos() = %v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Errorf("SpawnPos() = nil, want (%v, %v, %v)", want.X, want.Y, want.Z)
		return
	}
	if got.X != want.X || got.Y != want.Y || got.Z != want.Z {
		t.Errorf("SpawnPos() = (%v, %v, %v), want (%v, %v, %v)", got.X, got.Y, got.Z, want.X, want.Y, want.Z)
	}
}

func TestSessionAccessors(t *testing.T) {
	// wantVer: the newTestSession config supports [1,9] and the client
	// negotiates testVersion (7); before negotiation WireVersion falls
	// back to MaxProtoVer (9).
	//
	// SpawnPos (the auth spawn) is no longer populated after auth (design
	// D4): spawn is resolved by SelectCharacter and exposed via
	// CharacterSpawn. ActiveCharacterID/CharacterSpawn are empty/nil until
	// a successful SelectCharacter.
	cases := []struct {
		name          string
		setup         func(t *testing.T, s *Session, reg *protocol.Registry)
		wantVer       uint16
		wantPlayer    string
		wantSpawn     *mmov1.Vec3
		wantActiveID  string
		wantCharSpawn *mmov1.Vec3
	}{
		{
			name:       "fresh session",
			setup:      func(t *testing.T, s *Session, reg *protocol.Registry) {},
			wantVer:    9,
			wantPlayer: "",
			wantSpawn:  nil,
		},
		{
			name:       "after hello negotiated",
			setup:      sendHello,
			wantVer:    testVersion,
			wantPlayer: "",
			wantSpawn:  nil,
		},
		{
			name:          "in-world after auth + select",
			setup:         inWorld,
			wantVer:       testVersion,
			wantPlayer:    "p1",
			wantSpawn:     nil,
			wantActiveID:  testCharID,
			wantCharSpawn: &mmov1.Vec3{X: 1.5, Y: 0, Z: -2.5},
		},
		{
			name: "failed auth leaves accessors empty",
			setup: func(t *testing.T, s *Session, reg *protocol.Registry) {
				sendHello(t, s, reg)
				err := s.HandleTCP(clientFrame(t, reg, &mmov1.AuthRequest{Username: "alice", Password: "wrong"}, 0, 0))
				if err == nil {
					t.Fatalf("auth with wrong password must fail")
				}
			},
			wantVer:    testVersion,
			wantPlayer: "",
			wantSpawn:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newTestSession(t, nil)
			reg := protocol.NewWorldRegistry()
			tc.setup(t, s, reg)

			if got := s.WireVersion(); got != tc.wantVer {
				t.Errorf("WireVersion() = %d, want %d", got, tc.wantVer)
			}
			if got := s.PlayerID(); got != tc.wantPlayer {
				t.Errorf("PlayerID() = %q, want %q", got, tc.wantPlayer)
			}
			assertSpawn(t, s.SpawnPos(), tc.wantSpawn)
			if got := s.ActiveCharacterID(); got != tc.wantActiveID {
				t.Errorf("ActiveCharacterID() = %q, want %q", got, tc.wantActiveID)
			}
			assertSpawn(t, s.CharacterSpawn(), tc.wantCharSpawn)
		})
	}
}
