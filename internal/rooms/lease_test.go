package rooms

// Reservation lease tests (design P5): an uncommitted reservation holds its
// slot for Lease (30 s) and is swept on expiry; a committed reservation is
// never swept; sweeps are deterministic (injectable clock, no sleeps) and
// idempotent. The freed slot is immediately reservable again.

import (
	"testing"
	"time"
)

// fakeClock is an injectable time source so lease expiry is deterministic.
type fakeClock struct {
	t time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(0, 0)} }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// TestReservationNotExpiredBeforeLease: one nanosecond before the lease the
// slot is still held and the sweep frees nothing.
func TestReservationNotExpiredBeforeLease(t *testing.T) {
	clock := newFakeClock()
	m := NewManagerWithClock(clock)

	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	clock.Advance(Lease - time.Nanosecond)

	if got := m.SweepNow(); got != 0 {
		t.Fatalf("SweepNow before lease = %d, want 0", got)
	}
	if got := m.Reserved("room-a"); got != 1 {
		t.Fatalf("Reserved before lease expiry = %d, want 1", got)
	}
}

// TestReservationExpiresAtLease: exactly at Lease the slot is freed and the
// room is reservable again.
func TestReservationExpiresAtLease(t *testing.T) {
	clock := newFakeClock()
	m := NewManagerWithClock(clock)

	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	clock.Advance(Lease)

	if got := m.SweepNow(); got != 1 {
		t.Fatalf("SweepNow at lease = %d, want 1", got)
	}
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved after expiry = %d, want 0", got)
	}
	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve after expiry = %v, want nil (slot freed)", err)
	}
}

// TestCommittedReservationNotSwept: a committed member keeps its slot across
// sweeps, while a sibling uncommitted reservation expires.
func TestCommittedReservationNotSwept(t *testing.T) {
	clock := newFakeClock()
	m := NewManagerWithClock(clock)

	r1, err := m.Reserve("room-a")
	if err != nil {
		t.Fatalf("Reserve r1: %v", err)
	}
	if err := m.Commit(r1); err != nil {
		t.Fatalf("Commit r1: %v", err)
	}
	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve r2: %v", err)
	}

	clock.Advance(Lease)
	if got := m.SweepNow(); got != 1 {
		t.Fatalf("SweepNow = %d, want 1 (only uncommitted r2)", got)
	}
	if got := m.Occupancy("room-a"); got != 1 {
		t.Fatalf("Occupancy after sweep = %d, want 1 (committed member kept)", got)
	}
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved after sweep = %d, want 0", got)
	}
}

// TestSweepIdempotent: sweeping twice frees the slot once; a redundant sweep
// reports zero and never double-frees.
func TestSweepIdempotent(t *testing.T) {
	clock := newFakeClock()
	m := NewManagerWithClock(clock)

	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	clock.Advance(Lease)

	if got := m.SweepNow(); got != 1 {
		t.Fatalf("first SweepNow = %d, want 1", got)
	}
	if got := m.SweepNow(); got != 0 {
		t.Fatalf("second SweepNow = %d, want 0", got)
	}
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved = %d, want 0", got)
	}
}

// TestSweepReleasesOnlyExpired: reservations made at different times expire in
// order; a sweep at an intermediate instant frees only the earlier one.
func TestSweepReleasesOnlyExpired(t *testing.T) {
	clock := newFakeClock()
	m := NewManagerWithClock(clock)

	if _, err := m.Reserve("room-a"); err != nil { // expires at t0+Lease
		t.Fatalf("Reserve r1: %v", err)
	}
	clock.Advance(Lease / 2)
	if _, err := m.Reserve("room-a"); err != nil { // expires at t0+1.5*Lease
		t.Fatalf("Reserve r2: %v", err)
	}

	if got := m.Sweep(clock.t.Add(Lease / 2)); got != 1 {
		t.Fatalf("Sweep at r1 expiry = %d, want 1", got)
	}
	if got := m.Reserved("room-a"); got != 1 {
		t.Fatalf("Reserved after partial sweep = %d, want 1", got)
	}

	if got := m.Sweep(clock.t.Add(Lease)); got != 1 {
		t.Fatalf("Sweep at r2 expiry = %d, want 1", got)
	}
	if got := m.Reserved("room-a"); got != 0 {
		t.Fatalf("Reserved after full sweep = %d, want 0", got)
	}
}

// TestReleaseStillPreemptsLease: explicit Release frees the slot before the
// lease would, so a disconnect never waits the full 30 s; a later sweep has
// nothing left to do.
func TestReleaseStillPreemptsLease(t *testing.T) {
	m := NewManagerWithClock(newFakeClock())

	r, err := m.Reserve("room-a")
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	m.Release(r)
	if got := m.SweepNow(); got != 0 {
		t.Fatalf("SweepNow after explicit release = %d, want 0", got)
	}
	if _, err := m.Reserve("room-a"); err != nil {
		t.Fatalf("Reserve after release = %v, want nil", err)
	}
}
