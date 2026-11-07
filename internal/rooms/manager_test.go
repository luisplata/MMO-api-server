package rooms

// Room Manager tests (design P4): a fixed capacity-2 room directory with
// atomic Reserve/Commit/Release. Coverage: a third reservation is rejected
// without consuming a slot; concurrent reserves never over-subscribe; commit
// converts a reservation into a member; release is idempotent; a failed
// commit never leaks a slot; rooms are independent.

import (
	"errors"
	"sync"
	"testing"
)

// TestReserveCapacityTwo: a room accepts at most Capacity reservations and
// rejects the third with ErrRoomFull.
func TestReserveCapacityTwo(t *testing.T) {
	m := NewManager()
	r1, err := m.Reserve("room-a")
	if err != nil {
		t.Fatalf("Reserve(1): %v", err)
	}
	r2, err := m.Reserve("room-a")
	if err != nil {
		t.Fatalf("Reserve(2): %v", err)
	}
	if _, err := m.Reserve("room-a"); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("Reserve(3) = %v, want ErrRoomFull", err)
	}
	if got := m.Reserved("room-a"); got != 2 {
		t.Fatalf("Reserved = %d, want 2", got)
	}
	m.Release(r1)
	m.Release(r2)
}

// TestRoomFullConsumesNoSlot: a rejected third reserve holds nothing, so
// after one release a fresh reserve succeeds (capacity was never consumed).
func TestRoomFullConsumesNoSlot(t *testing.T) {
	m := NewManager()
	r1, _ := m.Reserve("room-a")
	r2, _ := m.Reserve("room-a")
	if _, err := m.Reserve("room-a"); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("Reserve(3) = %v, want ErrRoomFull", err)
	}

	m.Release(r1)
	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve after releasing one slot = %v, want nil", err)
	}
	if got := m.Reserved("room-a"); got != 2 {
		t.Fatalf("Reserved after re-reserve = %d, want 2", got)
	}
	m.Release(r2)
}

// TestConcurrentReserveAtomic: N concurrent reserves against a single fresh
// room grant exactly Capacity slots and reject every other caller with
// ErrRoomFull, never over-subscribing.
func TestConcurrentReserveAtomic(t *testing.T) {
	m := NewManager()
	const n = 100

	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Reserve("race")
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var success int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrRoomFull):
		default:
			t.Fatalf("unexpected reserve error: %v", err)
		}
	}
	if success != Capacity {
		t.Fatalf("successful reserves = %d, want %d", success, Capacity)
	}
	if got := m.Reserved("race"); got != Capacity {
		t.Fatalf("Reserved after race = %d, want %d", got, Capacity)
	}
}

// TestCommitConvertsReservationToMember: Commit moves a reservation into the
// committed (member) bucket without changing the total slot count.
func TestCommitConvertsReservationToMember(t *testing.T) {
	m := NewManager()
	r, err := m.Reserve("room-a")
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if got := m.Reserved("room-a"); got != 1 {
		t.Fatalf("Reserved before commit = %d, want 1", got)
	}
	if got := m.Occupancy("room-a"); got != 0 {
		t.Fatalf("Occupancy before commit = %d, want 0", got)
	}

	if err := m.Commit(r); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved after commit = %d, want 0", got)
	}
	if got := m.Occupancy("room-a"); got != 1 {
		t.Fatalf("Occupancy after commit = %d, want 1", got)
	}
}

// TestCommitIdempotent: committing an already-committed reservation is a
// no-op that returns nil and never double-counts the member.
func TestCommitIdempotent(t *testing.T) {
	m := NewManager()
	r, _ := m.Reserve("room-a")
	if err := m.Commit(r); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := m.Commit(r); err != nil {
		t.Fatalf("Commit (idempotent) = %v, want nil", err)
	}
	if got := m.Occupancy("room-a"); got != 1 {
		t.Fatalf("Occupancy after double commit = %d, want 1", got)
	}
}

// TestReleaseIdempotent: releasing the same reservation twice is safe and
// never frees the slot more than once.
func TestReleaseIdempotent(t *testing.T) {
	m := NewManager()
	r, _ := m.Reserve("room-a")
	m.Release(r)
	m.Release(r) // second release must be a no-op
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved after double release = %d, want 0", got)
	}
	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve after double release = %v, want nil", err)
	}
}

// TestFailedCommitFreesCapacity: a reservation released before Commit makes
// Commit fail with ErrUnknownReservation and leaves no leaked slot, so a
// fresh reserve succeeds.
func TestFailedCommitFreesCapacity(t *testing.T) {
	m := NewManager()
	r, _ := m.Reserve("room-a")
	m.Release(r) // disconnect before EnterWorld

	if err := m.Commit(r); !errors.Is(err, ErrUnknownReservation) {
		t.Fatalf("Commit after release = %v, want ErrUnknownReservation", err)
	}
	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve after failed commit = %v, want nil (no leaked slot)", err)
	}
	if got := m.Reserved("room-a"); got != 1 {
		t.Fatalf("Reserved after failed commit = %d, want 1", got)
	}
}

// TestCommitUnknownReservation: committing a token the manager never issued
// (the zero Reservation) fails without touching any room.
func TestCommitUnknownReservation(t *testing.T) {
	m := NewManager()
	if err := m.Commit(Reservation{}); !errors.Is(err, ErrUnknownReservation) {
		t.Fatalf("Commit(zero) = %v, want ErrUnknownReservation", err)
	}
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved after unknown commit = %d, want 0", got)
	}
}

// TestRoomsAreIndependent: capacity is per-room, not global.
func TestRoomsAreIndependent(t *testing.T) {
	m := NewManager()
	for _, id := range []RoomID{"room-a", "room-b"} {
		if _, err := m.Reserve(id); err != nil {
			t.Fatalf("Reserve(%s) 1: %v", id, err)
		}
		if _, err := m.Reserve(id); err != nil {
			t.Fatalf("Reserve(%s) 2: %v", id, err)
		}
		if _, err := m.Reserve(id); !errors.Is(err, ErrRoomFull) {
			t.Fatalf("Reserve(%s) 3 = %v, want ErrRoomFull", id, err)
		}
	}
}
