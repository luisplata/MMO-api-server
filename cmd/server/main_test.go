package main

// Boot map selection tests (design D7, spec WTM-4, task 2.6): the -map
// flag selects a canonical <name>.heightmap loaded through the shared
// loader (checksum + redundant metadata verified, fail-fast wrapped %w),
// and an empty flag selects the embedded hills fixture. Both paths hand
// the server a world.HeightResolver — nil/flat is never the boot result.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// fixturePath resolves the committed reference fixture pair from the
// test file's own location (robust to whatever cwd the test binary runs
// in — go test does not guarantee the package directory).
func fixturePath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "internal", "world", "testdata", "hills.heightmap")
}

// TestSplitOrigins pins the -wss-origins parsing: the flag is a
// comma-separated explicit host allowlist, trimmed, with empty entries
// dropped. Wildcards are preserved here so server.New can fail boot.
func TestSplitOrigins(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"allowed.example", []string{"allowed.example"}},
		{" allowed.example , https://game.example:443 ", []string{"allowed.example", "https://game.example:443"}},
		{"*,", []string{"*"}},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := splitOrigins(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitOrigins(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestShortSHA pins the boot-log abbreviation of the heightmap sha256:
// the first 8 hex characters, never a panic on short input.
func TestShortSHA(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"5fa2e4ca78d53407f39097666f66cf386038d69de2e777e471042741ac73d511", "5fa2e4ca"},
		{"abcdef0123456789", "abcdef01"},
		{"abc", "abc"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := shortSHA(tc.in); got != tc.want {
			t.Errorf("shortSHA(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestLoadMapDefaultSelectsEmbeddedFixture pins WTM-4 default: with no
// -map flag the boot resolves the embedded hills fixture, whose spawn
// point (100, 200) sits on the hill peak (Y = 25).
func TestLoadMapDefaultSelectsEmbeddedFixture(t *testing.T) {
	h, err := loadMap("")
	if err != nil {
		t.Fatalf("loadMap(\"\"): %v", err)
	}
	if h == nil {
		t.Fatal("loadMap(\"\") returned a nil resolver — flat boot is not a valid default")
	}
	if got := h.HeightAt(100, 200); got != 25 {
		t.Errorf("HeightAt(100, 200) = %v, want 25 (embedded hills fixture)", got)
	}
}

// TestLoadMapFlagLoadsFixtureFile pins WTM-4 load: the -map path loads
// the canonical .heightmap + .manifest pair from disk through the shared
// loader, producing the same grid as the embedded default.
func TestLoadMapFlagLoadsFixtureFile(t *testing.T) {
	h, err := loadMap(fixturePath(t))
	if err != nil {
		t.Fatalf("loadMap(%q): %v", fixturePath(t), err)
	}
	if h == nil {
		t.Fatal("loadMap returned a nil resolver for a valid file")
	}
	if got := h.HeightAt(100, 200); got != 25 {
		t.Errorf("HeightAt(100, 200) = %v, want 25 (file-loaded fixture)", got)
	}
	if got := h.HeightAt(0, 0); got != 0 {
		t.Errorf("HeightAt(0, 0) = %v, want 0 (flat base)", got)
	}
}

// TestLoadMapFailsFastOnCorruptFile pins WTM-5 boot behavior: a
// tampered or unverifiable map must fail wrapped (%w), never returning
// a partial resolver the server could boot with.
func TestLoadMapFailsFastOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "broken.heightmap")
	if err := os.WriteFile(corrupt, []byte("not a heightmap"), 0o644); err != nil {
		t.Fatalf("write corrupt fixture: %v", err)
	}
	if _, err := loadMap(corrupt); err == nil {
		t.Fatal("loadMap(corrupt) must fail (bad magic), got nil error")
	}
	if _, err := loadMap(filepath.Join(dir, "ghost.heightmap")); err == nil {
		t.Fatal("loadMap(missing) must fail (unreadable file), got nil error")
	}
}
