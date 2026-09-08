package game

// Tick loop and simulation tests (spec R14–R17, S14.1/S14.2/S15.1/
// S15.2/S16.1/S17.1/S19.3).
//
// Coverage: the fixed-dt accumulator (no time.Sleep, no drift), the
// loop's wall-clock cadence (20 ticks per simulated second), tick
// determinism (identical input sequence -> identical states), movement
// integration and over-speed clamping through the full sim, snapshot
// cadence + per-player stagger + monotonic seq, the authoritative
// correction rule, and the interest/event plumbing.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/stats"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"

	"github.com/luisplata/mmo-api-server/internal/world"
)

// recordingSink captures every snapshot delivery for assertions.
type recordingSink struct {
	mu    sync.Mutex
	snaps []sentSnapshot
	err   error
}

type sentSnapshot struct {
	playerID string
	snap     *mmov1.Snapshot
}

func (r *recordingSink) SendSnapshot(playerID string, snap *mmov1.Snapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.snaps = append(r.snaps, sentSnapshot{playerID: playerID, snap: snap})
	return nil
}

func (r *recordingSink) deliveries() []sentSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sentSnapshot(nil), r.snaps...)
}

func newSim(t *testing.T, mut func(*SimulationConfig)) *Simulation {
	t.Helper()
	cfg := SimulationConfig{Sink: &recordingSink{}}
	if mut != nil {
		mut(&cfg)
	}
	sim, err := NewSimulation(cfg)
	if err != nil {
		t.Fatalf("NewSimulation: %v", err)
	}
	return sim
}

func move(dirX, dirZ, speed float32, seq int32) *mmov1.MoveInput {
	return &mmov1.MoveInput{Seq: seq, Dir: &mmov1.Vec2{X: dirX, Z: dirZ}, Speed: speed, Yaw: 0}
}

func TestAccumulator(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		steps   int
	}{
		{"nothing yet", 0, 0},
		{"partial tick waits", 40 * time.Millisecond, 0},
		{"accumulated to a full tick", 10 * time.Millisecond, 1},
		{"burst of two", 120 * time.Millisecond, 2},
		{"carried remainder completes", 30 * time.Millisecond, 1},
		{"negative elapsed never runs", -10 * time.Millisecond, 0},
	}
	a := NewAccumulator(TickInterval)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.StepCount(tc.elapsed); got != tc.steps {
				t.Errorf("StepCount(%v) = %d, want %d", tc.elapsed, got, tc.steps)
			}
		})
	}
	// Fresh accumulator: any 1 second of elapsed time yields exactly
	// TickRate steps, regardless of the wake pattern (drift-free).
	for _, pattern := range []struct {
		name  string
		burst time.Duration
		n     int
	}{
		{"uniform 50ms wakes", 50 * time.Millisecond, 20},
		{"quarter-second wakes", 250 * time.Millisecond, 4},
		{"40ms wakes carry remainder", 40 * time.Millisecond, 25},
	} {
		t.Run(pattern.name, func(t *testing.T) {
			acc := NewAccumulator(TickInterval)
			total := 0
			for i := 0; i < pattern.n; i++ {
				total += acc.StepCount(pattern.burst)
			}
			if total != TickRate {
				t.Errorf("%s: %d steps over 1s, want %d", pattern.name, total, TickRate)
			}
		})
	}
}

// TestSimulationCadence drives the loop with an injected wake channel:
// 1 simulated second must advance exactly TickRate ticks (S14.1), with
// no time.Sleep anywhere. The first wake primes the baseline; the
// unbuffered channel makes every send block until the loop consumes it,
// and closing it gives a deterministic completion barrier.
func TestSimulationCadence(t *testing.T) {
	sim := newSim(t, nil)
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- sim.Run(ctx, wake) }()

	// Second 1: prime + 20 intervals of 50 ms -> 20 ticks.
	tick := time.UnixMilli(1_700_000_000_000)
	wake <- tick
	for i := 0; i < 20; i++ {
		tick = tick.Add(TickInterval)
		wake <- tick
	}
	// Second 2: 4 burst intervals of 250 ms -> 5 ticks each -> 20 ticks.
	for i := 0; i < 4; i++ {
		tick = tick.Add(250 * time.Millisecond)
		wake <- tick
	}
	close(wake)
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := sim.Tick(); got != TickRate*2 {
		t.Errorf("after 2 simulated seconds tick = %d, want %d (20 Hz, S14.1)", got, TickRate*2)
	}
}

