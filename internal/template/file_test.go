package template

// File-template repository tests (design D1, spec character-templates).
//
// Coverage: the file-backed TemplateRepository on data/templates.json
// loads a valid catalog (Get + All), fails fast on corrupt JSON, on
// duplicate template ids, and on malformed (empty-id) entries, and
// reports an unknown id via ErrNotFound. The committed seed catalog at
// the repo root is also loaded to prove the shipped data file is valid.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/luisplata/mmo-api-server/internal/stats"
)

// writeCatalog writes raw content to a temp file and returns its path.
func writeCatalog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "templates.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}

// repoRoot walks up from the test working directory until it finds
// go.mod — the repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the package working directory")
		}
		dir = parent
	}
}

// TestNewFileTemplateRepositoryLoadsValidCatalog covers the happy path:
// a valid array catalog is loaded, each template is reachable by id with
// its stats base and visualHint, and All returns every entry in file
// order (triangulated across two templates with distinct stats).
func TestNewFileTemplateRepositoryLoadsValidCatalog(t *testing.T) {
	path := writeCatalog(t, `[
		{"id":"warrior","name":"Guerrero","baseStats":{"hp":120,"speed":3,"atk":10,"def":8},"visualHint":"warrior_model"},
		{"id":"mage","name":"Mago","baseStats":{"hp":80,"speed":4,"atk":14,"def":4},"visualHint":"mage_model"}
	]`)
	repo, err := NewFileTemplateRepository(path)
	if err != nil {
		t.Fatalf("NewFileTemplateRepository: %v", err)
	}

	w, err := repo.Get("warrior")
	if err != nil {
		t.Fatalf("Get(warrior): %v", err)
	}
	if w.ID != "warrior" || w.Name != "Guerrero" || w.VisualHint != "warrior_model" {
		t.Errorf("Get(warrior) = %+v, want id/name/hint warrior/Guerrero/warrior_model", w)
	}
	if w.BaseStats != (stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}) {
		t.Errorf("Get(warrior).BaseStats = %+v, want {120 3 10 8}", w.BaseStats)
	}

	all, err := repo.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("All len = %d, want 2", len(all))
	}
	if all[0].ID != "warrior" || all[1].ID != "mage" {
		t.Errorf("All order = [%s, %s], want [warrior, mage]", all[0].ID, all[1].ID)
	}
	if all[1].BaseStats != (stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4}) {
		t.Errorf("all[1].BaseStats = %+v, want {80 4 14 4}", all[1].BaseStats)
	}
}

// TestNewFileTemplateRepositoryRejectsCorruptJSON covers the fail-fast
// corrupt class: syntactically invalid JSON and a well-formed JSON value
// that is not an array of templates must both fail with ErrInvalidJSON.
func TestNewFileTemplateRepositoryRejectsCorruptJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"garbage text", `not json at all`},
		{"truncated object", `[{"id":"warrior"`},
		{"object instead of array", `{"warrior":{"name":"Guerrero"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFileTemplateRepository(writeCatalog(t, tc.content))
			if !errors.Is(err, ErrInvalidJSON) {
				t.Errorf("err = %v, want ErrInvalidJSON", err)
			}
		})
	}
}

// TestNewFileTemplateRepositoryRejectsDuplicateID covers the fail-fast
// duplicate class: two entries sharing an id must fail with
// ErrDuplicateID and never expose a partial catalog.
func TestNewFileTemplateRepositoryRejectsDuplicateID(t *testing.T) {
	path := writeCatalog(t, `[
		{"id":"warrior","name":"Guerrero","baseStats":{"hp":120,"speed":3,"atk":10,"def":8},"visualHint":"a"},
		{"id":"warrior","name":"Clon","baseStats":{"hp":1,"speed":1,"atk":1,"def":1},"visualHint":"b"}
	]`)
	_, err := NewFileTemplateRepository(path)
	if !errors.Is(err, ErrDuplicateID) {
		t.Errorf("err = %v, want ErrDuplicateID", err)
	}
}

// TestNewFileTemplateRepositoryRejectsEmptyID covers a malformed entry: a
// template with no id (or no name) cannot be addressed, so the catalog is
// rejected with ErrInvalidTemplate.
func TestNewFileTemplateRepositoryRejectsEmptyID(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty id", `[{"id":"","name":"x","baseStats":{"hp":1},"visualHint":"x"}]`},
		{"empty name", `[{"id":"x","name":"","baseStats":{"hp":1},"visualHint":"x"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFileTemplateRepository(writeCatalog(t, tc.content))
			if !errors.Is(err, ErrInvalidTemplate) {
				t.Errorf("err = %v, want ErrInvalidTemplate", err)
			}
		})
	}
}

// TestNewFileTemplateRepositoryEmptyCatalog verifies an empty array is a
// valid (if useless) catalog: it loads without error and All returns an
// empty slice — no partial-state panic, no phantom templates.
func TestNewFileTemplateRepositoryEmptyCatalog(t *testing.T) {
	repo, err := NewFileTemplateRepository(writeCatalog(t, `[]`))
	if err != nil {
		t.Fatalf("NewFileTemplateRepository: %v", err)
	}
	all, err := repo.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("All len = %d, want 0", len(all))
	}
}

// TestFileTemplateRepositoryGetUnknown verifies an unknown id resolves to
// ErrNotFound (spec: templateId inexistente must be rejected).
func TestFileTemplateRepositoryGetUnknown(t *testing.T) {
	repo, err := NewFileTemplateRepository(writeCatalog(t, `[{"id":"warrior","name":"Guerrero","baseStats":{"hp":1},"visualHint":"m"}]`))
	if err != nil {
		t.Fatalf("NewFileTemplateRepository: %v", err)
	}
	if _, err := repo.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(nope) err = %v, want ErrNotFound", err)
	}
}

// TestCommittedCatalogLoads loads the real committed seed catalog at the
// repo root (data/templates.json) through the same loader path the server
// uses, proving the shipped data file is valid and non-empty (task 2.4).
func TestCommittedCatalogLoads(t *testing.T) {
	repo, err := NewFileTemplateRepository(filepath.Join(repoRoot(t), "data", "templates.json"))
	if err != nil {
		t.Fatalf("committed catalog: %v", err)
	}
	all, err := repo.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("committed catalog is empty")
	}
	for _, tpl := range all {
		if tpl.ID == "" || tpl.Name == "" {
			t.Errorf("committed template has empty id/name: %+v", tpl)
		}
		if _, err := repo.Get(tpl.ID); err != nil {
			t.Errorf("committed template %q not reachable by id: %v", tpl.ID, err)
		}
	}
}
