package stats

// Stats type tests (design D6, spec character-templates).
//
// Coverage: the Stats shape carries exactly the four base attributes a
// template defines (hp/speed/atk/def) as float32, and it must round-trip
// through JSON because the server loads them from data/templates.json —
// never from hardcoded Go constants (spec "Stats nunca hardcodeadas").

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStatsZeroValue pins the zero value: an empty Stats must read all
// zeros so a template that omits a base stat is not accidentally filled
// by the server.
func TestStatsZeroValue(t *testing.T) {
	var s Stats
	if s.HP != 0 || s.Speed != 0 || s.Atk != 0 || s.Def != 0 {
		t.Fatalf("zero Stats = %+v, want all fields 0", s)
	}
}

// TestStatsFieldAccess verifies each field is independently settable and
// readable — the server reads hp/speed/atk/def individually when it
// resolves a character's stats from a template.
func TestStatsFieldAccess(t *testing.T) {
	s := Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}
	if s.HP != 120 {
		t.Errorf("HP = %v, want 120", s.HP)
	}
	if s.Speed != 3 {
		t.Errorf("Speed = %v, want 3", s.Speed)
	}
	if s.Atk != 10 {
		t.Errorf("Atk = %v, want 10", s.Atk)
	}
	if s.Def != 8 {
		t.Errorf("Def = %v, want 8", s.Def)
	}
}

// TestStatsJSONRoundTrip verifies the JSON wire shape used by
// data/templates.json: the field names must be the lowercase
// hp/speed/atk/def (matching the proto Stats message and the seed
// catalog), and a full marshal→unmarshal cycle must reproduce the
// original values. Triangulation: three distinct stat vectors.
func TestStatsJSONRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   Stats
	}{
		{"warrior", Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}},
		{"mage", Stats{HP: 80, Speed: 4, Atk: 14, Def: 4}},
		{"ranger", Stats{HP: 90, Speed: 5, Atk: 9, Def: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(data)
			for _, want := range []string{`"hp":`, `"speed":`, `"atk":`, `"def":`} {
				if !strings.Contains(got, want) {
					t.Errorf("marshaled %s must contain %s", got, want)
				}
			}
			var out Stats
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out != tc.in {
				t.Errorf("round-trip = %+v, want %+v", out, tc.in)
			}
		})
	}
}

// TestStatsJSONUnknownFieldsIgnored pins that extra JSON fields do not
// corrupt the struct — the catalog is additive-friendly, so a template
// that later gains a field must still load into the current Stats shape.
func TestStatsJSONUnknownFieldsIgnored(t *testing.T) {
	data := []byte(`{"hp":50,"speed":2,"atk":7,"def":3,"luck":99}`)
	var s Stats
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal with unknown field: %v", err)
	}
	if s.HP != 50 || s.Speed != 2 || s.Atk != 7 || s.Def != 3 {
		t.Errorf("Stats = %+v, want {50 2 7 3}", s)
	}
}