// TestSimulationRunStopsOnCancel proves the loop exits cleanly on
// context cancellation (graceful shutdown path for the wiring layer).
func TestSimulationRunStopsOnCancel(t *testing.T) {
	sim := newSim(t, nil)
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})
	wake := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- sim.Run(ctx, wake) }()

	base := time.UnixMilli(1_700_000_000_000)
	wake <- base                   // prime
	wake <- base.Add(TickInterval) // one interval -> one tick
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
	if got := sim.Tick(); got != 1 {
		t.Errorf("tick = %d, want 1 (the single interval processed before cancel)", got)
	}
}

// TestSimulationDeterminism runs two identical simulations with the
// same fixed input sequence and asserts byte-identical entity states
// (S14.2).
func TestSimulationDeterminism(t *testing.T) {
	run := func() *Simulation {
		sim := newSim(t, nil)
		if err := sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer p1: %v", err)
		}
		if err := sim.RegisterPlayer("p2", Vec2{10, -10}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer p2: %v", err)
		}
		inputs := []*mmov1.MoveInput{
			move(1, 0, 5, 1),
			move(1, 1, 999, 2), // over-speed: must clamp identically
			move(0, -1, 3, 3),
			move(-1, 0, 7, 4),
			move(0, 0, 0, 5),
		}
		for i := 0; i < 40; i++ {
			if err := sim.QueueInput("p1", inputs[i%len(inputs)]); err != nil {
				t.Fatalf("QueueInput: %v", err)
			}
			if err := sim.Step(); err != nil {
				t.Fatalf("Step: %v", err)
			}
		}
		return sim
	}
	a, b := run(), run()
	for _, id := range []string{"p1", "p2"} {
		ea, _ := a.Entity(id)
		eb, _ := b.Entity(id)
		if ea.Pos != eb.Pos || ea.Velocity != eb.Velocity || ea.Yaw != eb.Yaw {
			t.Errorf("determinism broke for %s:\n  a: pos=%v vel=%v yaw=%v\n  b: pos=%v vel=%v yaw=%v",
				id, ea.Pos, eb.Pos, ea.Yaw, eb.Pos, eb.Velocity, eb.Yaw)
		}
	}
}

