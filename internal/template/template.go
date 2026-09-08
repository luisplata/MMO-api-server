// Package template defines the character-template catalog domain
// (spec character-templates, design D1/D6).
//
// A template is the "class" a character is created from: identity
// (id/name), the base attributes the character snapshots at creation,
// and a visual hint so Unity can pick a model. Templates are file-based
// today (data/templates.json) behind a repository interface, so a future
// DB swap only requires a new implementation — handlers never read the
// file directly.
package template

import "github.com/luisplata/mmo-api-server/internal/stats"

// Stats is a type alias so template consumers can write template.Stats
// interchangeably with stats.Stats (design D6). It is the same type, not
// a new one.
type Stats = stats.Stats

// Template is one entry of the character catalog.
//
// JSON field names (id/name/baseStats/visualHint) match the seed catalog
// in data/templates.json. baseStats is the source of a character's stats
// snapshot — the server never hardcodes these values.
type Template struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	BaseStats  stats.Stats `json:"baseStats"`
	VisualHint string      `json:"visualHint"`
}

// TemplateRepository abstracts the template catalog so handlers depend on
// an interface and a future DB swap only needs a new implementation
// (design D1). Get returns ErrNotFound for an unknown id; All returns
// every catalog entry in file order.
type TemplateRepository interface {
	Get(id string) (*Template, error)
	All() ([]*Template, error)
}
