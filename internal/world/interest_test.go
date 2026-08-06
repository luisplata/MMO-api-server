package world

// Interest management tests (design D4, spec R18/R19, S18.2/S19.1/S19.2).
//
// Coverage: v1 FlatResolver fanout (every player, never the subject),
// the InterestTracker's join/leave/move event computation, and — via a
// chunk-view test resolver implementing the SAME InterestResolver seam —
// proof that targeted fanout swaps in without touching the tracker logic
// or the wire contract (S19.2).

import (
	"testing"
)

// chunkViewResolver mimics the future chunk-based fanout (design D4):
// a player is interested in a subject only when the subject's cell lies
// inside the player's 5x5 view set. It is used to prove the seam — the
// tracker drives spawn/despawn events through any InterestResolver.
type chunkViewResolver struct {
	all func() []*Player
}

func (r chunkViewResolver) ViewPlayers(p *Player) []*Player {
	var out []*Player
	for _, q := range r.all() {
		if q.ID == p.ID {
			continue
		}
		if inView(q.Cell, p.Cell) {
			out = append(out, q)
		}
	}
	return out
}

func inView(viewer, subject Cell) bool {
	dx := viewer.X - subject.X
	dz := viewer.Z - subject.Z
	if dx < 0 {
		dx = -dx
	}
	if dz < 0 {
		dz = -dz
	}
	return dx <= ViewRadius && dz <= ViewRadius
}

