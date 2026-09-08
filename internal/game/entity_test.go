package game

// Phase 5 (design D3/D5): the world entity is keyed by the selected
// character id and carries the template identity + frozen stats. These
// tests pin that RegisterPlayer stores the template/stats on the Entity,
// and that the shared wire mappers (entityState, and therefore the
// snapshot/WorldSnapshot assemblers) propagate templateId onto the wire.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/stats"
)

// TestRegisterPlayerCarriesTemplateAndStats pins 5.1: RegisterPlayer
// must record the template id and the frozen stats snapshot on the
// entity (design D2/D3/D5). The result is non-trivial — a real entity
// is produced and inspected.
func TestRegisterPlayerCarriesTemplateAndStats(t *testing.T) {
	sim := newSim(t, nil)
	st := stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}
	if err := sim.RegisterPlayer("char-1", Vec2{100, 200}, "warrior", st); err != nil {
		t.Fatalf("RegisterPlayer: %v", err)
	}
	e, ok := sim.Entity("char-1")
	if !ok {
		t.Fatal("char-1 not registered in the simulation")
	}
	if e.TemplateID != "warrior" {
		t.Errorf("TemplateID = %q, want warrior", e.TemplateID)
	}
	if e.Stats != st {
		t.Errorf("Stats = %+v, want %+v (frozen snapshot)", e.Stats, st)
	}
}

// TestRegisterPlayerCarriesTemplateAndStatsDifferentVector triangulates:
// a second character with a different template/stats must be stored
// independently — a hardcoded "warrior" would fail here.
func TestRegisterPlayerCarriesTemplateAndStatsDifferentVector(t *testing.T) {
	sim := newSim(t, nil)
	mage := stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4}
	if err := sim.RegisterPlayer("char-2", Vec2{0, 0}, "mage", mage); err != nil {
		t.Fatalf("RegisterPlayer: %v", err)
	}
	e, ok := sim.Entity("char-2")
	if !ok {
		t.Fatal("char-2 not registered")
	}
	if e.TemplateID != "mage" {
		t.Errorf("TemplateID = %q, want mage", e.TemplateID)
	}
	if e.Stats != mage {
		t.Errorf("Stats = %+v, want %+v", e.Stats, mage)
	}
}

// TestEntityStateCarriesTemplateId pins 5.2: the shared entity mapper
// propagates the entity's template id onto the wire EntityState, so the
// Unity client can pick the visual prefab (design D5, spec
// world-protocol "templateId en estado").
func TestEntityStateCarriesTemplateId(t *testing.T) {
	e := &Entity{ID: "char-1", TemplateID: "warrior"}
	es := entityState(e)
	if es.TemplateId != "warrior" {
		t.Errorf("EntityState.templateId = %q, want warrior", es.TemplateId)
	}
}

// TestEntityStateCarriesTemplateIdEmpty triangulates the empty case: an
// entity with no template (a non-character entity) MUST carry an empty
// templateId — presence is explicit, absence is the zero value.
func TestEntityStateCarriesTemplateIdEmpty(t *testing.T) {
	e := &Entity{ID: "npc-1"}
	es := entityState(e)
	if es.TemplateId != "" {
		t.Errorf("EntityState.templateId = %q, want empty for a template-less entity", es.TemplateId)
	}
}

// TestWorldSnapshotCarriesTemplateId pins the end-to-end shape: the
// enter-world WorldSnapshot goes through the same entityState mapper, so
// a player registered with a template appears with its templateId in the
// enter-world payload (design D5, spec "Spawn según personaje").
func TestWorldSnapshotCarriesTemplateId(t *testing.T) {
	sim := newSim(t, nil)
	st := stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}
	if err := sim.RegisterPlayer("char-1", Vec2{100, 200}, "warrior", st); err != nil {
		t.Fatalf("RegisterPlayer: %v", err)
	}
	ws := sim.AssembleWorldSnapshot()
	if len(ws.Entities) != 1 {
		t.Fatalf("WorldSnapshot entities = %d, want 1", len(ws.Entities))
	}
	if ws.Entities[0].Id != "char-1" {
		t.Errorf("WorldSnapshot id = %q, want char-1", ws.Entities[0].Id)
	}
	if ws.Entities[0].TemplateId != "warrior" {
		t.Errorf("WorldSnapshot templateId = %q, want warrior", ws.Entities[0].TemplateId)
	}
}
