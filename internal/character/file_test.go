package character

// File-character repository tests (design D1, spec character-management).
//
// Coverage: the file-backed CharacterRepository on data/characters.json
// loads an empty/missing store, creates characters with generated stable
// ids, enforces id uniqueness and per-account name uniqueness, lists per
// account, finds by name, reports an unknown id, rejects corrupt JSON, and
// writes atomically (tmp+rename) so a reload sees the persisted character.
// The committed runtime store at the repo root is loaded to prove the
// shipped file is valid.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/luisplata/mmo-api-server/internal/stats"
)

// writeStore writes raw content to a temp file and returns its path.
func writeStore(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "characters.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}
	return path
}

// newRepo returns a fresh CharacterRepository backed by a temp file.
func newRepo(t *testing.T) CharacterRepository {
	t.Helper()
	r, err := NewFileCharacterRepository(filepath.Join(t.TempDir(), "characters.json"))
	if err != nil {
		t.Fatalf("NewFileCharacterRepository: %v", err)
	}
	return r
}

// repoRoot walks up from the test working directory until it finds go.mod —
// the repository root.
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

// TestNewFileCharacterRepositoryEmptyMissingFile covers a brand-new store
// whose file does not exist yet: it must load as an empty store (no error)
// and ListByAccount must return an empty, non-nil slice — the account has no
// characters (spec "Cuenta vacía": CharacterList vacío, no error).
func TestNewFileCharacterRepositoryEmptyMissingFile(t *testing.T) {
	repo, err := NewFileCharacterRepository(filepath.Join(t.TempDir(), "characters.json"))
	if err != nil {
		t.Fatalf("missing store file should not error: %v", err)
	}
	chars, err := repo.ListByAccount("luis")
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if chars == nil {
		t.Fatalf("ListByAccount returned nil slice, want empty non-nil")
	}
	if len(chars) != 0 {
		t.Fatalf("ListByAccount len = %d, want 0", len(chars))
	}
}

// TestFileRepositoryCreateAndGet covers the happy path: a character with a
// valid (account, name, template) is created, an id is assigned, and Get
// retrieves it with its frozen stats snapshot intact (design D2).
func TestFileRepositoryCreateAndGet(t *testing.T) {
	repo := newRepo(t)
	c := &Character{
		AccountID:  "luis",
		Name:       "mago_1",
		TemplateID: "mage",
		Stats:      stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4},
	}
	if err := repo.Create(c); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID == "" {
		t.Fatalf("Create must assign a non-empty id when none is provided")
	}
	got, err := repo.Get(c.ID)
	if err != nil {
		t.Fatalf("Get(%q): %v", c.ID, err)
	}
	if got.AccountID != "luis" || got.Name != "mago_1" || got.TemplateID != "mage" {
		t.Errorf("Get = %+v, want account luis / name mago_1 / template mage", got)
	}
	if got.Stats != (stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4}) {
		t.Errorf("Get Stats = %+v, want {80 4 14 4}", got.Stats)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("Create must set CreatedAt, got zero time")
	}
}

// TestFileRepositoryRespectsProvidedID verifies a caller-supplied id is kept
// (not regenerated) and remains retrievable — ids are the stable entity
// identity (design D3).
func TestFileRepositoryRespectsProvidedID(t *testing.T) {
	repo := newRepo(t)
	c := &Character{ID: "char_fixed", AccountID: "luis", Name: "hero", TemplateID: "warrior"}
	if err := repo.Create(c); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID != "char_fixed" {
		t.Fatalf("ID was overwritten = %q, want %q", c.ID, "char_fixed")
	}
	got, err := repo.Get("char_fixed")
	if err != nil {
		t.Fatalf("Get(char_fixed): %v", err)
	}
	if got.ID != "char_fixed" || got.Name != "hero" {
		t.Errorf("Get = %+v, want id char_fixed / name hero", got)
	}
}

