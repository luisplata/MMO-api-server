package character

// Character type + repository-contract tests (design D1/D2/D3, spec
// character-management).
//
// Coverage: the Character shape carries id/accountId/name/templateId/plus
// a frozen stats snapshot and createdAt, and round-trips through JSON (the
// runtime store data/characters.json). The CharacterRepository interface
// (Create/ListByAccount/Get/FindByName) is behaviorally exercised by
// file_test.go through the concrete fileCharacterRepository; this file pins
// the Character value shape and the interface contract.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/stats"
)

// TestCharacterFieldAccess verifies each field is independently settable
// and readable — the store exposes id/accountId/name/templateId/stats/
// createdAt.
func TestCharacterFieldAccess(t *testing.T) {
	created := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)
	c := Character{
		ID:         "char_1",
		AccountID:  "luis",
		Name:       "Mago_Principal",
		TemplateID: "mage",
		Stats:      stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4},
		CreatedAt:  created,
	}
	if c.ID != "char_1" {
		t.Errorf("ID = %q, want %q", c.ID, "char_1")
	}
	if c.AccountID != "luis" {
		t.Errorf("AccountID = %q, want %q", c.AccountID, "luis")
	}
	if c.Name != "Mago_Principal" {
		t.Errorf("Name = %q, want %q", c.Name, "Mago_Principal")
	}
	if c.TemplateID != "mage" {
		t.Errorf("TemplateID = %q, want %q", c.TemplateID, "mage")
	}
	if c.Stats != (stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4}) {
		t.Errorf("Stats = %+v, want {80 4 14 4}", c.Stats)
	}
	if !c.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", c.CreatedAt, created)
	}
}

// TestCharacterJSONRoundTrip verifies the JSON wire shape of the runtime
// store: field names id/accountId/name/templateId/stats/createdAt (stats
// carries the lowercase hp/speed/atk/def shape), and a full
// marshal→unmarshal cycle reproduces the original character. Triangulation:
// two distinct characters with different stats and template.
func TestCharacterJSONRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   Character
	}{
		{
			name: "mage",
			in: Character{
				ID:         "char_1",
				AccountID:  "luis",
				Name:       "Mago_Principal",
				TemplateID: "mage",
				Stats:      stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4},
				CreatedAt:  created,
			},
		},
		{
			name: "warrior",
			in: Character{
				ID:         "char_2",
				AccountID:  "ana",
				Name:       "guerrero_tanque",
				TemplateID: "warrior",
				Stats:      stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8},
				CreatedAt:  created,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(data)
			for _, want := range []string{`"id":`, `"accountId":`, `"name":`, `"templateId":`, `"stats":`, `"createdAt":`, `"hp":`} {
				if !strings.Contains(got, want) {
					t.Errorf("marshaled %s must contain %s", got, want)
				}
			}
			var out Character
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out.ID != tc.in.ID || out.AccountID != tc.in.AccountID ||
				out.Name != tc.in.Name || out.TemplateID != tc.in.TemplateID ||
				out.Stats != tc.in.Stats || !out.CreatedAt.Equal(tc.in.CreatedAt) {
				t.Errorf("round-trip = %+v, want %+v", out, tc.in)
			}
		})
	}
}
