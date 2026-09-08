// Package character defines the character-management domain
// (spec character-management, design D1/D2/D3/D7).
//
// A Character belongs to an account (accountId == the auth username),
// references a template, and carries a FROZEN snapshot of the template's
// base stats captured at creation (design D2). It is the world-entity
// identity (D3: entity id = characterId, not accountId). Characters are
// file-based today (data/characters.json) behind a repository interface, so
// a future DB swap only requires a new implementation — handlers never read
// the store directly.
package character

import (
	"time"

	"github.com/luisplata/mmo-api-server/internal/stats"
)

// Character is one created character owned by an account.
//
// JSON field names match the runtime store data/characters.json and the
// proto Character message: id/accountId/name/templateId/stats/createdAt.
//
// Stats is a snapshot of the template's baseStats at creation time — the
// server never re-derives it (design D2), so a template rebalance does not
// silently buff or nerf an existing character.
type Character struct {
	ID         string      `json:"id"`
	AccountID  string      `json:"accountId"`
	Name       string      `json:"name"`
	TemplateID string      `json:"templateId"`
	Stats      stats.Stats `json:"stats"`
	CreatedAt  time.Time   `json:"createdAt"`
}

// CharacterRepository abstracts the character store so handlers depend on an
// interface and a future DB swap only needs a new implementation (design D1).
//
// Create persists a character and enforces id uniqueness plus per-account
// name uniqueness; the concrete file impl writes atomically (tmp+rename),
// as required by design D1's single-writer write path. Get/FindByName return
// ErrNotFound for an unknown id or (account, name) pair; ListByAccount
// returns every character of an account in store order.
type CharacterRepository interface {
	Create(c *Character) error
	ListByAccount(accountID string) ([]*Character, error)
	Get(id string) (*Character, error)
	FindByName(accountID, name string) (*Character, error)
}
