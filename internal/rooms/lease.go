package rooms

import "time"

// Lease is how long an uncommitted reservation holds its slot before the
// sweeper frees it (design A7: 30 s, commit on EnterWorld). A reservation
// that is committed becomes a member and is never swept.
const Lease = 30 * time.Second

// Clock is an injectable time source so lease expiry is deterministic in
// tests (no sleeps). Production uses systemClock; tests inject a fake.
type Clock interface {
	Now() time.Time
}

// systemClock is the wall-clock source used by NewManager.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Reservation lease lifecycle:
//
//	Reserve   → slot held, expires after Lease
//	Commit    → slot becomes a permanent member (never swept)
//	Release   → explicit free (the disconnect hook, below)
//	SweepNow  → periodic free of uncommitted, expired reservations
//
// The disconnect hook (spec "limpieza al desconectar") calls Release for the
// session's active reservation so an abrupt drop frees its slot promptly,
// before the 30 s lease would. P10 wires this at the session layer; Release
// is idempotent so a redundant disconnect release is a no-op.
//
// SweepNow is the sweeper tick. The room runner (P6) invokes it on its tick
// so abandoned reservations cannot pin a slot past Lease. P5 deliberately
// ships only the tick method, no background goroutine: goroutine ownership
// belongs to the runner.

// Sweep releases every uncommitted reservation whose lease expired by now and
// returns how many slots it freed. Committed members are never swept. It is
// safe to call repeatedly and concurrently: an already-released reservation
// is simply absent. The explicit now keeps it deterministic in tests.
func (m *Manager) Sweep(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sweepLocked(now)
}

// SweepNow releases reservations expired at the manager's clock and returns
// how many slots it freed. The room runner calls this each tick.
func (m *Manager) SweepNow() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sweepLocked(m.clock.Now())
}

// sweepLocked releases expired reservations. The caller must hold mu.
func (m *Manager) sweepLocked(now time.Time) int {
	var expired []uint64
	for tok, res := range m.res {
		if !res.member && !now.Before(res.expires) {
			expired = append(expired, tok)
		}
	}
	for _, tok := range expired {
		m.releaseLocked(tok)
	}
	return len(expired)
}
