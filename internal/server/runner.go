package server

// The room runner (design: RoomManager = control plane, roomRunner =
// runtime). It owns every per-room simulation and steps them all off a
// single 20 Hz ticker — one owner goroutine, never a per-room ticker.

import (
	"fmt"
	"sync"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/world"
)

// DefaultRoom is the room legacy (no-room) clients use: the current single
// global world maps to it (spec: mundo TCP/UDP = default room). It is never
// destroyed when empty.
const DefaultRoom rooms.RoomID = "default"

// roomSim is one room's runtime: its isolated simulation plus a membership
// count used to detect emptiness. Control-plane capacity/leases live in
// rooms.Manager; the runner owns the runtime simulation side.
type roomSim struct {
	id      rooms.RoomID
	sim     *game.Simulation
	members int
}

// roomOp is one simulation mutation addressed to a room. apply runs on the
// owner goroutine against the room's simulation; delta (+1 join / -1 leave)
// records the membership change applied on success, so the runner can
// destroy a room that emptied. done is buffered so the owner never blocks.
type roomOp struct {
	roomID rooms.RoomID
	delta  int
	apply  func(sim *game.Simulation) error
	done   chan error
}

// roomRunner owns every per-room simulation. mu guards the rooms map: the
// owner goroutine mutates it while the UDP path may read it via room.
type roomRunner struct {
	mu      sync.Mutex
	rooms   map[rooms.RoomID]*roomSim
	ops     chan roomOp
	sink    game.SnapshotSink
	heights world.HeightResolver
}

// newRoomRunner seeds a runner with the default (legacy) room backed by
// defaultSim, built in New before any goroutine starts. New rooms reuse
// sink and heights.
func newRoomRunner(defaultSim *game.Simulation, sink game.SnapshotSink, heights world.HeightResolver) *roomRunner {
	return &roomRunner{
		rooms: map[rooms.RoomID]*roomSim{
			DefaultRoom: {id: DefaultRoom, sim: defaultSim},
		},
		ops:     make(chan roomOp),
		sink:    sink,
		heights: heights,
	}
}

// ensureRoom returns the room's runtime state, creating its simulation on
// first use.
func (r *roomRunner) ensureRoom(id rooms.RoomID) (*roomSim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rs, ok := r.rooms[id]; ok {
		return rs, nil
	}
	sim, err := game.NewSimulation(game.SimulationConfig{Sink: r.sink, Heights: r.heights})
	if err != nil {
		return nil, err
	}
	rs := &roomSim{id: id, sim: sim}
	r.rooms[id] = rs
	return rs, nil
}

// room returns the room's runtime state, or nil when it does not exist.
func (r *roomRunner) room(id rooms.RoomID) *roomSim {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rooms[id]
}

func (r *roomRunner) roomCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rooms)
}

func (r *roomRunner) allRooms() []*roomSim {
	r.mu.Lock()
	defer r.mu.Unlock()
	rooms := make([]*roomSim, 0, len(r.rooms))
	for _, rs := range r.rooms {
		rooms = append(rooms, rs)
	}
	return rooms
}

func (r *roomRunner) destroy(id rooms.RoomID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rooms, id)
}

// stepAll advances every room by exactly one fixed tick. The room-owner
// loop calls this once per 20 Hz wake — one ticker for all rooms.
func (r *roomRunner) stepAll() error {
	for _, rs := range r.allRooms() {
		if err := rs.sim.Step(); err != nil {
			return err
		}
	}
	return nil
}

// apply services one room op on the owner goroutine: apply the mutation,
// adjust membership on success, and destroy the room if it emptied.
func (r *roomRunner) apply(op roomOp) {
	rs := r.room(op.roomID)
	if rs == nil {
		if op.done != nil {
			op.done <- fmt.Errorf("server: room %q not found", op.roomID)
		}
		return
	}
	err := op.apply(rs.sim)
	if err == nil {
		rs.members += op.delta
	}
	if op.done != nil {
		op.done <- err
	}
	if rs.members == 0 && rs.id != DefaultRoom {
		r.destroy(rs.id)
	}
}
