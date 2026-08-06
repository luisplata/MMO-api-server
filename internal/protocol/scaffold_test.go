package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory (the package dir)
// until it finds go.mod — the repository root.
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

// TestRepositoryContract pins spec R8 S8.1 (Repository contract):
// the module is renamed, root scratch is gone, the required layout
// exists, and the tooling metadata is kept.
func TestRepositoryContract(t *testing.T) {
	root := repoRoot(t)

	// 1. go.mod must declare the new module path.
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.HasPrefix(string(gomod), "module github.com/luisplata/mmo-api-server\n") {
		t.Errorf("go.mod must declare module github.com/luisplata/mmo-api-server, got:\n%s", gomod)
	}

	// 2. Scratch from the old hello-world repo must be gone.
	if _, err := os.Stat(filepath.Join(root, "main.go")); !os.IsNotExist(err) {
		t.Errorf("root main.go (scratch hello-world) must be deleted")
	}
	if _, err := os.Stat(filepath.Join(root, "water-cli")); !os.IsNotExist(err) {
		t.Errorf("water-cli/ (unrelated scratch) must be deleted")
	}

	// 3. Required directory layout.
	required := []string{
		"cmd/server",
		"proto/v1",
		"internal/protocol",
		"internal/network",
		"internal/session",
		"internal/game",
		"internal/world",
		"internal/e2e",
		"docs",
	}
	for _, d := range required {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			t.Errorf("required directory %s missing: %v", d, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s exists but is not a directory", d)
		}
	}

	// 4. Tooling metadata must be kept.
	for _, p := range []string{".atl", ".agents", "skills-lock.json"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("tooling artifact %s must be kept: %v", p, err)
		}
	}
}
