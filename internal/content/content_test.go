package content

// Canonical content validation tests (M1 alignment gate).
//
// The real data/canonical/content.json is the acceptance fixture: it must
// parse and validate, and every documented corruption class must be
// rejected with ErrInvalidContent. Corruptions are produced by mutating the
// real artifact's generic JSON tree, so the tests exercise the exact bytes
// the client shipped rather than a synthetic schema.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// canonicalDir locates data/canonical from this file so the tests read the
// same committed artifacts the deployed flags default to.
func canonicalDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Join(filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), "data", "canonical")
}

// canonicalBytes reads one committed canonical artifact.
func canonicalBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(canonicalDir(t), name))
	if err != nil {
		t.Fatalf("read canonical %s: %v", name, err)
	}
	return data
}

// rawContent decodes the real content artifact into a mutable JSON tree.
func rawContent(t *testing.T) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(canonicalBytes(t, "content.json"), &raw); err != nil {
		t.Fatalf("decode content.json: %v", err)
	}
	return raw
}

// marshalContent re-encodes a mutated JSON tree.
func marshalContent(t *testing.T, raw map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode mutated content: %v", err)
	}
	return data
}

// ability returns the mutable ability object for id.
func ability(t *testing.T, raw map[string]any, id string) map[string]any {
	t.Helper()
	abilities, ok := raw["abilities"].(map[string]any)
	if !ok {
		t.Fatalf("content.json abilities is %T, want object", raw["abilities"])
	}
	a, ok := abilities[id].(map[string]any)
	if !ok {
		t.Fatalf("ability %q missing from artifact", id)
	}
	return a
}

// TestParseContentCanonicalArtifact pins the real client-delivered content:
// version, 8 classes in canonical order, 40 abilities keyed by id, and the
// 21 declared primitives.
func TestParseContentCanonicalArtifact(t *testing.T) {
	c, err := ParseContent(canonicalBytes(t, "content.json"))
	if err != nil {
		t.Fatalf("ParseContent(canonical): %v", err)
	}
	if c.ContentVersion != "731e2a2d" {
		t.Errorf("contentVersion = %q, want 731e2a2d", c.ContentVersion)
	}
	if c.Meta.SourceCommit != "3c986f7" {
		t.Errorf("sourceCommit = %q, want 3c986f7", c.Meta.SourceCommit)
	}
	if got := len(c.Classes); got != 8 {
		t.Fatalf("classes = %d, want 8", got)
	}
	if got := len(c.Abilities); got != 40 {
		t.Fatalf("abilities = %d, want 40", got)
	}
	if got := len(c.Meta.Primitives); got != 21 {
		t.Fatalf("primitives = %d, want 21", got)
	}
	wantOrder := []string{"terravox", "umbraclaw", "psyflame", "floraviva", "campeon", "mortero", "franco", "disparador"}
	for i, want := range wantOrder {
		if c.Classes[i].Key != want {
			t.Errorf("classes[%d].key = %q, want %q", i, c.Classes[i].Key, want)
		}
		if c.Classes[i].ClassID != i+1 {
			t.Errorf("classes[%d].classId = %d, want %d", i, c.Classes[i].ClassID, i+1)
		}
	}
	terravox := c.Classes[0]
	if terravox.Basic == nil || terravox.Basic.Kind != "melee" {
		t.Fatalf("terravox basic = %+v, want melee", terravox.Basic)
	}
	if terravox.HP != 1500 || terravox.SpeedU != 8 || terravox.RadiusPx != 30 {
		t.Errorf("terravox stats = hp %d speedU %v radius %d, want 1500/8/30",
			terravox.HP, terravox.SpeedU, terravox.RadiusPx)
	}
	if got := len(terravox.Kit); got != 3 {
		t.Errorf("terravox kit = %d ids, want 3", got)
	}

	slam := c.Abilities["seismic-slam"]
	if slam.ID != "seismic-slam" {
		t.Errorf("abilities[seismic-slam].id = %q", slam.ID)
	}
	if slam.CD != 6 {
		t.Errorf("seismic-slam cd = %v, want 6", slam.CD)
	}
	if slam.PP == nil || *slam.PP != 15 {
		t.Errorf("seismic-slam pp = %v, want 15", slam.PP)
	}
	if _, ok := c.Abilities["spore-charge"]; !ok {
		t.Fatal("spore-charge missing from abilities map")
	}
	if pp := c.Abilities["spore-charge"].PP; pp != nil {
		t.Errorf("spore-charge pp = %v, want absent (infinite)", *pp)
	}

	if got := c.Types.Multipliers; got.Strong != 1.4 || got.Weak != 0.72 || got.Neutral != 1 {
		t.Errorf("multipliers = %+v, want 1.4/0.72/1", got)
	}
	if got := len(c.Types.Beats); got != 8 {
		t.Errorf("types.beats keys = %d, want 8", got)
	}
}

