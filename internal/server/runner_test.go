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
