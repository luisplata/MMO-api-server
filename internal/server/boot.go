package server

// Deployed boot wiring (P17): the binary loads the committed character
// catalogs from disk before opening the WSS listener. Both repositories are
// file-backed and validated at construction, so LoadRepositories is the
// single fail-fast entry point that keeps the server from booting with a
// missing or corrupt character service.

import (
	"fmt"
	"os"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/template"
)

// Repositories bundles the character-management stores the session layer
// needs for the selecting phase (design D1).
type Repositories struct {
	Templates  template.TemplateRepository
	Characters character.CharacterRepository
}

// LoadRepositories opens the template catalog and character store at the
// given paths. Missing files and malformed JSON abort the boot with a
// wrapped error, so the deployed server never exposes a partial or empty
// character service by accident.
func LoadRepositories(templatesPath, charactersPath string) (Repositories, error) {
	if _, err := os.Stat(templatesPath); err != nil {
		return Repositories{}, fmt.Errorf("server: templates %s: %w", templatesPath, err)
	}
	templates, err := template.NewFileTemplateRepository(templatesPath)
	if err != nil {
		return Repositories{}, fmt.Errorf("server: templates: %w", err)
	}

	if _, err := os.Stat(charactersPath); err != nil {
		return Repositories{}, fmt.Errorf("server: characters %s: %w", charactersPath, err)
	}
	characters, err := character.NewFileCharacterRepository(charactersPath)
	if err != nil {
		return Repositories{}, fmt.Errorf("server: characters: %w", err)
	}

	return Repositories{Templates: templates, Characters: characters}, nil
}