// TestFileRepositoryGeneratesUniqueIDs triangulates id generation: two
// characters created without an id must get distinct ids, and each must be
// retrievable by its own id.
func TestFileRepositoryGeneratesUniqueIDs(t *testing.T) {
	repo := newRepo(t)
	c1 := &Character{AccountID: "luis", Name: "a", TemplateID: "warrior"}
	c2 := &Character{AccountID: "luis", Name: "b", TemplateID: "mage"}
	if err := repo.Create(c1); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if err := repo.Create(c2); err != nil {
		t.Fatalf("Create c2: %v", err)
	}
	if c1.ID == "" || c2.ID == "" {
		t.Fatalf("both ids must be non-empty, got %q and %q", c1.ID, c2.ID)
	}
	if c1.ID == c2.ID {
		t.Fatalf("ids must be unique, both = %q", c1.ID)
	}
	if _, err := repo.Get(c1.ID); err != nil {
		t.Errorf("Get(%q): %v", c1.ID, err)
	}
	if _, err := repo.Get(c2.ID); err != nil {
		t.Errorf("Get(%q): %v", c2.ID, err)
	}
}

// TestFileRepositoryDuplicateID covers uniqueness on the entity id: two
// characters sharing a stored id must fail with ErrDuplicateID and leave the
// store unchanged.
func TestFileRepositoryDuplicateID(t *testing.T) {
	repo := newRepo(t)
	c1 := &Character{ID: "same", AccountID: "luis", Name: "a", TemplateID: "warrior"}
	c2 := &Character{ID: "same", AccountID: "ana", Name: "b", TemplateID: "mage"}
	if err := repo.Create(c1); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if err := repo.Create(c2); !errors.Is(err, ErrDuplicateID) {
		t.Errorf("Create c2 err = %v, want ErrDuplicateID", err)
	}
}

// TestFileRepositoryDuplicateNameSameAccount covers per-account name
// uniqueness (design D7): a second character in the SAME account using an
// already-used name must fail with ErrDuplicateName.
func TestFileRepositoryDuplicateNameSameAccount(t *testing.T) {
	repo := newRepo(t)
	c1 := &Character{AccountID: "luis", Name: "hero", TemplateID: "warrior"}
	c2 := &Character{AccountID: "luis", Name: "hero", TemplateID: "mage"}
	if err := repo.Create(c1); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if err := repo.Create(c2); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("Create c2 err = %v, want ErrDuplicateName", err)
	}
}

// TestFileRepositorySameNameDifferentAccount triangulates the uniqueness
// rule: the SAME name is allowed in a DIFFERENT account (uniqueness is per
// account, not global).
func TestFileRepositorySameNameDifferentAccount(t *testing.T) {
	repo := newRepo(t)
	c1 := &Character{AccountID: "luis", Name: "hero", TemplateID: "warrior"}
	c2 := &Character{AccountID: "ana", Name: "hero", TemplateID: "mage"}
	if err := repo.Create(c1); err != nil {
		t.Fatalf("Create c1: %v", err)
	}
	if err := repo.Create(c2); err != nil {
		t.Fatalf("Create c2 (different account) err = %v, want nil", err)
	}
	// Both characters must coexist and be independently addressable.
	if got, err := repo.FindByName("luis", "hero"); err != nil || got.TemplateID != "warrior" {
		t.Errorf("FindByName(luis, hero) = %+v, %v; want warrior", got, err)
	}
	if got, err := repo.FindByName("ana", "hero"); err != nil || got.TemplateID != "mage" {
		t.Errorf("FindByName(ana, hero) = %+v, %v; want mage", got, err)
	}
}

// TestFileRepositoryListByAccount covers the list path (spec "List
// characters"): ListByAccount returns only the account's own characters, in
// store order, and nothing from other accounts.
func TestFileRepositoryListByAccount(t *testing.T) {
	repo := newRepo(t)
	chars := []*Character{
		{AccountID: "luis", Name: "hero", TemplateID: "warrior"},
		{AccountID: "luis", Name: "mago_1", TemplateID: "mage"},
		{AccountID: "ana", Name: "ranger_1", TemplateID: "ranger"},
	}
	for i := range chars {
		if err := repo.Create(chars[i]); err != nil {
			t.Fatalf("Create %q: %v", chars[i].Name, err)
		}
	}

	l, err := repo.ListByAccount("luis")
	if err != nil {
		t.Fatalf("ListByAccount(luis): %v", err)
	}
	if len(l) != 2 {
		t.Fatalf("ListByAccount(luis) len = %d, want 2", len(l))
	}
	if l[0].Name != "hero" || l[1].Name != "mago_1" {
		t.Errorf("ListByAccount(luis) order = [%s, %s], want [hero, mago_1]", l[0].Name, l[1].Name)
	}
	for _, c := range l {
		if c.AccountID != "luis" {
			t.Errorf("ListByAccount(luis) returned foreign account %q", c.AccountID)
		}
	}

	a, err := repo.ListByAccount("ana")
	if err != nil {
		t.Fatalf("ListByAccount(ana): %v", err)
	}
	if len(a) != 1 || a[0].Name != "ranger_1" {
		t.Errorf("ListByAccount(ana) = %+v, want just ranger_1", a)
	}
}