func TestFlatResolverExcludesSelf(t *testing.T) {
	players := []*Player{
		{ID: "a", Cell: Cell{0, 0}},
		{ID: "b", Cell: Cell{1, 1}},
		{ID: "c", Cell: Cell{2, 2}},
	}
	r := NewFlatResolver(func() []*Player { return players })
	got := r.ViewPlayers(players[0])
	if len(got) != 2 {
		t.Fatalf("FlatResolver.ViewPlayers(a) = %d players, want 2 (all others)", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		if p.ID == "a" {
			t.Errorf("FlatResolver returned the subject itself")
		}
		seen[p.ID] = true
	}
	if !seen["b"] || !seen["c"] {
		t.Errorf("FlatResolver fanout = %v, want {b, c} (spec S19.1)", seen)
	}
}

func TestTrackerJoinSpawnsToExisting(t *testing.T) {
	tr := NewInterestTracker(nil) // nil -> v1 FlatResolver
	if got := tr.PlayerCount(); got != 0 {
		t.Fatalf("fresh tracker PlayerCount = %d, want 0", got)
	}
	// First player joins: nobody exists to see it, no events (the
	// newcomer receives the initial world state via WorldSnapshot).
	if events := tr.Update("a", 0, 0); len(events) != 0 {
		t.Fatalf("first join events = %v, want none", events)
	}
	if got := tr.PlayerCount(); got != 1 {
		t.Fatalf("after join a PlayerCount = %d, want 1", got)
	}
	// Second player joins: the existing player must spawn it.
	events := tr.Update("b", 100, 100)
	if len(events) != 1 {
		t.Fatalf("second join events = %d, want 1 spawn", len(events))
	}
	ev := events[0]
	if ev.Kind != SpawnEvent {
		t.Errorf("event kind = %v, want SpawnEvent", ev.Kind)
	}
	if ev.Subject != "b" {
		t.Errorf("event subject = %q, want %q (the joiner)", ev.Subject, "b")
	}
	if len(ev.Targets) != 1 || ev.Targets[0] != "a" {
		t.Errorf("event targets = %v, want [a] (v1 flat: everyone existing)", ev.Targets)
	}
	// A third join fans out to BOTH existing players.
	events = tr.Update("c", 200, 200)
	if len(events) != 1 {
		t.Fatalf("third join events = %d, want 1", len(events))
	}
	if len(events[0].Targets) != 2 {
		t.Errorf("third join targets = %v, want [a b]", events[0].Targets)
	}
}

func TestTrackerLeaveDespawnsToRemaining(t *testing.T) {
	tr := NewInterestTracker(nil)
	tr.Update("a", 0, 0)
	tr.Update("b", 100, 100)
	tr.Update("c", 200, 200)

	events := tr.Remove("a")
	if len(events) != 1 {
		t.Fatalf("remove a events = %d, want 1 despawn", len(events))
	}
	ev := events[0]
	if ev.Kind != DespawnEvent {
		t.Errorf("event kind = %v, want DespawnEvent", ev.Kind)
	}
	if ev.Subject != "a" {
		t.Errorf("event subject = %q, want %q", ev.Subject, "a")
	}
	if len(ev.Targets) != 2 {
		t.Errorf("despawn targets = %v, want [b c] (all remaining)", ev.Targets)
	}
	if got := tr.PlayerCount(); got != 2 {
		t.Errorf("PlayerCount after remove = %d, want 2", got)
	}
}

// TestTrackerMoveFlatNoEvents proves the v1 flat resolver is
// cell-independent: a crossing cell change must NOT produce events
// because every player is already interested in every other (S19.1).
func TestTrackerMoveFlatNoEvents(t *testing.T) {
	tr := NewInterestTracker(nil)
	tr.Update("a", 0, 0)
	tr.Update("b", 0, 0)
	// Move b two cells east — still flat-visible to everyone.
	events := tr.Update("b", 200, 0)
	if len(events) != 0 {
		t.Fatalf("flat cell move events = %v, want none", events)
	}
}

// TestTrackerChunkResolverTargetedEvents proves the seam (S18.2/S19.2):
// the SAME tracker code produces targeted spawn/despawn events when the
// resolver narrows fanout to the 5x5 view set. No tracker or protocol
// change is needed — only the resolver implementation swaps.
func TestTrackerChunkResolverTargetedEvents(t *testing.T) {
	var tr *InterestTracker
	tr = NewInterestTracker(chunkViewResolver{all: func() []*Player { return tr.allPlayers() }})
	tr.Update("a", 0, 0)   // at cell (0,0)
	tr.Update("b", 640, 0) // at cell (10,0) — far outside a's view
	if got := tr.PlayerCount(); got != 2 {
		t.Fatalf("PlayerCount = %d, want 2", got)
	}

	// b joins out of view: no one must spawn it yet.
	events := tr.Update("b", 640, 0)
	if len(events) != 0 {
		t.Fatalf("out-of-view join events = %v, want none", events)
	}

	// b moves to cell (1,0): now inside a's 5x5 view (dx=1) — a must
	// spawn b (S18.2 enter event).
	events = tr.Update("b", 100, 0)
	if len(events) != 1 {
		t.Fatalf("crossing events = %d, want 1 spawn", len(events))
	}
	ev := events[0]
	if ev.Kind != SpawnEvent || ev.Subject != "b" {
		t.Fatalf("crossing event = %+v, want SpawnEvent of b", ev)
	}
	if len(ev.Targets) != 1 || ev.Targets[0] != "a" {
		t.Errorf("crossing targets = %v, want [a] (only a's view contains b)", ev.Targets)
	}

	// b moves out again: a loses interest -> despawn (S18.2 leave).
	events = tr.Update("b", 640, 0)
	if len(events) != 1 {
		t.Fatalf("leave events = %d, want 1 despawn", len(events))
	}
	ev = events[0]
	if ev.Kind != DespawnEvent || ev.Subject != "b" {
		t.Fatalf("leave event = %+v, want DespawnEvent of b", ev)
	}
	if len(ev.Targets) != 1 || ev.Targets[0] != "a" {
		t.Errorf("leave targets = %v, want [a]", ev.Targets)
	}
}

// TestTrackerRemoveUnknown is a no-op: removing a player that was never
// registered must not panic and must not emit events.
func TestTrackerRemoveUnknown(t *testing.T) {
	tr := NewInterestTracker(nil)
	if events := tr.Remove("ghost"); len(events) != 0 {
		t.Fatalf("remove unknown events = %v, want none", events)
	}
}

// TestTrackerCellOf exposes the tracked cell so the simulation can avoid
// redundant resolver calls and tests can assert the tracked position.
func TestTrackerCellOf(t *testing.T) {
	tr := NewInterestTracker(nil)
	tr.Update("a", -1, 64)
	cell, ok := tr.CellOf("a")
	if !ok {
		t.Fatal("CellOf(a) not found after Update")
	}
	if cell != (Cell{-1, 1}) {
		t.Errorf("CellOf(a) = %v, want (-1,1) for (-1, 64)", cell)
	}
	if _, ok := tr.CellOf("ghost"); ok {
		t.Error("CellOf(ghost) reported present")
	}
}
