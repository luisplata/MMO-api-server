package character

// Character-name validation (design D7, spec "Create character").
//
// The rule is deterministic and self-contained so handlers can validate a
// client-supplied name before persisting. Uniqueness is NOT validated here
// — that is a store concern resolved via CharacterRepository.FindByName
// (per-account). This file only checks the lexical shape: trimmed length
// and allowed charset.

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidName is returned by ValidateName for an empty, out-of-range, or
// off-charset name.
var ErrInvalidName = errors.New("character: invalid name")

// namePattern is the allowed charset for a character name (design D7):
// letters (either case), digits, and underscore, 3–16 chars after trim.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)

// ValidateName trims name and validates it against the character-name rule
// (design D7): the trimmed form must be 3–16 chars containing only
// [a-zA-Z0-9_]. It returns the trimmed name on success so callers persist
// the normalized value; it returns ErrInvalidName on any violation, without
// side effects so a failed create never mutates state (spec: "no muta
// estado").
func ValidateName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if !namePattern.MatchString(trimmed) {
		return "", ErrInvalidName
	}
	return trimmed, nil
}
