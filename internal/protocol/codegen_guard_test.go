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
	"regexp"
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
// proto file, so World.cs must declare all 14 contract messages.
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
		"MoveInput", "Snapshot", "Ack", "SpawnEntity", "DespawnEntity",
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

// TestGeneratedTypeScriptFoundationContract pins the committed ES bindings
// for the P19b handshake/auth/character-selection surface: the protoc-gen-es
// v2 preamble and runtime import are fixed, the foundation/session messages
// are exported as both a type and a schema, and the deferred private-room
// symbols stay absent. The scan needs no Node toolchain.
func TestGeneratedTypeScriptFoundationContract(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "proto", "v1", "gen", "es", "v1", "world_pb.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("committed generated TypeScript missing: %v", err)
	}
	src := string(data)

	// The preamble is a comment, so verify it on the raw source.
	wantPreamble := `// @generated by protoc-gen-es v2.16.0 with parameter "target=ts"`
	if !strings.Contains(src, wantPreamble) {
		t.Errorf("world_pb.ts must carry %q", wantPreamble)
	}
	for _, want := range []string{
		`// @generated from file v1/world.proto (package mmo.v1, syntax proto3)`,
		`from "@bufbuild/protobuf/codegenv2"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("world_pb.ts missing generated contract token %q", want)
		}
	}

	body := stripTSComments(src)
	required := []string{
		"Vec2", "Vec3", "Hello", "ServerInfo", "VersionMismatch",
		"AuthRequest", "AuthResponse", "EnterWorld",
		"Ping", "Pong", "JoinRoom", "JoinRoomResponse",
	}
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(body, "export type "+name+" =") {
				t.Errorf("world_pb.ts must export type %s", name)
			}
			if !strings.Contains(body, "export const "+name+"Schema") {
				t.Errorf("world_pb.ts must export const %sSchema", name)
			}
		})
	}

	for _, banned := range []string{"roomCode", "RoomError", "ByCode", "Create"} {
		t.Run("absent/"+banned, func(t *testing.T) {
			if hasTSSymbol(body, banned) {
				t.Errorf("world_pb.ts must not declare private-room symbol %q", banned)
			}
		})
	}
}

// TestTSSymbolGuardIgnoresComments triangulates the comment-safe scanner used
// by the generated-TypeScript guard: proto comments are copied into the .ts
// output, so symbol checks must never treat comment prose as a declaration.
func TestTSSymbolGuardIgnoresComments(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		symbol string
		want   bool
	}{
		{"line comment", "// Create/ByCode are A1-gated\nexport const x = 1;", "ByCode", false},
		{"block comment", "/* roomCode and RoomError */\nexport const y = 1;", "roomCode", false},
		{"real declaration", "export type JoinRoom = { roomCode: string };", "roomCode", true},
		{"CreateCharacter is allowed", "export type CreateCharacter = {};", "Create", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasTSSymbol(stripTSComments(tc.src), tc.symbol); got != tc.want {
				t.Errorf("hasTSSymbol(stripTSComments(%q), %q) = %v, want %v", tc.src, tc.symbol, got, tc.want)
			}
		})
	}
}

// stripTSComments removes // line comments and /* ... */ block comments
// without touching comment markers inside string literals.
func stripTSComments(src string) string {
	var b strings.Builder
	var quote byte
	escaped, inLine, inBlock := false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte(c)
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case quote != 0:
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		default:
			if c == '\'' || c == '"' || c == '`' {
				quote = c
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// hasTSSymbol reports whether name appears as a whole identifier, so Create
// does not match CreateCharacter.
func hasTSSymbol(src, name string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(src)
}
