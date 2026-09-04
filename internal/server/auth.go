package server

// v1 dev-mode authentication (spec R12, design PR4b point 3): the seam
// is the deliverable — a real backend slots in later without touching
// the session layer. Enabled accepts any credentials and returns the
// username as the player id with a fixed spawn; disabled rejects
// everything as a placeholder.

import (
	"errors"

	"github.com/luisplata/mmo-api-server/internal/game"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// devAuthenticator is the v1 development authenticator.
type devAuthenticator struct {
	enabled bool
	spawn   game.Vec2
}

// Authenticate implements session.Authenticator. The returned spawn is
// a v2 Vec3; the Y component is the terrain height at the spawn point,
// resolved from the active map (Slice B wiring) — before that lands it
// is 0.
func (d devAuthenticator) Authenticate(username, password string) (string, *mmov1.Vec3, error) {
	if !d.enabled {
		return "", nil, errors.New("server: authentication disabled (real auth pending)")
	}
	if username == "" {
		return "", nil, errors.New("server: empty username")
	}
	return username, &mmov1.Vec3{X: d.spawn.X, Y: 0, Z: d.spawn.Z}, nil
}
