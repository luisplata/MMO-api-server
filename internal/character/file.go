package character

// File-backed character store (design D1, spec character-management).
//
// Unlike the read-only template catalog, characters are mutable, so the
// repository keeps an in-memory slice loaded at construction and persists it
// atomically on every Create: it writes to a temp file in the same directory
// and renames it over the target. The tmp+rename keeps a single-writer store
// crash-safe (a torn write never corrupts an existing store), per design D1.
// A missing store file is treated as an empty store (not an error).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Store error sentinels — each corruption class is distinguishable so
// callers can report the reason and the server can refuse to boot.
var (
	// ErrInvalidJSON wraps a store file that is not valid JSON or not an
	// array of characters.
	ErrInvalidJSON = errors.New("character: invalid JSON store")
	// ErrInvalidEntity wraps a character entry missing a required identity
	// field (id/accountId/name/templateId).
	ErrInvalidEntity = errors.New("character: malformed character entry")
	// ErrDuplicateID is returned by Create for a character id already stored.
	ErrDuplicateID = errors.New("character: duplicate character id")
	// ErrDuplicateName is returned by Create for a name already used by
	// another character in the same account (design D7).
	ErrDuplicateName = errors.New("character: name already used in account")
	// ErrNotFound is returned by Get/FindByName for an unknown id or
	// (account, name) pair.
	ErrNotFound = errors.New("character: not found")
)

// fileCharacterRepository is the file-backed CharacterRepository. It is the
// single writer for the store: the in-memory slice is guarded by mu, and
// every Create persists the whole store atomically to the backing file.
type fileCharacterRepository struct {
	path  string
	mu    sync.Mutex
	chars []*Character
}

// Compile-time assertion: fileCharacterRepository satisfies the
// CharacterRepository contract (design D1).
var _ CharacterRepository = (*fileCharacterRepository)(nil)

// NewFileCharacterRepository loads the store at path. A missing file is a
// valid empty store; a corrupt file fails fast with ErrInvalidJSON and no
// partial state. It returns the repository ready to Create/List/Get/Find.
func NewFileCharacterRepository(path string) (*fileCharacterRepository, error) {
	r := &fileCharacterRepository{path: path}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// load populates the in-memory slice from the backing file. A missing file
// or an empty file is treated as an empty store; a corrupt file is rejected.
func (r *fileCharacterRepository) load() error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			r.chars = []*Character{}
			return nil
		}
		return fmt.Errorf("character: read store %s: %w", r.path, err)
	}
	if len(data) == 0 {
		r.chars = []*Character{}
		return nil
	}
	var entries []*Character
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	r.chars = entries
	return nil
}

// Create persists c. If c.ID is empty it assigns a unique generated id; if
// c.CreatedAt is zero it sets it to now. It enforces id uniqueness and
// per-account name uniqueness (design D7), then writes the store atomically.
// On a write failure it rolls back the in-memory append so the repository is
// never out of sync with the file.
func (r *fileCharacterRepository) Create(c *Character) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if c.ID == "" {
		c.ID = newID()
	}
	if c.Name == "" || c.AccountID == "" || c.TemplateID == "" {
		return fmt.Errorf("%w: empty id/accountId/name/templateId", ErrInvalidEntity)
	}
	for _, e := range r.chars {
		if e.ID == c.ID {
			return fmt.Errorf("%w: %q", ErrDuplicateID, c.ID)
		}
	}
	for _, e := range r.chars {
		if e.AccountID == c.AccountID && e.Name == c.Name {
			return fmt.Errorf("%w: %q", ErrDuplicateName, c.Name)
		}
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}

	r.chars = append(r.chars, clone(c))
	if err := r.persist(); err != nil {
		r.chars = r.chars[:len(r.chars)-1] // rollback the in-memory append
		return err
	}
	return nil
}

// ListByAccount returns copies of every character of accountID in store
// order. An account with no characters yields an empty non-nil slice
// (spec "Cuenta vacía": no error).
func (r *fileCharacterRepository) ListByAccount(accountID string) ([]*Character, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*Character{}
	for _, c := range r.chars {
		if c.AccountID == accountID {
			out = append(out, clone(c))
		}
	}
	return out, nil
}

// Get returns a copy of the character for id, or ErrNotFound.
func (r *fileCharacterRepository) Get(id string) (*Character, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.chars {
		if c.ID == id {
			return clone(c), nil
		}
	}
	return nil, ErrNotFound
}

// FindByName returns a copy of the character for the (accountID, name) pair,
// or ErrNotFound. It is the per-account name-uniqueness lookup (design D7).
func (r *fileCharacterRepository) FindByName(accountID, name string) (*Character, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.chars {
		if c.AccountID == accountID && c.Name == name {
			return clone(c), nil
		}
	}
	return nil, ErrNotFound
}

// persist writes the whole store atomically: it marshals the slice, writes a
// temp file in the same directory, then renames it over the target. Because
// the temp file shares the directory as the target, the rename is atomic on
// the same filesystem — a crash between write and rename leaves the previous
// store intact.
func (r *fileCharacterRepository) persist() error {
	data, err := json.MarshalIndent(r.chars, "", "  ")
	if err != nil {
		return fmt.Errorf("character: marshal store: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("character: write temp store: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("character: rename store: %w", err)
	}
	return nil
}

// clone returns a shallow copy of c so the store's canonical entries are
// never mutated by callers.
func clone(c *Character) *Character {
	cp := *c
	return &cp
}

// newID returns a random 16-char hex id (8 random bytes) so a character gets
// a stable, unique entity identity (design D3). crypto/rand supplies the
// bytes; if it ever fails (it should not), it falls back to a timestamp.
func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
