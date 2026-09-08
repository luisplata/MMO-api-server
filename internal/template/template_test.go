package template

// Template contract tests (design D1/D6, spec character-templates).
//
// Coverage: the Template shape carries id/name/baseStats/visualHint and
// round-trips through JSON (the seed catalog is loaded from
// data/templates.json). The TemplateRepository interface (Get/All) is
// behaviorally exercised by file_test.go through the concrete
// fileTemplateRepository; this file pins the Template value shape.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luisplata/mmo-api-server/internal/stats"
)

// TestTemplateFieldAccess verifies each field is independently settable
// and readable — the catalog exposes id/name/baseStats/visualHint.
func TestTemplateFieldAccess(t *testing.T) {
	tpl := Template{
		ID:         "warrior",
		Name:       "Guerrero",
		BaseStats:  stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8},
		VisualHint: "warrior_model",
	}
	if tpl.ID != "warrior" {
		t.Errorf("ID = %q, want %q", tpl.ID, "warrior")
	}
	if tpl.Name != "Guerrero" {
		t.Errorf("Name = %q, want %q", tpl.Name, "Guerrero")
	}
	if tpl.BaseStats != (stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}) {
		t.Errorf("BaseStats = %+v, want {120 3 10 8}", tpl.BaseStats)
	}
	if tpl.VisualHint != "warrior_model" {
		t.Errorf("VisualHint = %q, want %q", tpl.VisualHint, "warrior_model")
	}
}

// TestTemplateJSONRoundTrip verifies the JSON wire shape of the seed
// catalog: field names id/name/baseStats/visualHint (baseStats carries
// the lowercase hp/speed/atk/def stats shape), and a full
// marshal→unmarshal cycle reproduces the original template.
func TestTemplateJSONRoundTrip(t *testing.T) {
	in := Template{
		ID:         "mage",
		Name:       "Mago",
		BaseStats:  stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4},
		VisualHint: "mage_model",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, want := range []string{`"id":`, `"name":`, `"baseStats":`, `"visualHint":`, `"hp":`} {
		if !strings.Contains(got, want) {
			t.Errorf("marshaled %s must contain %s", got, want)
		}
	}
	var out Template
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip = %+v, want %+v", out, in)
	}
}
