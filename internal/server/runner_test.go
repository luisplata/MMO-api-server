package server

// P6 room-runner tests (design: RoomManager = control plane, roomRunner =
// runtime). They pin the slice's three behaviors: one stepping mechanism
// advances every room (no per-room ticker), ops are addressed by roomID,
// and an emptied non-default room is destroyed while the default (legacy)
// room survives. All deterministic — no time.Sleep.

import (
	"fmt"
	"testing"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/stats"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

type discardSink struct{}

func (discardSink) SendSnapshot(string, *mmov1.Snapshot) error { return nil }

func newTestRunner(t *testing.T) *roomRunner {
	t.Helper()
	def, err := game.NewSimulation(game.SimulationConfig{Sink: discardSink{}})
	if err != nil {
		t.Fatalf("NewSimulation: %v", err)
	}
	return newRoomRunner(def, discardSink{}, nil)
}

// TestRoomRunnerStepsAllRoomsOnSingleTicker pins 6.1: a single stepAll — the
// one call the room-owner loop makes per 20 Hz wake — advances every one of
// 50 rooms by exactly one tick. A per-room ticker would need one call per
// room.
func TestRoomRunnerStepsAllRoomsOnSingleTicker(t *testing.T) {
	runner := newTestRunner(t)

	const n = 50
	for i := 0; i < n; i++ {
		if _, err := runner.ensureRoom(rooms.RoomID(fmt.Sprintf("room-%d", i))); err != nil {
			t.Fatalf("ensureRoom(%d): %v", i, err)
		}
	}
	if got := runner.roomCount(); got != n+1 {
		t.Fatalf("roomCount = %d, want %d (50 rooms + default)", got, n+1)
	}

	if err := runner.stepAll(); err != nil {
		t.Fatalf("stepAll: %v", err)
	}

	for i := 0; i < n; i++ {
		id := rooms.RoomID(fmt.Sprintf("room-%d", i))
		rs := runner.room(id)
		if rs == nil {
			t.Fatalf("room %s missing after stepAll", id)
		}
		if got := rs.sim.Tick(); got != 1 {
			t.Fatalf("room %s Tick = %d, want 1 (stepped by the single ticker)", id, got)
		}
	}
}

// TestRoomRunnerDestroysEmptyRoom pins 6.3: a non-default room whose last
// member leaves is destroyed, a failed apply never mutates membership, and
// an op on an unknown room fails cleanly.
func TestRoomRunnerDestroysEmptyRoom(t *testing.T) {
	runner := newTestRunner(t)
	if _, err := runner.ensureRoom("room-a"); err != nil {
		t.Fatalf("ensureRoom: %v", err)
	}

	join := roomOp{
		roomID: "room-a", delta: +1, done: make(chan error, 1),
		apply: func(sim *game.Simulation) error {
			return sim.RegisterPlayer("p1", game.Vec2{}, "", stats.Stats{})
		},
	}
	runner.apply(join)
	if err := <-join.done; err != nil {
		t.Fatalf("join: %v", err)
	}
	if runner.room("room-a") == nil {
		t.Fatal("room-a must survive a successful join")
	}

	// A failed duplicate join must not bump membership (delta not applied).
	dup := roomOp{
		roomID: "room-a", delta: +1, done: make(chan error, 1),
		apply: func(sim *game.Simulation) error {
			return sim.RegisterPlayer("p1", game.Vec2{}, "", stats.Stats{})
		},
	}
	runner.apply(dup)
	if err := <-dup.done; err == nil {
		t.Fatal("duplicate register must fail")
	}
	if runner.room("room-a") == nil {
		t.Fatal("room-a must survive a failed duplicate join")
	}

	leave := roomOp{
		roomID: "room-a", delta: -1, done: make(chan error, 1),
		apply: func(sim *game.Simulation) error { return sim.RemovePlayer("p1") },
	}
	runner.apply(leave)
	if err := <-leave.done; err != nil {
		t.Fatalf("leave: %v", err)
	}
	if runner.room("room-a") != nil {
		t.Fatal("room-a must be destroyed after its last player left")
	}

	unknown := roomOp{
		roomID: "missing", done: make(chan error, 1),
		apply: func(*game.Simulation) error { return nil },
	}
	runner.apply(unknown)
	if err := <-unknown.done; err == nil {
		t.Fatal("op on an unknown room must fail")
	}
}

// TestRoomRunnerPreservesDefaultRoom pins the legacy-world boundary: the
// default room exists from construction and survives a join/leave cycle,
// never counting as empty for destruction.
func TestRoomRunnerPreservesDefaultRoom(t *testing.T) {
	runner := newTestRunner(t)

	if runner.room(DefaultRoom) == nil {
		t.Fatal("default room must exist from construction")
	}

	// A leave on an empty default room (unknown player) must not destroy it.
	leaveEmpty := roomOp{
		roomID: DefaultRoom, delta: -1, done: make(chan error, 1),
		apply: func(sim *game.Simulation) error { return sim.RemovePlayer("nobody") },
	}
	runner.apply(leaveEmpty)
	<-leaveEmpty.done // ErrUnknownPlayer is expected and ignored
	if runner.room(DefaultRoom) == nil {
		t.Fatal("default room must never be destroyed")
	}

	join := roomOp{
		roomID: DefaultRoom, delta: +1, done: make(chan error, 1),
		apply: func(sim *game.Simulation) error {
			return sim.RegisterPlayer("p1", game.Vec2{}, "", stats.Stats{})
		},
	}
	runner.apply(join)
	if err := <-join.done; err != nil {
		t.Fatalf("join: %v", err)
	}
	leave := roomOp{
		roomID: DefaultRoom, delta: -1, done: make(chan error, 1),
		apply: func(sim *game.Simulation) error { return sim.RemovePlayer("p1") },
	}
	runner.apply(leave)
	if err := <-leave.done; err != nil {
		t.Fatalf("leave: %v", err)
	}

	if runner.room(DefaultRoom) == nil {
		t.Fatal("default room must survive emptying")
	}
	if got := runner.roomCount(); got != 1 {
		t.Fatalf("roomCount = %d, want 1 (only the default room)", got)
	}
}

// TestRoomRunnerMoveIdleIsolation pins P23 room isolation: the stale-input
// deadline lives inside each room's own simulation. Expiring room A's input
// on the shared ticker must never zero room B's renewed velocity.
func TestRoomRunnerMoveIdleIsolation(t *testing.T) {
	runner := newTestRunner(t)
	a, err := runner.ensureRoom("room-a")
	if err != nil {
		t.Fatalf("ensureRoom(room-a): %v", err)
	}
	b, err := runner.ensureRoom("room-b")
	if err != nil {
		t.Fatalf("ensureRoom(room-b): %v", err)
	}
	if err := a.sim.RegisterPlayer("a1", game.Vec2{}, "", stats.Stats{}); err != nil {
		t.Fatalf("register a1: %v", err)
	}
	if err := b.sim.RegisterPlayer("b1", game.Vec2{}, "", stats.Stats{}); err != nil {
		t.Fatalf("register b1: %v", err)
	}
	input := func(seq int32) *mmov1.MoveInput {
		return &mmov1.MoveInput{Seq: seq, Dir: &mmov1.Vec2{X: 1}, Speed: 5}
	}
	a.sim.QueueInput("a1", input(1))
	b.sim.QueueInput("b1", input(2))

	for i := 0; i < 5; i++ { // ticks 0..4
		if err := runner.stepAll(); err != nil {
			t.Fatalf("stepAll: %v", err)
		}
	}
	b.sim.QueueInput("b1", input(3)) // tick 5 renewal in room-b only
	for i := 0; i < 6; i++ {         // ticks 5..10
		if err := runner.stepAll(); err != nil {
			t.Fatalf("stepAll: %v", err)
		}
	}

	a1, _ := a.sim.Entity("a1")
	b1, _ := b.sim.Entity("b1")
	dt := float32(game.TickInterval.Seconds())
	if !almostEqual(a1.Velocity.X, 0) || !almostEqual(a1.Velocity.Z, 0) {
		t.Errorf("room-a a1 velocity = %v, want zero after expiry", a1.Velocity)
	}
	if !almostEqual(a1.Pos.X, float32(game.InputStopAfterTicks)*5*dt) {
		t.Errorf("room-a a1 pos.x = %v, want %v", a1.Pos.X, float32(game.InputStopAfterTicks)*5*dt)
	}
	if !almostEqual(b1.Velocity.X, 5) || !almostEqual(b1.Velocity.Z, 0) {
		t.Errorf("room-b b1 velocity = %v, want (5, 0) [room-a expiry must not cross rooms]", b1.Velocity)
	}
	if !almostEqual(b1.Pos.X, float32(game.InputStopAfterTicks+1)*5*dt) {
		t.Errorf("room-b b1 pos.x = %v, want %v", b1.Pos.X, float32(game.InputStopAfterTicks+1)*5*dt)
	}
}
