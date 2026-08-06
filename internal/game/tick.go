package game

// Deterministic 20 Hz simulation loop (spec R14, S14.1/S14.2).
//
// The loop never sleeps: it wakes on an injected channel, accumulates
// wall-clock time into fixed-dt steps (Accumulator), and runs each step
// with the inputs queued since the last tick. All tick math is pure —
// identical input sequences produce identical entity states (S14.2) —
// and the clock/wake source is injected so tests are deterministic
// (no time.Sleep anywhere).
//
// Movement, snapshot broadcast and interest updates all happen inside
// Step; the wiring layer (cmd/server, PR4b) only needs to feed
// MoveInputs (QueueInput) and drain InterestEvents / snapshots.

import (
	"context"
	"errors"
	"sync"
	"time"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"

	"github.com/luisplata/mmo-api-server/internal/world"
)

// TickRate is the simulation cadence in ticks per second (spec R14).
const TickRate = 20

// TickInterval is the fixed delta consumed by each tick: 50 ms.
const TickInterval = 50 * time.Millisecond

// Errors returned by the simulation.
var (
	// ErrUnknownPlayer wraps input/removal for an unregistered player.
	ErrUnknownPlayer = errors.New("game: unknown player")
	// ErrDuplicatePlayer wraps re-registering an existing player.
	ErrDuplicatePlayer = errors.New("game: player already registered")
	// ErrNilSink rejects a simulation without a snapshot sink.
	ErrNilSink = errors.New("game: SnapshotSink is required")
)

// Accumulator converts wall-clock elapsed time into an exact number of
// fixed-dt steps, carrying the remainder across wakes so the loop never
// drifts and never runs fractional ticks. A negative elapsed (clock
// skew) runs nothing.
type Accumulator struct {
	dt        time.Duration
	remainder time.Duration
}

// NewAccumulator builds an accumulator for the given fixed delta.
func NewAccumulator(dt time.Duration) *Accumulator {
	return &Accumulator{dt: dt}
}

// StepCount returns how many fixed-dt ticks the elapsed time contains
// (plus any carried remainder) and keeps the leftover for the next call.
func (a *Accumulator) StepCount(elapsed time.Duration) int {
	if elapsed < 0 {
		return 0
	}
	total := a.remainder + elapsed
	steps := int(total / a.dt)
	a.remainder = total % a.dt
	return steps
}

// SnapshotSink receives assembled per-player snapshots. The wiring
// layer (PR4b) routes them over the session's UDP transport.
type SnapshotSink interface {
	// SendSnapshot delivers one snapshot to the named player.
	SendSnapshot(playerID string, snap *mmov1.Snapshot) error
}

// SimulationConfig carries the tunables and seams for a simulation.
type SimulationConfig struct {
	// MaxSpeed is the speed limit (units/sec); zero selects
	// DefaultMaxSpeed.
	MaxSpeed float32
	// Assembler fills the wire Snapshot; nil selects FullStateAssembler.
	Assembler SnapshotAssembler
	// Resolver drives interest fanout (design D4); nil selects the v1
	// FlatResolver.
	Resolver world.InterestResolver
	// Sink receives every broadcast snapshot. Required.
	Sink SnapshotSink
}

// Simulation is the deterministic world: the entity registry, the input
// queue, the interest tracker and the snapshot broadcaster, driven by
// Step (one fixed tick) or Run (wall-clock loop).
type Simulation struct {
	assembler SnapshotAssembler
	maxSpeed  float32
	tracker   *world.InterestTracker
	sink      SnapshotSink

	players map[string]*Entity
	order   []string

	mu     sync.Mutex
	inputs map[string][]moveInput

	tick   uint64
	seq    uint32
	events []world.InterestEvent
}

// moveInput is a queued client input awaiting the next tick. The client
// seq tags processing to the tick that consumes it (spec R15).
type moveInput struct {
	clientSeq int32
	dir       Vec2
	speed     float32
	yaw       float32
}

// NewSimulation builds a simulation. The sink is required; a zero
// MaxSpeed and nil assembler/resolver fall back to the defaults.
func NewSimulation(cfg SimulationConfig) (*Simulation, error) {
	if cfg.Sink == nil {
		return nil, ErrNilSink
	}
	maxSpeed := cfg.MaxSpeed
	if maxSpeed == 0 {
		maxSpeed = DefaultMaxSpeed
	}
	assembler := cfg.Assembler
	if assembler == nil {
		assembler = FullStateAssembler{}
	}
	s := &Simulation{
		assembler: assembler,
		maxSpeed:  maxSpeed,
		sink:      cfg.Sink,
		players:   make(map[string]*Entity),
		inputs:    make(map[string][]moveInput),
	}
	s.tracker = world.NewInterestTracker(cfg.Resolver)
	return s, nil
}

// Tick reports the number of steps executed so far.
func (s *Simulation) Tick() uint64 { return s.tick }

// Entity returns the current authoritative state of a player.
func (s *Simulation) Entity(id string) (*Entity, bool) {
	e, ok := s.players[id]
	return e, ok
}

