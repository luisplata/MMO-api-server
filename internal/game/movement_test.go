package game

// Movement model tests (spec R15/R19, S15.1/S15.2/S19.3).
//
// Coverage: speed validation/clamping (over-speed rejected/clamped,
// negative speeds zeroed), velocity from input (direction normalized,
// zero direction yields zero velocity), kinematic integration
// (pos += vel*dt), and the authoritative application of a MoveInput.

import (
	"math"
	"testing"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func almostEqual(a, b float32) bool {
	return math.Abs(float64(a-b)) <= 1e-4
}

func almostVec(a, b Vec2) bool {
	return almostEqual(a.X, b.X) && almostEqual(a.Z, b.Z)
}

func TestClampSpeed(t *testing.T) {
	tests := []struct {
		name       string
		speed, max float32
		want       float32
		wantClamp  bool
	}{
		{"under limit", 5, 10, 5, false},
		{"at limit", 10, 10, 10, false},
		{"over limit clamps", 25, 10, 10, true},
		{"negative zeroed", -3, 10, 0, true},
		{"zero is fine", 0, 10, 0, false},
		{"custom limit under", 7, 8, 7, false},
		{"custom limit over", 9, 8, 8, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := ClampSpeed(tc.speed, tc.max)
			if clamped != tc.wantClamp || !almostEqual(got, tc.want) {
				t.Errorf("ClampSpeed(%v, %v) = (%v, %v), want (%v, %v)",
					tc.speed, tc.max, got, clamped, tc.want, tc.wantClamp)
			}
		})
	}
}

func TestVelocityFromInput(t *testing.T) {
	tests := []struct {
		name  string
		dir   Vec2
		speed float32
		max   float32
		want  Vec2
		clamp bool
	}{
		{"simple east", Vec2{1, 0}, 5, 10, Vec2{5, 0}, false},
		{"normalized diagonal", Vec2{3, 4}, 10, 10, Vec2{6, 8}, false},
		{"zero dir no velocity", Vec2{0, 0}, 10, 10, Vec2{0, 0}, false},
		{"over speed clamped", Vec2{1, 0}, 999, 10, Vec2{10, 0}, true},
		{"negative speed zeroed", Vec2{1, 0}, -5, 10, Vec2{0, 0}, true},
		{"west direction", Vec2{-2, 0}, 5, 10, Vec2{-5, 0}, false},
		{"unit diagonal", Vec2{1, 1}, 7.071068, 10, Vec2{5, 5}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := VelocityFromInput(tc.dir, tc.speed, tc.max)
			if clamped != tc.clamp || !almostVec(got, tc.want) {
				t.Errorf("VelocityFromInput(%v, %v, %v) = (%v, %v), want (%v, %v)",
					tc.dir, tc.speed, tc.max, got, clamped, tc.want, tc.clamp)
			}
		})
	}
}

func TestIntegrate(t *testing.T) {
	tests := []struct {
		name     string
		pos, vel Vec2
		dt       float32
		wantPos  Vec2
	}{
		{"advance by velocity", Vec2{0, 0}, Vec2{5, 0}, 0.05, Vec2{0.25, 0}},
		{"negative axis", Vec2{10, -2}, Vec2{0, -8}, 0.1, Vec2{10, -2.8}},
		{"zero velocity no move", Vec2{3, 4}, Vec2{0, 0}, 0.5, Vec2{3, 4}},
		{"larger dt", Vec2{0, 0}, Vec2{2, 1}, 1, Vec2{2, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Entity{Pos: tc.pos, Velocity: tc.vel}
			Integrate(e, tc.dt)
			if !almostVec(e.Pos, tc.wantPos) {
				t.Errorf("Integrate pos = %v, want %v", e.Pos, tc.wantPos)
			}
		})
	}
}

func TestSetVelocityFromInput(t *testing.T) {
	e := &Entity{Pos: Vec2{0, 0}}
	in := &mmov1.MoveInput{
		Seq:   7,
		Dir:   &mmov1.Vec2{X: 1, Z: 0},
		Speed: 5,
		Yaw:   1.2,
	}
	clamped := SetVelocityFromInput(e, in, 10)
	if clamped {
		t.Errorf("SetVelocityFromInput clamped = true, want false for speed 5")
	}
	if !almostVec(e.Velocity, Vec2{5, 0}) {
		t.Errorf("velocity = %v, want (5, 0)", e.Velocity)
	}
	if !almostEqual(e.Yaw, 1.2) {
		t.Errorf("yaw = %v, want 1.2", e.Yaw)
	}

	// Over-speed input must clamp the velocity but keep the direction.
	e2 := &Entity{}
	in2 := &mmov1.MoveInput{Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 999, Yaw: 0}
	clamped = SetVelocityFromInput(e2, in2, 10)
	if !clamped {
		t.Errorf("SetVelocityFromInput clamped = false, want true for speed 999")
	}
	if !almostVec(e2.Velocity, Vec2{10, 0}) {
		t.Errorf("clamped velocity = %v, want (10, 0)", e2.Velocity)
	}
}

// TestDefaultMaxSpeedIsTunable proves the speed limit constant exists
// and is a sane default for the simulation config.
func TestDefaultMaxSpeedIsTunable(t *testing.T) {
	if DefaultMaxSpeed <= 0 {
		t.Fatalf("DefaultMaxSpeed = %v, want a positive tunable limit", DefaultMaxSpeed)
	}
}
