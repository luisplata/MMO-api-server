package protocol

// Guards for spec R1 S1.1 (generated code committed), S1.2 (no
// reflection-heavy client paths) and S1.3 (no legacy wire formats).
// These tests scan the committed generated bindings and the server's
// non-generated Go source so forbidden serialization paths can never
// creep in unnoticed.

import (
	"errors"
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

// TestGeneratedTypeScriptContract pins the committed ES bindings for the
// public web-client surface: the protoc-gen-es v2 preamble and runtime import
// are fixed, every contract message (foundation/session, simulation/world-state
// and character management) is exported as both a type and a schema, and the
// deferred private-room symbols stay absent. The scan needs no Node toolchain.
func TestGeneratedTypeScriptContract(t *testing.T) {
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
		"EntityState", "WorldSnapshot", "MoveInput", "Snapshot", "Ack",
		"SpawnEntity", "DespawnEntity",
		"Stats", "Character", "ListCharacters", "CharacterList",
		"CreateCharacter", "CreateCharacterResponse",
		"SelectCharacter", "SelectCharacterResponse",
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

// TestGeneratedTypeScriptContractIsComplete covers the final P19d contract:
// every world.proto message is exported, the generator inputs stay pinned and
// unfiltered, and the unallocated private-room id 23 stays absent.
func TestGeneratedTypeScriptContractIsComplete(t *testing.T) {
	root := repoRoot(t)
	protoSrc := readRepoFile(t, root, "proto/v1/world.proto")
	tsBody := stripTSComments(readRepoFile(t, root, "proto/v1/gen/es/v1/world_pb.ts"))
	for _, name := range protoMessageNames(protoSrc) {
		if !strings.Contains(tsBody, "export type "+name+" =") || !strings.Contains(tsBody, "export const "+name+"Schema") {
			t.Errorf("world.proto message %s is not exported", name)
		}
	}
	if got, want := strings.Count(tsBody, "export type "), len(protoMessageNames(protoSrc)); got != want {
		t.Errorf("world_pb.ts exports %d types, want the %d-message contract", got, want)
	}
	assertTokens(t, root, "package.json", `"@bufbuild/protobuf": "2.16.0"`, `"@bufbuild/protoc-gen-es": "2.16.0"`)
	assertTokens(t, root, "buf.gen.es.yaml", "version: v2", "local: protoc-gen-es", "out: proto/v1/gen/es", "opt: target=ts")
	assertTokens(t, root, "Makefile", "proto-es:", "buf.gen.es.yaml", "git diff --exit-code")
	if regexp.MustCompile(`(?m)^\s+types:`).MatchString(readRepoFile(t, root, "buf.gen.es.yaml")) {
		t.Errorf("buf.gen.es.yaml must not filter types")
	}

	for _, row := range protoTypeIDTable(t) {
		if row.id > 22 {
			t.Errorf("world.proto allocates id %d above 22 (%s)", row.id, row.name)
		}
	}
	if _, err := NewWorldRegistry().NewMessage(23); !errors.Is(err, ErrUnknownType) {
		t.Errorf("id 23 must stay unregistered, got %v", err)
	}
}

var protoMessagePattern = regexp.MustCompile(`(?m)^message (\w+) \{`)

func protoMessageNames(src string) []string {
	var names []string
	for _, m := range protoMessagePattern.FindAllStringSubmatch(src, -1) {
		names = append(names, m[1])
	}
	return names
}

func assertTokens(t *testing.T, root, file string, want ...string) {
	t.Helper()
	src := readRepoFile(t, root, file)
	for _, token := range want {
		if !strings.Contains(src, token) {
			t.Errorf("%s missing generation token %q", file, token)
		}
	}
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
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