// TestFileRepositoryFindByName covers the uniqueness lookup (design D7): the
// (account, name) pair resolves to the character, a wrong account resolves
// to ErrNotFound, and an unknown name resolves to ErrNotFound.
func TestFileRepositoryFindByName(t *testing.T) {
	repo := newRepo(t)
	c := &Character{AccountID: "luis", Name: "hero", TemplateID: "warrior"}
	if err := repo.Create(c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.FindByName("luis", "hero")
	if err != nil {
		t.Fatalf("FindByName(luis, hero): %v", err)
	}
	if got.ID != c.ID || got.TemplateID != "warrior" {
		t.Errorf("FindByName = %+v, want id %q / warrior", got, c.ID)
	}

	if _, err := repo.FindByName("ana", "hero"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByName(ana, hero) err = %v, want ErrNotFound", err)
	}
	if _, err := repo.FindByName("luis", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByName(luis, nope) err = %v, want ErrNotFound", err)
	}
}

// TestFileRepositoryGetUnknown verifies an unknown id resolves to
// ErrNotFound (spec: personaje inexistente must be rejected).
func TestFileRepositoryGetUnknown(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(nope) err = %v, want ErrNotFound", err)
	}
}

// TestFileRepositoryAtomicWriteAndDurability covers the atomic write path
// (design D1): after a Create the store file is valid JSON containing the
// character, no temp file is left behind, and a brand-new repository on the
// same path reloads the same character with the same id (id stability +
// durability).
func TestFileRepositoryAtomicWriteAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "characters.json")
	repo, err := NewFileCharacterRepository(path)
	if err != nil {
		t.Fatalf("NewFileCharacterRepository: %v", err)
	}
	c := &Character{AccountID: "luis", Name: "hero", TemplateID: "warrior"}
	if err := repo.Create(c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var entries []Character
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("store is not valid JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != c.ID || entries[0].Name != "hero" {
		t.Errorf("persisted store = %+v, want a single %q/hero", entries, c.ID)
	}

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp store file %s.tmp left behind (stat err = %v)", path, err)
	}

	// Reload through a fresh repository: the character (and its id) survives.
	repo2, err := NewFileCharacterRepository(path)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	got, err := repo2.Get(c.ID)
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.Name != "hero" || got.ID != c.ID || got.TemplateID != "warrior" {
		t.Errorf("reloaded = %+v, want id %q / hero / warrior", got, c.ID)
	}
}

// TestNewFileCharacterRepositoryRejectsCorruptJSON covers the fail-fast
// corrupt class on the write path's load: syntactically invalid JSON and a
// well-formed JSON value that is not an array of characters must both fail
// with ErrInvalidJSON.
func TestNewFileCharacterRepositoryRejectsCorruptJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"garbage text", `not json at all`},
		{"truncated object", `[{"id":"a"`},
		{"object instead of array", `{"a":{"id":"a"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFileCharacterRepository(writeStore(t, tc.content))
			if !errors.Is(err, ErrInvalidJSON) {
				t.Errorf("err = %v, want ErrInvalidJSON", err)
			}
		})
	}
}

// TestCommittedStoreLoads loads the real committed runtime store at the repo
// root (data/characters.json) through the same loader path the server uses,
// proving the shipped store file is valid and starts empty (task 3.2).
func TestCommittedStoreLoads(t *testing.T) {
	repo, err := NewFileCharacterRepository(filepath.Join(repoRoot(t), "data", "characters.json"))
	if err != nil {
		t.Fatalf("committed store: %v", err)
	}
	chars, err := repo.ListByAccount("nobody")
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(chars) != 0 {
		t.Fatalf("committed store should start empty, got %d characters", len(chars))
	}
}
