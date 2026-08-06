package game

// Server-authoritative kinematic movement (spec R15/R19, design D3).
//
// The v1 movement model is kinematic: an entity's velocity is set by
// validated MoveInputs (speed clamped to the server limit), and each
// fixed tick advances position by velocity * dt. No collision, no
// navmesh, no client-side prediction — the next snapshot carries the
// authoritative position and overrides any client guess (S19.3).

import (
	"math"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// DefaultMaxSpeed is the v1 speed limit in world units per second
// (tunable; a SimulationConfig.MaxSpeed of zero selects it).
const DefaultMaxSpeed float32 = 10

// Vec2 is the internal ground-plane vector (x, z) — the sim's own type
// so internals never leak the wire shape (contract-vs-impl separation,
// design D3).
type Vec2 struct {
	X float32
	Z float32
}

// Entity is the server-side authoritative state of one player. Yaw is
// in radians (v1 sends raw radians; future delta-encoding must handle
// wrap-around at 0/2π).
type Entity struct {
	ID       string
	Pos      Vec2
	Velocity Vec2
	Yaw      float32

	// LastInputTick/LastInputSeq tag the most recent MoveInput to the
	// tick that processed it (spec R15) — the hook for future
	// correction/ack bookkeeping.
	LastInputTick uint64
	LastInputSeq  int32
}

// ClampSpeed validates a client-requested speed against the server
// limit (spec S15.2): speeds above the limit are clamped, negative
// speeds are zeroed, and the second return reports whether the value
// was altered — the authoritative position never follows an unclamped
// input.
func ClampSpeed(speed, max float32) (float32, bool) {
	switch {
	case speed < 0:
		return 0, true
	case speed > max:
		return max, true
	default:
		return speed, false
	}
}

// VelocityFromInput derives the authoritative velocity from a client
// MoveInput: the direction is normalized and scaled by the clamped
// speed. A zero direction always yields zero velocity (no movement),
// even with a nonzero speed.
func VelocityFromInput(dir Vec2, speed, max float32) (Vec2, bool) {
	clamped, altered := ClampSpeed(speed, max)
	if clamped == 0 {
		return Vec2{}, altered
	}
	mag := float32(math.Sqrt(float64(dir.X*dir.X + dir.Z*dir.Z)))
	if mag == 0 {
		return Vec2{}, altered
	}
	return Vec2{
		X: dir.X / mag * clamped,
		Z: dir.Z / mag * clamped,
	}, altered
}

// SetVelocityFromInput applies one MoveInput to the entity: it computes
// the clamped velocity and sets the yaw (design D3). It returns whether
// the input's speed had to be clamped. The entity's position is NOT
// advanced here — the tick integrates once with the final velocity.
func SetVelocityFromInput(e *Entity, in *mmov1.MoveInput, max float32) bool {
	var dir Vec2
	if in.Dir != nil {
		dir = Vec2{X: in.Dir.X, Z: in.Dir.Z}
	}
	return applyMove(e, dir, in.Speed, in.Yaw, max)
}

// applyMove is the allocation-free core of SetVelocityFromInput, used
// by the tick loop's input drain.
func applyMove(e *Entity, dir Vec2, speed, yaw, max float32) bool {
	vel, clamped := VelocityFromInput(dir, speed, max)
	e.Velocity = vel
	e.Yaw = yaw
	return clamped
}

// Integrate advances the entity's position by one fixed step:
// pos += velocity * dt (spec S15.1). dt is the sim's fixed tick delta.
func Integrate(e *Entity, dt float32) {
	e.Pos.X += e.Velocity.X * dt
	e.Pos.Z += e.Velocity.Z * dt
}
