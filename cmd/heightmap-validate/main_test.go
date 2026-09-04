package main

// Validator command tests (design D8, spec WTM-8, task 3.1/3.2).
//
// The tests drive run() directly (no subprocess): the committed
// reference fixture pair (internal/world/testdata/hills.*) must validate
// with exit 0 and a readable summary; every corruption class — bad
// magic, bad reserved byte, bad version, bad dims, non-finite sample,
// out-of-range sample, truncated body, manifest sha256 mismatch and
// redundant-metadata mismatch — must exit 1 with the actionable reason
// on stderr; argument mistakes must exit 2 with usage on stderr. All
// corruptions are built by mutating copies of the committed fixture, so
// the exact artifact the server boots on is what the validator gates.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixtureDir resolves the committed reference fixture directory
// (internal/world/testdata) from this test file's own location — go
// test does not guarantee the package directory as cwd.
func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "internal", "world", "testdata")
}

// readFixture loads the committed hills pair bytes.
func readFixture(t *testing.T) (data, manifest []byte) {
	t.Helper()
	dir := fixtureDir(t)
	var err error
	data, err = os.ReadFile(filepath.Join(dir, "hills.heightmap"))
	if err != nil {
		t.Fatalf("read hills.heightmap: %v", err)
	}
	manifest, err = os.ReadFile(filepath.Join(dir, "hills.manifest"))
	if err != nil {
		t.Fatalf("read hills.manifest: %v", err)
	}
	return data, manifest
}