func TestSimulationMoveApplied(t *testing.T) {
	sim := newSim(t, nil)
	if err := sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	if err := sim.QueueInput("p1", move(1, 0, 5, 1)); err != nil {
		t.Fatal(err)
	}
	if err := sim.Step(); err != nil { // tick 0: vel (5,0), pos += 5*0.05
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if !almostVec(e.Pos, Vec2{0.25, 0}) {
		t.Errorf("after one tick pos = %v, want (0.25, 0) = vel(5) * dt(0.05) [S15.1]", e.Pos)
	}
	// Kinematic persistence: no new input -> continues at vel (5,0).
	if err := sim.Step(); err != nil {
		t.Fatal(err)
	}
	if !almostVec(e.Pos, Vec2{0.5, 0}) {
		t.Errorf("after two ticks pos = %v, want (0.5, 0)", e.Pos)
	}
	// A zero input stops the entity.
	if err := sim.QueueInput("p1", move(0, 0, 0, 2)); err != nil {
		t.Fatal(err)
	}
	if err := sim.Step(); err != nil {
		t.Fatal(err)
	}
	if !almostVec(e.Velocity, Vec2{0, 0}) || !almostVec(e.Pos, Vec2{0.5, 0}) {
		t.Errorf("after stop pos = %v vel = %v, want (0.5, 0)/(0, 0)", e.Pos, e.Velocity)
	}
}

func TestSimulationOverSpeedClamped(t *testing.T) {
	sim := newSim(t, nil) // DefaultMaxSpeed = 10
	if err := sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	if err := sim.QueueInput("p1", move(1, 0, 999, 1)); err != nil {
		t.Fatal(err)
	}
	if err := sim.Step(); err != nil {
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if !almostVec(e.Pos, Vec2{0.5, 0}) {
		t.Errorf("over-speed pos = %v, want (0.5, 0) = maxSpeed(10) * dt [S15.2]", e.Pos)
	}
	if !almostVec(e.Velocity, Vec2{10, 0}) {
		t.Errorf("over-speed velocity = %v, want (10, 0) clamped", e.Velocity)
	}
}

func TestSimulationCustomMaxSpeed(t *testing.T) {
	sim := newSim(t, func(c *SimulationConfig) { c.MaxSpeed = 5 })
	if err := sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	if err := sim.QueueInput("p1", move(1, 0, 999, 1)); err != nil {
		t.Fatal(err)
	}
	if err := sim.Step(); err != nil {
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if !almostVec(e.Pos, Vec2{0.25, 0}) {
		t.Errorf("custom-limit pos = %v, want (0.25, 0) = maxSpeed(5) * dt", e.Pos)
	}
}

// TestSimulationSnapshotStagger verifies S17.1 end to end: two players
// over 40 ticks each receive 20 snapshots (~10 Hz), their streams are
// staggered (never the same tick), and every emission carries a
// strictly monotonic server seq.
func TestSimulationSnapshotStagger(t *testing.T) {
	sink := &recordingSink{}
	sim := newSim(t, func(c *SimulationConfig) { c.Sink = sink })
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})
	sim.RegisterPlayer("p2", Vec2{10, 0}, "", stats.Stats{})

	for i := 0; i < 40; i++ {
		if err := sim.Step(); err != nil {
			t.Fatal(err)
		}
	}
	del := sink.deliveries()
	if len(del) != 40 {
		t.Fatalf("total snapshot deliveries = %d, want 40 (2 players x 20)", len(del))
	}

	// The two streams must strictly alternate (staggered), starting
	// with p1 on tick 0.
	wantOrder := make([]string, 40)
	for i := range wantOrder {
		if i%2 == 0 {
			wantOrder[i] = "p1"
		} else {
			wantOrder[i] = "p2"
		}
	}
	counts := map[string]int{}
	var prevSeq int32
	for i, d := range del {
		if d.playerID != wantOrder[i] {
			t.Errorf("delivery %d to %q, want %q (staggered streams)", i, d.playerID, wantOrder[i])
		}
		counts[d.playerID]++
		if d.snap.Seq <= prevSeq {
			t.Errorf("server seq not monotonic: %d after %d", d.snap.Seq, prevSeq)
		}
		prevSeq = d.snap.Seq
	}
	if counts["p1"] != 20 || counts["p2"] != 20 {
		t.Errorf("per-player snapshot counts = %v, want p1:20 p2:20 (~10 Hz each)", counts)
	}
}

// TestSimulationSnapshotCarriesFullState verifies S16.1 through the
// whole sim: every received snapshot entity carries x, z, velocity and
// yaw — nothing less.
func TestSimulationSnapshotCarriesFullState(t *testing.T) {
	sink := &recordingSink{}
	sim := newSim(t, func(c *SimulationConfig) { c.Sink = sink })
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})
	sim.RegisterPlayer("p2", Vec2{10, -2}, "", stats.Stats{})
	sim.QueueInput("p1", move(1, 0, 5, 1))

	for i := 0; i < 4; i++ {
		if err := sim.Step(); err != nil {
			t.Fatal(err)
		}
	}
	del := sink.deliveries()
	if len(del) == 0 {
		t.Fatal("no snapshots delivered")
	}
	snap := del[0].snap
	if len(snap.Entities) != 2 {
		t.Fatalf("snapshot has %d entities, want 2 (full state)", len(snap.Entities))
	}
	for _, es := range snap.Entities {
		if es.Pos == nil || es.Velocity == nil {
			t.Errorf("entity %s missing pos/velocity", es.Id)
		}
		if !almostEqual(es.Yaw, 0) {
			t.Errorf("entity %s yaw = %v, want 0 (present)", es.Id, es.Yaw)
		}
	}
}

