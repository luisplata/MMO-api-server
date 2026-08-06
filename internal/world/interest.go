package world

// Interest management (design D4, spec R18/R19).
//
// InterestResolver is the seam that keeps the wire contract stable
// (S19.2): v1 uses FlatResolver (every player sees every other, spec
// S19.1); a future chunk resolver narrows fanout to the 5x5 view set
// with zero protocol churn. The InterestTracker turns membership and
// cell crossings into spawn/despawn events (S18.2) — the wiring layer
// turns those events into TCP frames (WorldSnapshot for a spawn in v1).

import (
	"strings"
)

// Player is a world member tracked for interest. The tracker keeps
// Cell in sync with the player's latest position.
type Player struct {
	ID   string
	Cell Cell
}

// InterestResolver returns the players currently interested in seeing
// p — i.e. who must receive p's spawns, despawns and (later) updates.
// Contract: the returned set MUST NOT include p itself; a player is
// never interested in itself (no self-spawn/despawn events).
//
//	v1:  FlatResolver — every other player (S19.1).
//	future: chunk resolver — players whose 5x5 view contains p.Cell.
type InterestResolver interface {
	ViewPlayers(p *Player) []*Player
}

// FlatResolver is the v1 interest implementation (spec S19.1): fanout
// is the full player set. all supplies the current players — the
// InterestTracker wires it to its own registry.
type FlatResolver struct {
	all func() []*Player
}

// NewFlatResolver builds a FlatResolver reading the player set from all.
func NewFlatResolver(all func() []*Player) *FlatResolver {
	return &FlatResolver{all: all}
}

// ViewPlayers returns every player except p (S19.1).
func (f *FlatResolver) ViewPlayers(p *Player) []*Player {
	out := make([]*Player, 0, 8)
	for _, q := range f.all() {
		if q != p && q.ID != p.ID {
			out = append(out, q)
		}
	}
	return out
}

// EventKind discriminates spawn from despawn events.
type EventKind uint8

const (
	// SpawnEvent: Targets must SpawnEntity(Subject) (S18.2).
	SpawnEvent EventKind = iota
	// DespawnEvent: Targets must DespawnEntity(Subject) (S18.2).
	DespawnEvent
)

var eventKindNames = [...]string{"spawn", "despawn"}

// String returns the stable event name.
func (k EventKind) String() string {
	if int(k) < len(eventKindNames) {
		return eventKindNames[k]
	}
	return "unknown"
}

// InterestEvent is one spawn/despawn decision: the Targets must spawn
// (or despawn) the Subject. The wiring layer turns it into TCP frames;
// in v1 the spawn payload is the entity's state (WorldSnapshot), which
// the InterestTracker does not need to know about.
type InterestEvent struct {
	Kind    EventKind
	Subject string
	Targets []string
}

// InterestTracker maintains each player's cell and emits the spawn/
// despawn events caused by joins, leaves and cell crossings. It is
// resolver-agnostic — the same tracker drives flat v1 and a future
// chunk resolver (S19.2).
type InterestTracker struct {
	resolver InterestResolver
	all      []*Player
	byID     map[string]int
}

// NewInterestTracker builds a tracker. A nil resolver selects the v1
// FlatResolver wired to the tracker's own player registry.
func NewInterestTracker(resolver InterestResolver) *InterestTracker {
	t := &InterestTracker{byID: make(map[string]int)}
	if resolver == nil {
		t.resolver = NewFlatResolver(func() []*Player { return t.all })
	} else {
		t.resolver = resolver
	}
	return t
}

// allPlayers exposes the current player slice (read-only by convention)
// to resolvers and tests.
func (t *InterestTracker) allPlayers() []*Player { return t.all }

// PlayerCount reports the number of tracked players.
func (t *InterestTracker) PlayerCount() int { return len(t.all) }

// CellOf returns the cell currently tracked for id.
func (t *InterestTracker) CellOf(id string) (Cell, bool) {
	p, ok := t.playerByID(id)
	if !ok {
		return Cell{}, false
	}
	return p.Cell, true
}

