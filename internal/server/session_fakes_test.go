package server

// In-memory stand-ins for the character-management repository interfaces
// the session layer needs in the selecting phase (design D1/D4). The
// server wiring tests build a session directly and drive it through
// Hello→Auth→SelectCharacter→EnterWorld; these fakes let that flow run
// without a real store.

import (
	"strings"
	"time"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/stats"
	"github.com/luisplata/mmo-api-server/internal/template"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// testCharID is the character seeded for the test account so the session
// can select it.
const testCharID = "char-1"

type fakeTemplateRepo struct {
	templates map[string]*template.Template
}

func (f *fakeTemplateRepo) Get(id string) (*template.Template, error) {
	t, ok := f.templates[id]
	if !ok {
		return nil, template.ErrNotFound
	}
	return t, nil
}

func (f *fakeTemplateRepo) All() ([]*template.Template, error) {
	out := []*template.Template{}
	for _, t := range f.templates {
		out = append(out, t)
	}
	return out, nil
}

type fakeCharacterRepo struct {
	chars []*character.Character
}

func (f *fakeCharacterRepo) Create(c *character.Character) error {
	if c.ID == "" {
		c.ID = "gen-" + c.AccountID + "-" + c.Name
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	f.chars = append(f.chars, c)
	return nil
}

func (f *fakeCharacterRepo) ListByAccount(accountID string) ([]*character.Character, error) {
	out := []*character.Character{}
	for _, c := range f.chars {
		if c.AccountID == accountID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeCharacterRepo) Get(id string) (*character.Character, error) {
	for _, c := range f.chars {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, character.ErrNotFound
}

func (f *fakeCharacterRepo) FindByName(accountID, name string) (*character.Character, error) {
	for _, c := range f.chars {
		if c.AccountID == accountID && strings.EqualFold(c.Name, name) {
			return c, nil
		}
	}
	return nil, character.ErrNotFound
}

// fakeSpawnResolver returns a fixed spawn for any selected character.
type fakeSpawnResolver struct{ spawn mmov1.Vec3 }

func (f *fakeSpawnResolver) SpawnFor(characterID string) *mmov1.Vec3 {
	return &mmov1.Vec3{X: f.spawn.X, Y: f.spawn.Y, Z: f.spawn.Z}
}

// testTemplateRepo returns a catalog with the committed warrior/mage
// templates so CreateCharacter has a valid template to snapshot.
func testTemplateRepo() *fakeTemplateRepo {
	return &fakeTemplateRepo{templates: map[string]*template.Template{
		"warrior": {ID: "warrior", Name: "Guerrero", BaseStats: stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}},
		"mage":    {ID: "mage", Name: "Mago", BaseStats: stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4}},
	}}
}

// testCharacterRepo returns a store seeded with one character owned by
// accountID, so the session can select it.
func testCharacterRepo(accountID string) *fakeCharacterRepo {
	return &fakeCharacterRepo{chars: []*character.Character{
		{ID: testCharID, AccountID: accountID, Name: "Hero", TemplateID: "warrior",
			Stats: stats.Stats{HP: 120, Speed: 3, Atk: 10, Def: 8}, CreatedAt: time.Now().UTC()},
	}}
}