// TestSimulationServerCorrection verifies S19.3: the next snapshot's
// position is the server-integrated one (input speed clamped, direction
// normalized) — the client's raw intent is never reflected.
func TestSimulationServerCorrection(t *testing.T) {
	sink := &recordingSink{}
	sim := newSim(t, func(c *SimulationConfig) { c.Sink = sink })
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})
	// Client sends a non-normalized direction with absurd speed: the
	// server must clamp to max speed and normalize the direction.
	sim.QueueInput("p1", &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 30, Z: 40}, Speed: 999, Yaw: 0.7})

	for i := 0; i < 2; i++ {
		if err := sim.Step(); err != nil {
			t.Fatal(err)
		}
	}
	del := sink.deliveries()
	if len(del) == 0 {
		t.Fatal("no snapshots delivered")
	}
	es := del[0].snap.Entities[0]
	// vel = normalize(30,40)*10 = (6,8); pos after 1 tick = (0.3, 0.4).
	if !almostEqual(es.Pos.X, 0.3) || !almostEqual(es.Pos.Z, 0.4) {
		t.Errorf("authoritative pos = (%v, %v), want (0.3, 0.4) [S19.3]", es.Pos.X, es.Pos.Z)
	}
	if !almostEqual(es.Yaw, 0.7) {
		t.Errorf("authoritative yaw = %v, want 0.7", es.Yaw)
	}
}

func TestSimulationInterestEvents(t *testing.T) {
	sim := newSim(t, nil)
	if events := sim.TakeInterestEvents(); len(events) != 0 {
		t.Fatalf("fresh sim events = %v, want none", events)
	}
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})
	if events := sim.TakeInterestEvents(); len(events) != 0 {
		t.Fatalf("first join events = %v, want none", events)
	}
	// p2 joins -> p1 must spawn p2 (v1 flat).
	sim.RegisterPlayer("p2", Vec2{10, 10}, "", stats.Stats{})
	events := sim.TakeInterestEvents()
	if len(events) != 1 {
		t.Fatalf("join events = %v, want 1 spawn", events)
	}
	ev := events[0]
	if ev.Subject != "p2" || len(ev.Targets) != 1 || ev.Targets[0] != "p1" {
		t.Errorf("join event = %+v, want spawn p2 -> [p1]", ev)
	}
	// Flat: movement across cells emits nothing.
	sim.QueueInput("p2", move(1, 0, 999, 1))
	for i := 0; i < 40; i++ {
		sim.Step()
	}
	if events := sim.TakeInterestEvents(); len(events) != 0 {
		t.Fatalf("flat movement events = %v, want none", events)
	}
	// p1 leaves -> p2 must despawn p1.
	sim.RemovePlayer("p1")
	events = sim.TakeInterestEvents()
	if len(events) != 1 {
		t.Fatalf("leave events = %v, want 1 despawn", events)
	}
	ev = events[0]
	if ev.Subject != "p1" || len(ev.Targets) != 1 || ev.Targets[0] != "p2" {
		t.Errorf("leave event = %+v, want despawn p1 -> [p2]", ev)
	}
	if _, ok := sim.Entity("p1"); ok {
		t.Error("p1 still registered after RemovePlayer")
	}
}

func TestSimulationQueueInputErrors(t *testing.T) {
	sim := newSim(t, nil)
	if err := sim.QueueInput("ghost", move(1, 0, 5, 1)); err == nil {
		t.Error("QueueInput for unknown player must error")
	}
	if err := sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	if err := sim.RegisterPlayer("p1", Vec2{5, 5}, "", stats.Stats{}); err == nil {
		t.Error("duplicate RegisterPlayer must error")
	}
	if err := sim.RemovePlayer("ghost"); err == nil {
		t.Error("RemovePlayer for unknown player must error")
	}
	if err := sim.RemovePlayer("p1"); err != nil {
		t.Fatalf("RemovePlayer p1: %v", err)
	}
}

