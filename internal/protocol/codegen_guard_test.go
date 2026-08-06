package protocol

// Guards for spec R1 S1.1 (generated code committed), S1.2 (no
// reflection-heavy client paths) and S1.3 (no legacy wire formats).
// These tests scan the committed generated bindings and the server's
// non-generated Go source so forbidden serialization paths can never
// creep in unnoticed.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedGoCompilesAndIsLegacyFree covers S1.1 + S1.3 for the Go
// side: the committed .pb.go files must exist and must not import gob or
// hand-rolled serialization helpers (dynamicpb/jsonpb/gob).
func TestGeneratedGoCompilesAndIsLegacyFree(t *testing.T) {
	root := repoRoot(t)
	files := []string{
		"proto/v1/gen/go/v1/world.pb.go",
		"internal/protocol/testfixture/mini.pb.go",
	}
	for _, f := range files {
		path := filepath.Join(root, filepath.FromSlash(f))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("committed generated Go missing: %s (%v)", f, err)
			continue
		}
		src := string(data)
		for _, banned := range []string{"encoding/gob", "dynamicpb", "jsonpb"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s references %s (legacy/reflection-heavy serialization)", f, banned)
			}
		}
	}
}

// TestGeneratedCSharpExistsAndIsReflectionFree covers S1.1 + S1.2: the
// committed C# bindings must exist for every contract message and must
// not reference the reflection-heavy IL2CPP paths (DynamicMessage,
// JsonParser, google.protobuf.Any). protoc-gen-csharp emits one .cs per
// proto file, so World.cs must declare all 12 contract messages.
func TestGeneratedCSharpExistsAndIsReflectionFree(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "proto", "v1", "gen", "csharp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("generated C# directory missing: %v", err)
	}

	// Every message in proto/v1/world.proto must have a committed .cs.
	required := []string{
		"Vec2", "EntityState", "Hello", "ServerInfo", "VersionMismatch",
		"AuthRequest", "AuthResponse", "EnterWorld", "WorldSnapshot",
		"MoveInput", "Snapshot", "Ack",
	}
	present := map[string]bool{}
	for _, e := range entries {
		present[e.Name()] = true
	}
	if !present["World.cs"] {
		t.Fatalf("missing committed generated C# file: World.cs (found: %v)", entries)
	}
	data, err := os.ReadFile(filepath.Join(dir, "World.cs"))
	if err != nil {
		t.Fatalf("read World.cs: %v", err)
	}
	src := string(data)
	for _, m := range required {
		want := "class " + m + " "
		if !strings.Contains(src, want) {
			t.Errorf("World.cs must declare %s, missing %q", m, want)
		}
	}

	// Reflection-heavy API guard (S1.2): the generated C# must never
	// reference DynamicMessage, JsonParser, or google.protobuf.Any.
	banned := []string{"DynamicMessage", "JsonParser", "WellKnownTypes"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cs") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Errorf("read %s: %v", e.Name(), err)
			continue
		}
		fileSrc := string(content)
		for _, b := range banned {
			if strings.Contains(fileSrc, b) {
				t.Errorf("%s references %s (reflection-heavy IL2CPP path)", e.Name(), b)
			}
		}
	}
}

// TestNoGobInServerMessagePaths covers S1.3 for the server side: no
// non-generated, non-test Go source under internal/ or cmd/ may use gob
// (or any other legacy wire format) for message serialization.
func TestNoGobInServerMessagePaths(t *testing.T) {
	root := repoRoot(t)
	var sources []string
	for _, d := range []string{"internal", "cmd"} {
		base := filepath.Join(root, d)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".pb.go") {
				return nil // tests and generated code are covered elsewhere
			}
			sources = append(sources, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}
	for _, f := range sources {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		if strings.Contains(string(data), "encoding/gob") {
			t.Errorf("%s imports encoding/gob (legacy wire format in message paths)", f)
		}
	}
}