// writePair writes name.heightmap + name.manifest into a temp dir and
// returns the .heightmap path.
func writePair(t *testing.T, name string, data, manifest []byte) string {
	t.Helper()
	dir := t.TempDir()
	hmPath := filepath.Join(dir, name+".heightmap")
	manPath := filepath.Join(dir, name+".manifest")
	if err := os.WriteFile(hmPath, data, 0o644); err != nil {
		t.Fatalf("write heightmap: %v", err)
	}
	if err := os.WriteFile(manPath, manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return hmPath
}

// runWith drives run() with the given args and returns (exit code,
// stdout, stderr).
func runWith(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// mutateData returns a deep copy of data with mutate applied, for
// crafting each corruption class from the committed fixture.
func mutateData(data []byte, mutate func([]byte)) []byte {
	out := append([]byte(nil), data...)
	mutate(out)
	return out
}

// mutateManifest returns a re-marshaled copy of the manifest JSON with
// mutate applied, for crafting checksum / redundant-metadata mismatch.
func mutateManifest(t *testing.T, manifest []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	mutate(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return out
}

// TestRunValidReferenceFixture pins WTM-8 valid: the committed reference
// fixture validates with exit 0 and a readable summary naming the dims,
// cell size, origin, y_scale and world bounds of the map.
func TestRunValidReferenceFixture(t *testing.T) {
	data, manifest := readFixture(t)
	hmPath := writePair(t, "hills", data, manifest)

	code, out, errOut := runWith(hmPath)
	if code != exitOK {
		t.Errorf("run(valid fixture) exit = %d, want %d; stderr: %s", code, exitOK, errOut)
	}
	if errOut != "" {
		t.Errorf("run(valid fixture) stderr = %q, want empty", errOut)
	}
	for _, want := range []string{"valid", "32x32", "origin (0, 0)", "y_scale 1", "bounds: x [0, 310]"} {
		if !strings.Contains(out, want) {
			t.Errorf("run(valid fixture) stdout missing %q:\n%s", want, out)
		}
	}
}

// TestRunRejectsCorruptions is the validator error table (WTM-8 invalid,
// task 3.2): every corruption class of the committed fixture must exit 1
// with the actionable reason on stderr — the same sentinel the boot-time
// loader would report (world.Err*).
func TestRunRejectsCorruptions(t *testing.T) {
	validData, validManifest := readFixture(t)

	type corrupt struct {
		name     string
		data     []byte
		manifest []byte
		wantSub  string // expected substring on stderr (the loader reason)
	}
	cases := []corrupt{
		{
			name:    "bad magic",
			data:    mutateData(validData, func(b []byte) { b[0] = 'X' }),
			wantSub: "bad heightmap magic",
		},
		{
			name:    "bad reserved byte",
			data:    mutateData(validData, func(b []byte) { b[7] = 1 }),
			wantSub: "reserved byte",
		},
		{
			name: "bad version",
			data: mutateData(validData, func(b []byte) {
				binary.LittleEndian.PutUint16(b[8:10], 2)
			}),
			wantSub: "unsupported heightmap version",
		},
		{
			name: "zero width",
			data: mutateData(validData, func(b []byte) {
				binary.LittleEndian.PutUint32(b[10:14], 0)
			}),
			wantSub: "dimensions out of range",
		},
		{
			name: "non-finite sample",
			data: mutateData(validData, func(b []byte) {
				binary.LittleEndian.PutUint32(b[34:38], math.Float32bits(float32(math.NaN())))
			}),
			wantSub: "not finite",
		},
		{
			name: "out-of-range sample",
			data: mutateData(validData, func(b []byte) {
				binary.LittleEndian.PutUint32(b[34:38], math.Float32bits(10_001))
			}),
			wantSub: "out of range",
		},
		{
			name:    "truncated body",
			data:    validData[:len(validData)-4],
			wantSub: "body length",
		},
		{
			name: "manifest sha256 mismatch",
			manifest: mutateManifest(t, validManifest, func(m map[string]any) {
				m["sha256"] = strings.Repeat("0", 64)
			}),
			wantSub: "sha256",
		},
		{
			name: "manifest redundant width mismatch",
			manifest: mutateManifest(t, validManifest, func(m map[string]any) {
				m["width"] = 7
			}),
			wantSub: "dims",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.data
			if data == nil {
				data = validData
			}
			manifest := tc.manifest
			if manifest == nil {
				manifest = validManifest
			}
			hmPath := writePair(t, "map", data, manifest)

			code, _, errOut := runWith(hmPath)
			if code != exitInvalid {
				t.Errorf("exit = %d, want %d", code, exitInvalid)
			}
			if !strings.Contains(errOut, tc.wantSub) {
				t.Errorf("stderr missing %q:\n%s", tc.wantSub, errOut)
			}
		})
	}
}

// TestRunUsageErrors pins design D8 exit 2: zero or multiple arguments
// are usage errors and print the usage text to stderr, never to stdout.
func TestRunUsageErrors(t *testing.T) {
	for _, tc := range [][]string{nil, {"a.heightmap", "b.heightmap"}} {
		code, out, errOut := runWith(tc...)
		if code != exitUsage {
			t.Errorf("run(%v) exit = %d, want %d", tc, code, exitUsage)
		}
		if !strings.Contains(errOut, "usage: heightmap-validate") {
			t.Errorf("run(%v) stderr missing usage text:\n%s", tc, errOut)
		}
		if out != "" {
			t.Errorf("run(%v) stdout = %q, want empty (usage goes to stderr)", tc, out)
		}
	}
}

// TestRunMissingFiles: a missing heightmap or a heightmap without its
// manifest sidecar must exit 1 with a reason, never 0.
func TestRunMissingFiles(t *testing.T) {
	dir := t.TempDir()

	code, _, errOut := runWith(filepath.Join(dir, "ghost.heightmap"))
	if code != exitInvalid {
		t.Errorf("missing heightmap exit = %d, want %d", code, exitInvalid)
	}
	if errOut == "" {
		t.Error("missing heightmap stderr is empty, want a reason")
	}

	data, _ := readFixture(t)
	hmPath := filepath.Join(dir, "map.heightmap")
	if err := os.WriteFile(hmPath, data, 0o644); err != nil {
		t.Fatalf("write heightmap: %v", err)
	}
	code, _, errOut = runWith(hmPath)
	if code != exitInvalid {
		t.Errorf("missing manifest exit = %d, want %d", code, exitInvalid)
	}
	if errOut == "" {
		t.Error("missing manifest stderr is empty, want a reason")
	}
}