func TestSimulationInputTaggedToTick(t *testing.T) {
	sim := newSim(t, nil)
	sim.RegisterPlayer("p1", Vec2{0, 0}, "", stats.Stats{})
	sim.QueueInput("p1", &mmov1.MoveInput{Seq: 5, Dir: &mmov1.Vec2{X: 1}, Speed: 5, Yaw: 0})
	if err := sim.Step(); err != nil { // processes tick 0
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if e.LastInputTick != 0 || e.LastInputSeq != 5 {
		t.Errorf("input tag = (tick %d, seq %d), want (0, 5)", e.LastInputTick, e.LastInputSeq)
	}
	if err := sim.Step(); err != nil { // tick 1: no input
		t.Fatal(err)
	}
	if e.LastInputTick != 0 {
		t.Errorf("input tag changed without a new input: tick %d", e.LastInputTick)
	}
}

func TestSimulationWorldSnapshot(t *testing.T) {
	sim := newSim(t, nil)
	sim.RegisterPlayer("p1", Vec2{1, 2}, "", stats.Stats{})
	sim.RegisterPlayer("p2", Vec2{-3, 4}, "", stats.Stats{})
	sim.QueueInput("p1", move(1, 0, 5, 1))
	for i := 0; i < 3; i++ {
		if err := sim.Step(); err != nil {
			t.Fatal(err)
		}
	}
	ws := sim.AssembleWorldSnapshot()
	if len(ws.Entities) != 2 {
		t.Fatalf("WorldSnapshot entities = %d, want 2", len(ws.Entities))
	}
	p1 := ws.Entities[0]
	if p1.Id != "p1" || !almostEqual(p1.Pos.X, 1.75) {
		t.Errorf("WorldSnapshot p1 = id %q pos.x %v, want p1 / 1.75 (moved 3 ticks at 5 u/s)", p1.Id, p1.Pos.X)
	}
}

func TestNewSimulationValidation(t *testing.T) {
	if _, err := NewSimulation(SimulationConfig{}); err == nil {
		t.Error("NewSimulation with nil Sink must error")
	}
	sim := newSim(t, nil)
	if sim.Tick() != 0 {
		t.Errorf("fresh sim tick = %d, want 0", sim.Tick())
	}
}

// TestSimulationYFollowsTerrain pins CTH-1 tick: after each Step the
// entity's Y equals the terrain height at its NEW position — derived
// from the HeightResolver after integration, never from input.
func TestSimulationYFollowsTerrain(t *testing.T) {
	heights, err := world.DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	sim := newSim(t, func(c *SimulationConfig) { c.Heights = heights })
	if err := sim.RegisterPlayer("p1", Vec2{100, 200}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if e.Y != 25 {
		t.Errorf("Y at spawn = %v, want 25 (hill peak at sample (10, 20))", e.Y)
	}
	// Move east along the hill flank: Y must track HeightAt at every
	// post-integration position.
	sim.QueueInput("p1", move(1, 0, 5, 1))
	for i := 0; i < 4; i++ {
		if err := sim.Step(); err != nil {
			t.Fatal(err)
		}
		want := heights.HeightAt(e.Pos.X, e.Pos.Z)
		if e.Y != want {
			t.Errorf("step %d: Y = %v, want HeightAt(%v, %v) = %v", i, e.Y, e.Pos.X, e.Pos.Z, want)
		}
	}
	// East of the peak the hill falls away: still on the flank, Y is
	// strictly below the peak.
	if e.Pos.X <= 100 {
		t.Fatalf("player did not advance: pos = %v", e.Pos)
	}
	if e.Y >= 25 {
		t.Errorf("east of the peak Y = %v, want strictly below 25", e.Y)
	}
}

// TestSimulationFlatYWithoutResolver pins CTH-2: a simulation without a
// HeightResolver keeps Y at 0 — flat terrain is the nil-resolver
// default.
func TestSimulationFlatYWithoutResolver(t *testing.T) {
	sim := newSim(t, nil)
	sim.RegisterPlayer("p1", Vec2{100, 200}, "", stats.Stats{})
	sim.QueueInput("p1", move(1, 0, 5, 1))
	for i := 0; i < 4; i++ {
		if err := sim.Step(); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := sim.Entity("p1")
	if e.Y != 0 {
		t.Errorf("Y without resolver = %v, want 0 (flat)", e.Y)
	}
}

// TestSimulationRegisterResolvesSpawnY pins CTH-3 on the sim side: the
// entity's Y is resolved from the terrain at the spawn XZ the moment it
// registers, so the WorldSnapshot assembled right after registration
// carries the spawn height.
func TestSimulationRegisterResolvesSpawnY(t *testing.T) {
	heights, err := world.DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	sim := newSim(t, func(c *SimulationConfig) { c.Heights = heights })
	if err := sim.RegisterPlayer("p1", Vec2{100, 200}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if e.Y != 25 {
		t.Errorf("spawn Y on the hill = %v, want 25", e.Y)
	}
	// A flat-ground spawn resolves to 0.
	if err := sim.RegisterPlayer("p2", Vec2{0, 0}, "", stats.Stats{}); err != nil {
		t.Fatal(err)
	}
	e2, _ := sim.Entity("p2")
	if e2.Y != 0 {
		t.Errorf("flat spawn Y = %v, want 0", e2.Y)
	}
}

// TestSimulationInputNeverSetsY pins CTH-1 input through the full sim:
// a client MoveInput changes velocity/yaw, but the entity's Y always
// equals the terrain at its position — the client has no way to set Y
// (MoveInput carries no height field).
func TestSimulationInputNeverSetsY(t *testing.T) {
	heights, err := world.DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	sim := newSim(t, func(c *SimulationConfig) { c.Heights = heights })
	sim.RegisterPlayer("p1", Vec2{100, 200}, "", stats.Stats{})
	sim.QueueInput("p1", &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: 5, Yaw: 1.5})
	if err := sim.Step(); err != nil {
		t.Fatal(err)
	}
	e, _ := sim.Entity("p1")
	if e.Y != heights.HeightAt(e.Pos.X, e.Pos.Z) {
		t.Errorf("Y = %v, want terrain %v (input must not influence Y)", e.Y, heights.HeightAt(e.Pos.X, e.Pos.Z))
	}
}

// TestSimulationDeterminismOnHills pins WTM-6/CTH-1 determinism: two
// simulations sharing the same HeightResolver and the same input
// sequence produce identical positions, velocities, yaws AND Y on the
// hills fixture.
func TestSimulationDeterminismOnHills(t *testing.T) {
	run := func() *Simulation {
		heights, err := world.DefaultHeightfield()
		if err != nil {
			t.Fatalf("DefaultHeightfield: %v", err)
		}
		sim := newSim(t, func(c *SimulationConfig) { c.Heights = heights })
		if err := sim.RegisterPlayer("p1", Vec2{100, 200}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer p1: %v", err)
		}
		if err := sim.RegisterPlayer("p2", Vec2{60, 240}, "", stats.Stats{}); err != nil {
			t.Fatalf("RegisterPlayer p2: %v", err)
		}
		inputs := []*mmov1.MoveInput{
			move(1, 0, 5, 1),
			move(0, 1, 4, 2),
			move(-1, -1, 999, 3), // over-speed: must clamp identically
			move(1, 1, 3, 4),
		}
		for i := 0; i < 40; i++ {
			if err := sim.QueueInput("p1", inputs[i%len(inputs)]); err != nil {
				t.Fatalf("QueueInput: %v", err)
			}
			if err := sim.QueueInput("p2", inputs[(i+1)%len(inputs)]); err != nil {
				t.Fatalf("QueueInput: %v", err)
			}
			if err := sim.Step(); err != nil {
				t.Fatalf("Step: %v", err)
			}
		}
		return sim
	}
	a, b := run(), run()
	for _, id := range []string{"p1", "p2"} {
		ea, _ := a.Entity(id)
		eb, _ := b.Entity(id)
		if ea.Pos != eb.Pos || ea.Velocity != eb.Velocity || ea.Yaw != eb.Yaw || ea.Y != eb.Y {
			t.Errorf("determinism broke for %s:\n  a: pos=%v vel=%v yaw=%v y=%v\n  b: pos=%v vel=%v yaw=%v y=%v",
				id, ea.Pos, ea.Velocity, ea.Yaw, ea.Y, eb.Pos, eb.Velocity, eb.Yaw, eb.Y)
		}
	}
}
