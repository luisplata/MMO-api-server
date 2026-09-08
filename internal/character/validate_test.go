package character

// Name-validation tests (design D7, spec "Create character" / "Nombre
// inválido").
//
// Rule under test: ValidateName trims the input, then requires the trimmed
// form to be 3–16 characters drawn from [a-zA-Z0-9_] (letters either case,
// digits, underscore). It returns the trimmed name on success so callers
// persist the normalized value.
//
// Coverage: valid names (min/max length, uppercase, underscore, digits,
// surrounding whitespace trimmed) and invalid names (empty, only spaces,
// too short, too long, forbidden characters including internal space and
// punctuation).

import (
	"errors"
	"testing"
)

// TestValidateNameValid covers the happy path and the normalization: a valid
// name string (after trimming) returns the trimmed name with no error.
// Triangulation: distinct lengths, cases, and charsets.
func TestValidateNameValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase", "guerrero", "guerrero"},
		{"uppercase_and_underscore", "Mago_1", "Mago_1"},
		{"digits_and_underscore", "a_b_c_123", "a_b_c_123"},
		{"min_length", "abc", "abc"},
		{"max_length", "abcdefghijklmnop", "abcdefghijklmnop"},
		{"surrounding_whitespace_trimmed", "  sorcerer  ", "sorcerer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateName(tc.in)
			if err != nil {
				t.Fatalf("ValidateName(%q) err = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ValidateName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateNameInvalid covers every rejection: empty, whitespace-only,
// out-of-length-range, forbidden characters (internal space, punctuation),
// and non-ASCII letters. Each must fail with ErrInvalidName.
func TestValidateNameInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace_only", "   "},
		{"too_short", "ab"},
		{"too_long", "abcdefghijklmnopq"},
		{"forbidden_bang", "Mago!"},
		{"forbidden_hyphen", "Mago-Name"},
		{"forbidden_dot", "Mago.Name"},
		{"internal_space", "Mago Name"},
		{"non_ascii_accent", "Magó"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateName(tc.in); !errors.Is(err, ErrInvalidName) {
				t.Errorf("ValidateName(%q) err = %v, want ErrInvalidName", tc.in, err)
			}
		})
	}
}
