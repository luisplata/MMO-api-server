package template

// File-backed template catalog (design D1, spec character-templates).
//
// The repository loads data/templates.json once at construction and fails
// fast on any invalid catalog — corrupt JSON, a duplicate template id, or
// a malformed (empty-id/name) entry — so the server never exposes a
// partial or ambiguous catalog. It is read-only: templates are a static
// catalog, so the only mutation is the future DB swap that provides a new
// TemplateRepository implementation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Catalog error sentinels — each corruption class is distinguishable so
// callers can report the reason and the server can refuse to boot.
var (
	// ErrInvalidJSON wraps a catalog file that is not valid JSON or not
	// an array of templates.
	ErrInvalidJSON = errors.New("template: invalid JSON catalog")
	// ErrInvalidTemplate wraps a template entry missing its id or name.
	ErrInvalidTemplate = errors.New("template: malformed template entry")
	// ErrDuplicateID wraps a catalog with two entries sharing an id.
	ErrDuplicateID = errors.New("template: duplicate template id")
	// ErrNotFound is returned by Get for an id not present in the catalog.
	ErrNotFound = errors.New("template: not found")
)

// fileTemplateRepository is the file-backed TemplateRepository. The
// catalog is fully parsed at construction so Get/All are in-memory and
// never re-read the file.
type fileTemplateRepository struct {
	byID  map[string]*Template
	order []*Template
}

// Compile-time assertion: fileTemplateRepository satisfies the
// TemplateRepository contract (design D1).
var _ TemplateRepository = (*fileTemplateRepository)(nil)

// NewFileTemplateRepository reads and validates the template catalog at
// path, returning an error without any partial state if the catalog is
// corrupt, contains duplicate ids, or has a malformed entry.
func NewFileTemplateRepository(path string) (*fileTemplateRepository, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("template: read catalog %s: %w", path, err)
	}

	var entries []Template
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	byID := make(map[string]*Template, len(entries))
	order := make([]*Template, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		if e.ID == "" || e.Name == "" {
			return nil, fmt.Errorf("%w: entry %d has empty id/name", ErrInvalidTemplate, i)
		}
		if _, dup := byID[e.ID]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateID, e.ID)
		}
		byID[e.ID] = e
		order = append(order, e)
	}
	return &fileTemplateRepository{byID: byID, order: order}, nil
}

// Get returns a copy of the template registered for id, or ErrNotFound if
// the id is not in the catalog. It returns a copy so callers cannot
// mutate the catalog's canonical entries.
func (r *fileTemplateRepository) Get(id string) (*Template, error) {
	e, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(e), nil
}

// All returns copies of every catalog entry in file order.
func (r *fileTemplateRepository) All() ([]*Template, error) {
	out := make([]*Template, 0, len(r.order))
	for _, e := range r.order {
		out = append(out, clone(e))
	}
	return out, nil
}

// clone returns a shallow copy of t so the catalog's canonical entries are
// never mutated by callers.
func clone(t *Template) *Template {
	cp := *t
	return &cp
}
