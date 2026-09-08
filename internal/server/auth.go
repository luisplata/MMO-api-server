package server

// v1 dev-mode authentication (spec R12, design PR4b point 3, D4): the
// seam is the deliverable — a real backend slots in later without
// touching the session layer. Enabled accepts any credentials and
// returns the username as the ACCOUNT id; disabled rejects everything as
// a placeholder. Auth resolves only the account identity: the spawn
// position is NOT resolved here — it is resolved by SelectCharacter in
// the selecting phase (design D4), so Authenticate returns a nil spawn.

import (
	"errors"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// devAuthenticator is the v1 development authenticator.
type devAuthenticator struct {
	enabled bool
}

// Authenticate implements session.Authenticator. It returns the account
// id (the username) for valid credentials and a NIL spawn — design D4
// moved spawn resolution to SelectCharacter, so the session never
// consumes a spawn from auth.
func (d devAuthenticator) Authenticate(username, password string) (string, *mmov1.Vec3, error) {
	if !d.enabled {
		return "", nil, errors.New("server: authentication disabled (real auth pending)")
	}
	if username == "" {
		return "", nil, errors.New("server: empty username")
	}
	return username, nil, nil
}