// RegisterPlayer spawns a new player at the given position (design D3:
// player spawning at spawnPos), tracking its interest cell. It fails on
// a duplicate id.
func (s *Simulation) RegisterPlayer(id string, spawn Vec2) error {
	if _, ok := s.players[id]; ok {
		return ErrDuplicatePlayer
	}
	s.players[id] = &Entity{ID: id, Pos: spawn}
	s.order = append(s.order, id)
	s.events = append(s.events, s.tracker.Update(id, spawn.X, spawn.Z)...)
	return nil
}

// RemovePlayer unregisters a player, emitting its despawn event and
// clearing any queued inputs. It fails on an unknown id.
func (s *Simulation) RemovePlayer(id string) error {
	if _, ok := s.players[id]; !ok {
		return ErrUnknownPlayer
	}
	s.events = append(s.events, s.tracker.Remove(id)...)
	delete(s.players, id)
	for i, pid := range s.order {
		if pid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Lock()
	delete(s.inputs, id)
	s.mu.Unlock()
	return nil
}

// QueueInput injects one client MoveInput for the next tick (the
// session's UDP path feeds this — the interface cmd/server wiring
// consumes). The input's client seq is preserved to tag processing to
// the tick. It fails for an unknown player.
func (s *Simulation) QueueInput(id string, in *mmov1.MoveInput) error {
	if in == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.players[id]; !ok {
		return ErrUnknownPlayer
	}
	var dir Vec2
	if in.Dir != nil {
		dir = Vec2{X: in.Dir.X, Z: in.Dir.Z}
	}
	s.inputs[id] = append(s.inputs[id], moveInput{
		clientSeq: in.Seq,
		dir:       dir,
		speed:     in.Speed,
		yaw:       in.Yaw,
	})
	return nil
}

// Step runs exactly one fixed-dt tick, deterministically: queued inputs
// are applied (validated and clamped, tagged to this tick), entities
// integrate kinematically, interest cells update, and the 10 Hz
// staggered snapshots are assembled and delivered to the sink.
func (s *Simulation) Step() error {
	dt := float32(TickInterval.Seconds())

	// Drain the input queue atomically so a concurrent UDP handler can
	// never interleave mid-tick.
	s.mu.Lock()
	inputs := s.inputs
	s.inputs = make(map[string][]moveInput)
	s.mu.Unlock()

	for _, id := range s.order {
		e := s.players[id]
		for _, in := range inputs[id] {
			applyMove(e, in.dir, in.speed, in.yaw, s.maxSpeed)
			e.LastInputTick = s.tick
			e.LastInputSeq = in.clientSeq
		}
		Integrate(e, dt)
		s.events = append(s.events, s.tracker.Update(id, e.Pos.X, e.Pos.Z)...)
	}

	entities := s.snapshotEntities()
	for idx, id := range s.order {
		if ShouldSnapshot(s.tick, idx) {
			s.seq++
			snap := s.assembler.Assemble(s.seq, entities)
			if err := s.sink.SendSnapshot(id, snap); err != nil {
				return err
			}
		}
	}
	s.tick++
	return nil
}

// Run drives the loop until ctx is cancelled or the wake channel
// closes. The wake channel carries the wall-clock timestamps (a
// time.Ticker in production) and is the loop's only time source: the
// first wake primes the baseline, and each subsequent wake consumes as
// many fixed-dt steps as the elapsed time between timestamps allows
// (20 ticks per simulated second, S14.1). Tests drive it with synthetic
// timestamps — fully deterministic, no time.Sleep.
func (s *Simulation) Run(ctx context.Context, wake <-chan time.Time) error {
	acc := NewAccumulator(TickInterval)
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case t, ok := <-wake:
			if !ok {
				return nil
			}
			if !last.IsZero() {
				steps := acc.StepCount(t.Sub(last))
				for i := 0; i < steps; i++ {
					if err := s.Step(); err != nil {
						return err
					}
				}
			}
			last = t
		}
	}
}

// TakeInterestEvents drains the pending spawn/despawn events for the
// wiring layer to turn into TCP frames.
func (s *Simulation) TakeInterestEvents() []world.InterestEvent {
	ev := s.events
	s.events = nil
	return ev
}

// AssembleWorldSnapshot fills the enter-world payload with the full
// current state (v1: full-state, no deltas) — the wiring layer sends it
// on EnterWorld. Yaw is carried raw; future delta-encoding must handle
// wrap-around at 0/2π (spec S16.2).
func (s *Simulation) AssembleWorldSnapshot() *mmov1.WorldSnapshot {
	entities := s.snapshotEntities()
	ws := &mmov1.WorldSnapshot{Entities: make([]*mmov1.EntityState, 0, len(entities))}
	for _, e := range entities {
		ws.Entities = append(ws.Entities, entityState(e))
	}
	return ws
}

// snapshotEntities returns the current entities in stable broadcast
// order (registration order).
func (s *Simulation) snapshotEntities() []*Entity {
	entities := make([]*Entity, 0, len(s.order))
	for _, id := range s.order {
		entities = append(entities, s.players[id])
	}
	return entities
}