// playerByID resolves a tracked player.
func (t *InterestTracker) playerByID(id string) (*Player, bool) {
	i, ok := t.byID[id]
	if !ok || i >= len(t.all) {
		return nil, false
	}
	return t.all[i], true
}

// Update registers a new player or moves an existing one to (x, z),
// emitting the interest events caused:
//
//   - join:  every player interested in the newcomer must spawn it
//     (v1 flat: everyone existing; chunk: only viewers in range).
//   - crossing: players who gained the mover in their view must spawn
//     it; players who lost it must despawn it (S18.2).
//
// The reciprocal direction — the mover spawning players it newly sees —
// is delivered as initial world state by the wiring in v1 (flat: the
// newcomer's WorldSnapshot already carries everyone).
func (t *InterestTracker) Update(id string, x, z float32) []InterestEvent {
	cell := CellAt(x, z)
	p, known := t.playerByID(id)
	if !known {
		p = &Player{ID: id, Cell: cell}
		t.all = append(t.all, p)
		t.byID[id] = len(t.all) - 1
		after := t.filterSelf(id, t.resolver.ViewPlayers(p))
		if len(after) > 0 {
			return []InterestEvent{{Kind: SpawnEvent, Subject: id, Targets: after}}
		}
		return nil
	}
	if p.Cell == cell {
		return nil // no crossing
	}
	old := p.Cell
	p.Cell = cell
	after := t.filterSelf(id, t.resolver.ViewPlayers(p))
	p.Cell = old
	before := t.filterSelf(id, t.resolver.ViewPlayers(p))
	p.Cell = cell

	entered, left := diffTargets(before, after)
	var events []InterestEvent
	if len(entered) > 0 {
		events = append(events, InterestEvent{Kind: SpawnEvent, Subject: id, Targets: entered})
	}
	if len(left) > 0 {
		events = append(events, InterestEvent{Kind: DespawnEvent, Subject: id, Targets: left})
	}
	return events
}

// Remove unregisters a player and returns who must despawn it: every
// player that was still interested while it was present (S18.2 leave).
// Removing an unknown id is a no-op.
func (t *InterestTracker) Remove(id string) []InterestEvent {
	p, known := t.playerByID(id)
	if !known {
		return nil
	}
	before := t.filterSelf(id, t.resolver.ViewPlayers(p))

	// Swap-remove keeps the registry compact.
	idx := t.byID[id]
	last := len(t.all) - 1
	t.all[idx] = t.all[last]
	t.all = t.all[:last]
	if idx != last {
		t.byID[t.all[idx].ID] = idx
	}
	delete(t.byID, id)

	if len(before) > 0 {
		return []InterestEvent{{Kind: DespawnEvent, Subject: id, Targets: before}}
	}
	return nil
}

// filterSelf guards the resolver contract: the subject must never
// appear in its own target set.
func (t *InterestTracker) filterSelf(id string, players []*Player) []string {
	out := make([]string, 0, len(players))
	for _, p := range players {
		if p != nil && p.ID != id {
			out = append(out, p.ID)
		}
	}
	return out
}

// diffTargets returns the players newly interested (entered) and the
// players that lost interest (left) when a subject moves.
func diffTargets(before, after []string) (entered, left []string) {
	beforeSet := make(map[string]bool, len(before))
	for _, id := range before {
		beforeSet[id] = true
	}
	afterSet := make(map[string]bool, len(after))
	for _, id := range after {
		afterSet[id] = true
	}
	for _, id := range after {
		if !beforeSet[id] {
			entered = append(entered, id)
		}
	}
	for _, id := range before {
		if !afterSet[id] {
			left = append(left, id)
		}
	}
	return entered, left
}

// String returns a compact human-readable form of an event.
func (e InterestEvent) String() string {
	return e.Kind.String() + " " + e.Subject + " -> [" + strings.Join(e.Targets, " ") + "]"
}
