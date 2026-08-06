package server

// Pure helpers shared by the wiring paths.

import (
	"bytes"

	"github.com/luisplata/mmo-api-server/internal/game"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// vecFromProto converts a wire Vec2 to the sim's internal Vec2 (design
// D3 — internals never leak the wire shape).
func vecFromProto(v *mmov1.Vec2) game.Vec2 {
	if v == nil {
		return game.Vec2{}
	}
	return game.Vec2{X: v.X, Z: v.Z}
}

// isBindToken reports whether a UDP datagram payload IS the bind token
// (spec R13): the first packet after auth binds the session, and the
// bind datagram itself must not be dispatched as an input.
func isBindToken(payload, token []byte) bool {
	if len(token) == 0 {
		return false
	}
	return bytes.Equal(payload, token)
}

// entityStateFromGame maps the sim's internal entity to its wire state —
// a mirror of internal/game's unexported entityState, needed here for
// the interest fanout's SpawnEntity.State. Ground-plane position,
// velocity and yaw only (design D3, spec S16.1).
func entityStateFromGame(e *game.Entity) *mmov1.EntityState {
	return &mmov1.EntityState{
		Id:       e.ID,
		Pos:      &mmov1.Vec2{X: e.Pos.X, Z: e.Pos.Z},
		Velocity: &mmov1.Vec2{X: e.Velocity.X, Z: e.Velocity.Z},
		Yaw:      e.Yaw,
	}
}