// TestParseContentRejectsCorruptions is the fail-fast table: each documented
// corruption class must be wrapped with ErrInvalidContent.
func TestParseContentRejectsCorruptions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, raw map[string]any)
	}{
		{"wrong class count", func(t *testing.T, raw map[string]any) {
			classes := raw["classes"].([]any)
			raw["classes"] = classes[:7]
		}},
		{"duplicate classId", func(t *testing.T, raw map[string]any) {
			raw["classes"].([]any)[1].(map[string]any)["classId"] = 1
		}},
		{"kit id missing", func(t *testing.T, raw map[string]any) {
			kit := raw["classes"].([]any)[0].(map[string]any)["kit"].([]any)
			kit[0] = "ghost-ability"
		}},
		{"unknown primitive", func(t *testing.T, raw map[string]any) {
			effects := ability(t, raw, "seismic-slam")["effects"].([]any)
			effects[0].(map[string]any)["do"] = "not-a-primitive"
		}},
		{"bad multiplier", func(t *testing.T, raw map[string]any) {
			types := raw["types"].(map[string]any)
			types["multipliers"].(map[string]any)["strong"] = 1
		}},
		{"negative pp", func(t *testing.T, raw map[string]any) {
			ability(t, raw, "seismic-slam")["pp"] = -1
		}},
		{"empty effects", func(t *testing.T, raw map[string]any) {
			ability(t, raw, "seismic-slam")["effects"] = []any{}
		}},
		{"wrong classOrder", func(t *testing.T, raw map[string]any) {
			order := raw["meta"].(map[string]any)["classOrder"].([]any)
			order[0] = "wrong-class"
		}},
		{"missing sourceCommit", func(t *testing.T, raw map[string]any) {
			raw["meta"].(map[string]any)["sourceCommit"] = ""
		}},
		{"empty contentVersion", func(t *testing.T, raw map[string]any) {
			raw["contentVersion"] = ""
		}},
		{"duplicate mapped ability id", func(t *testing.T, raw map[string]any) {
			ability(t, raw, "seismic-slam")["id"] = "earthquake"
		}},
		{"primitive duplicate", func(t *testing.T, raw map[string]any) {
			prims := raw["meta"].(map[string]any)["primitives"].([]any)
			raw["meta"].(map[string]any)["primitives"] = append(prims, prims[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawContent(t)
			tc.mutate(t, raw)
			if _, err := ParseContent(marshalContent(t, raw)); !errors.Is(err, ErrInvalidContent) {
				t.Fatalf("ParseContent() error = %v, want ErrInvalidContent", err)
			}
		})
	}
}

// TestParseContentRejectsInvalidJSON pins that malformed JSON is also an
// ErrInvalidContent failure, never a zero-value Content.
func TestParseContentRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseContent([]byte("{not json")); !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("ParseContent(malformed) error = %v, want ErrInvalidContent", err)
	}
}

// TestLoadContentReadsFromDisk pins the path loader and its missing-file
// contract.
func TestLoadContentReadsFromDisk(t *testing.T) {
	c, err := LoadContent(filepath.Join(canonicalDir(t), "content.json"))
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if c.ContentVersion != "731e2a2d" || len(c.Classes) != 8 {
		t.Errorf("LoadContent returned %q/%d classes, want 731e2a2d/8", c.ContentVersion, len(c.Classes))
	}
	if _, err := LoadContent(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadContent(missing) must fail")
	}
}
