package rooms

// Room directory — capacity core (design P4). A Manager atomically reserves
// one of a room's Capacity slots and tracks the reservation -> member
// transition, so a third concurrent reserve for a two-player room can never
// succeed. It is the control-plane seam of the "fleet seam" decision: leases
// (P5) and room runners (P6) attach without changing this capacity core.

import (
	"errors"
	"sync"
)

// Capacity is the fixed number of active players per room (design: 2).
const Capacity = 2

// Errors returned by the Manager.
var (
	// ErrRoomFull is returned by Reserve when the room has no free slot.
	ErrRoomFull = errors.New("rooms: room full")
	// ErrUnknownReservation is returned by Commit for a reservation the
	// manager no longer tracks (never issued or already released).
	ErrUnknownReservation = errors.New("rooms: unknown reservation")
)

// RoomID identifies a room. It is opaque to the Manager, which only keys
// per-room occupancy on it (design: room ids are opaque).
type RoomID string

// Reservation is the opaque handle returned by a successful Reserve. It is
// the unit passed to Commit and Release. Callers hold it as an opaque value;
// the zero Reservation is never a valid token.
type Reservation struct {
	token uint64
}

// Manager is the in-process room directory. All mutations run under a single
// mutex so capacity accounting is atomic under concurrency.
type Manager struct {
	mu    sync.Mutex
	rooms map[RoomID]*room
	res   map[uint64]*reservation
	next  uint64
}

// room tracks one room's occupancy: committed members plus uncommitted
// reservations. A slot is occupied by either.
type room struct {
	reserved int // uncommitted reservations
	members  int // committed active players
}

// slots is the total occupied capacity (members + reservations).
func (r *room) slots() int { return r.reserved + r.members }

// reservation is the internal record backing an opaque Reservation token.
type reservation struct {
	roomID RoomID
	member bool // true once committed
}

// NewManager builds an empty room directory.
func NewManager() *Manager {
	return &Manager{
		rooms: make(map[RoomID]*room),
		res:   make(map[uint64]*reservation),
	}
}

// Reserve atomically takes one of the room's slots, lazily creating the room
// on first use. On success it returns a Reservation to Commit or Release.
// When the room is full it returns ErrRoomFull and consumes no slot.
func (m *Manager) Reserve(roomID RoomID) (Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.rooms[roomID]
	if r == nil {
		r = &room{}
		m.rooms[roomID] = r
	}
	if r.slots() >= Capacity {
		return Reservation{}, ErrRoomFull
	}
	r.reserved++
	m.next++
	tok := m.next
	m.res[tok] = &reservation{roomID: roomID}
	return Reservation{token: tok}, nil
}

// Commit converts a reservation into an active member. It is idempotent:
// committing an already-committed reservation is a no-op returning nil. It
// fails with ErrUnknownReservation when the reservation was released (or
// never issued), in which case no slot is held — capacity is already free.
func (m *Manager) Commit(r Reservation) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	res, ok := m.res[r.token]
	if !ok {
		return ErrUnknownReservation
	}
	if res.member {
		return nil
	}
	res.member = true
	room := m.rooms[res.roomID]
	room.reserved--
	room.members++
	return nil
}

// Release frees the reservation's slot (a member's slot if it was committed,
// the reserved slot otherwise). It is idempotent and safe to call after a
// failed Commit or on an unknown token: a redundant release is a no-op, and
// an emptied room is dropped.
func (m *Manager) Release(r Reservation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseLocked(r.token)
}

// releaseLocked frees a tracked reservation's slot. The caller must hold mu.
// It is the shared path for explicit Release and the lease sweep.
func (m *Manager) releaseLocked(token uint64) {
	res, ok := m.res[token]
	if !ok {
		return
	}
	delete(m.res, token)
	room := m.rooms[res.roomID]
	if res.member {
		room.members--
	} else {
		room.reserved--
	}
	if room.slots() == 0 {
		delete(m.rooms, res.roomID)
	}
}

// Occupancy reports the committed active players in a room (0 for an
// unknown room).
func (m *Manager) Occupancy(roomID RoomID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rooms[roomID]; r != nil {
		return r.members
	}
	return 0
}

// Reserved reports the uncommitted reservations in a room (0 for an unknown
// room).
func (m *Manager) Reserved(roomID RoomID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rooms[roomID]; r != nil {
		return r.reserved
	}
	return 0
}
