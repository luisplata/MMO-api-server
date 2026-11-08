package content

// Canonical arena validation and heightmap alignment tests (M1 gate).
//
// data/canonical/map.json is the real client artifact; corruptions mutate
// its JSON tree. The alignment tests use the real carnaval-solar-v1
// heightmap + manifest pair so the client-requested arena<->heightmap gate
// is exercised against the exact shipped bytes.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rawArena decodes the real map artifact into a mutable JSON tree.
func rawArena(t *testing.T) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(canonicalBytes(t, "map.json"), &raw); err != nil {
		t.Fatalf("decode map.json: %v", err)
	}
	return raw
}

// marshalArena re-encodes a mutated JSON tree.
func marshalArena(t *testing.T, raw map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode mutated arena: %v", err)
	}
	return data
}

// TestParseArenaCanonicalArtifact pins the real client-delivered map:
// identity, world/bounds, heightmap metadata, 18 pillars, 2 spawns and the
// single water band.
func TestParseArenaCanonicalArtifact(t *testing.T) {
	arena, err := ParseArena(canonicalBytes(t, "map.json"))
	if err != nil {
		t.Fatalf("ParseArena(canonical): %v", err)
	}
	if arena.MapID != "carnaval-solar-v1" || arena.MapHash != "5fa2e4ca" {
		t.Errorf("identity = %q/%q, want carnaval-solar-v1/5fa2e4ca", arena.MapID, arena.MapHash)
	}
	if arena.World.WidthU != 130 || arena.World.HeightU != 85 {
		t.Errorf("world = %vx%v, want 130x85", arena.World.WidthU, arena.World.HeightU)
	}
	if arena.Bounds.MinX != 0 || arena.Bounds.MaxX != 130 || arena.Bounds.MinZ != 0 || arena.Bounds.MaxZ != 85 {
		t.Errorf("bounds = %+v, want 0..130 x 0..85", arena.Bounds)
	}
	hm := arena.Heightmap
	if hm.SHA256 != "5fa2e4ca78d53407f39097666f66cf386038d69de2e777e471042741ac73d511" {
		t.Errorf("heightmap sha256 = %q", hm.SHA256)
	}
	if hm.Width != 131 || hm.Height != 86 || hm.CellSize != 1 {
		t.Errorf("heightmap grid = %dx%d cell %v, want 131x86 cell 1", hm.Width, hm.Height, hm.CellSize)
	}
	if got := len(arena.Pillars); got != 18 {
		t.Fatalf("pillars = %d, want 18", got)
	}
	if got := len(arena.Spawns); got != 2 {
		t.Fatalf("spawns = %d, want 2", got)
	}
	if arena.Spawns[0].Index != 0 || arena.Spawns[0].X != 65 || arena.Spawns[0].Z != 68.5 {
		t.Errorf("spawn[0] = %+v, want index 0 at (65, 68.5)", arena.Spawns[0])
	}
	if arena.Spawns[1].Index != 1 || arena.Spawns[1].X != 65 || arena.Spawns[1].Z != 16.5 {
		t.Errorf("spawn[1] = %+v, want index 1 at (65, 16.5)", arena.Spawns[1])
	}
	if got := len(arena.Water); got != 1 {
		t.Fatalf("water bands = %d, want 1", got)
	}
	if band := arena.Water[0]; band.Z0 != 38 || band.Z1 != 47 || band.SpeedScale != 0.7 {
		t.Errorf("water band = %+v, want 38..47 @ 0.7", band)
	}
}

// TestParseArenaRejectsCorruptions is the arena fail-fast table.
func TestParseArenaRejectsCorruptions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, raw map[string]any)
	}{
		{"wrong pillar count", func(t *testing.T, raw map[string]any) {
			pillars := raw["pillars"].([]any)
			raw["pillars"] = pillars[:17]
		}},
		{"duplicate pillar id", func(t *testing.T, raw map[string]any) {
			raw["pillars"].([]any)[1].(map[string]any)["id"] = 0
		}},
		{"pillar outside bounds", func(t *testing.T, raw map[string]any) {
			raw["pillars"].([]any)[0].(map[string]any)["x"] = -10
		}},
		{"pillar center too close to edge for radius", func(t *testing.T, raw map[string]any) {
			raw["pillars"].([]any)[0].(map[string]any)["x"] = 0.5
		}},
		{"bad coverage", func(t *testing.T, raw map[string]any) {
			raw["heightmap"].(map[string]any)["width"] = 130
		}},
		{"non-hex map hash", func(t *testing.T, raw map[string]any) {
			raw["mapHash"] = "zzzzzzzz"
		}},
		{"short heightmap sha", func(t *testing.T, raw map[string]any) {
			raw["heightmap"].(map[string]any)["sha256"] = "abc"
		}},
		{"duplicate spawn index", func(t *testing.T, raw map[string]any) {
			raw["spawns"].([]any)[1].(map[string]any)["index"] = 0
		}},
		{"spawn outside bounds", func(t *testing.T, raw map[string]any) {
			raw["spawns"].([]any)[0].(map[string]any)["x"] = -1
		}},
		{"spawns not distinct", func(t *testing.T, raw map[string]any) {
			spawns := raw["spawns"].([]any)
			spawns[1].(map[string]any)["x"] = spawns[0].(map[string]any)["x"]
			spawns[1].(map[string]any)["z"] = spawns[0].(map[string]any)["z"]
		}},
		{"water z0 not below z1", func(t *testing.T, raw map[string]any) {
			raw["water"].([]any)[0].(map[string]any)["z0"] = 50
		}},
		{"water speedScale zero", func(t *testing.T, raw map[string]any) {
			raw["water"].([]any)[0].(map[string]any)["speedScale"] = 0
		}},
		{"water outside z bounds", func(t *testing.T, raw map[string]any) {
			raw["water"].([]any)[0].(map[string]any)["z1"] = 90
		}},
		{"world width mismatch with bounds", func(t *testing.T, raw map[string]any) {
			raw["world"].(map[string]any)["widthU"] = 131
		}},
		{"empty map id", func(t *testing.T, raw map[string]any) {
			raw["mapId"] = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawArena(t)
			tc.mutate(t, raw)
			if _, err := ParseArena(marshalArena(t, raw)); !errors.Is(err, ErrInvalidArena) {
				t.Fatalf("ParseArena() error = %v, want ErrInvalidArena", err)
			}
		})
	}
}

// TestParseArenaRejectsInvalidJSON pins malformed JSON rejection.
func TestParseArenaRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseArena([]byte("{not json")); !errors.Is(err, ErrInvalidArena) {
		t.Fatalf("ParseArena(malformed) error = %v, want ErrInvalidArena", err)
	}
}

// TestLoadArenaReadsFromDisk pins the path loader.
func TestLoadArenaReadsFromDisk(t *testing.T) {
	path := filepath.Join(canonicalDir(t), "map.json")
	arena, err := LoadArena(path)
	if err != nil {
		t.Fatalf("LoadArena: %v", err)
	}
	if arena.MapID != "carnaval-solar-v1" || len(arena.Pillars) != 18 {
		t.Errorf("LoadArena returned %q/%d pillars, want carnaval-solar-v1/18", arena.MapID, len(arena.Pillars))
	}
	if _, err := LoadArena(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadArena(missing) must fail")
	}
}

// copyCanonicalPair copies the real heightmap + manifest into dir and
// returns the copied heightmap path, so tamper tests never touch the
// committed artifacts.
func copyCanonicalPair(t *testing.T) (dir, heightmapPath, manifestPath string) {
	t.Helper()
	dir = t.TempDir()
	heightmapPath = filepath.Join(dir, "carnaval-solar-v1.heightmap")
	manifestPath = filepath.Join(dir, "carnaval-solar-v1.manifest")
	if err := os.WriteFile(heightmapPath, canonicalBytes(t, "carnaval-solar-v1.heightmap"), 0o644); err != nil {
		t.Fatalf("copy heightmap: %v", err)
	}
	if err := os.WriteFile(manifestPath, canonicalBytes(t, "carnaval-solar-v1.manifest"), 0o644); err != nil {
		t.Fatalf("copy manifest: %v", err)
	}
	return dir, heightmapPath, manifestPath
}

// TestVerifyHeightmapFileAcceptsCanonicalPair is the M1 alignment gate: the
// real arena and the real heightmap pair agree.
func TestVerifyHeightmapFileAcceptsCanonicalPair(t *testing.T) {
	arena, err := LoadArena(filepath.Join(canonicalDir(t), "map.json"))
	if err != nil {
		t.Fatalf("LoadArena: %v", err)
	}
	heightmapPath := filepath.Join(canonicalDir(t), "carnaval-solar-v1.heightmap")
	if err := VerifyHeightmapFile(arena, heightmapPath); err != nil {
		t.Fatalf("VerifyHeightmapFile(canonical) = %v, want nil", err)
	}
}

// TestVerifyHeightmapFileRejectsTampering is the negative side of the M1
// gate: a single flipped heightmap byte, a mismatched arena hash, a
// tampered manifest and mismatched arena dimensions must all fail.
func TestVerifyHeightmapFileRejectsTampering(t *testing.T) {
	t.Run("flipped heightmap byte", func(t *testing.T) {
		_, heightmapPath, _ := copyCanonicalPair(t)
		data, err := os.ReadFile(heightmapPath)
		if err != nil {
			t.Fatalf("read copy: %v", err)
		}
		data[len(data)-1] ^= 0xff
		if err := os.WriteFile(heightmapPath, data, 0o644); err != nil {
			t.Fatalf("write tampered heightmap: %v", err)
		}
		arena, err := LoadArena(filepath.Join(canonicalDir(t), "map.json"))
		if err != nil {
			t.Fatalf("LoadArena: %v", err)
		}
		if err := VerifyHeightmapFile(arena, heightmapPath); !errors.Is(err, ErrInvalidArena) {
			t.Fatalf("VerifyHeightmapFile(tampered heightmap) = %v, want ErrInvalidArena", err)
		}
	})

	t.Run("mismatched arena hash", func(t *testing.T) {
		arena, err := LoadArena(filepath.Join(canonicalDir(t), "map.json"))
		if err != nil {
			t.Fatalf("LoadArena: %v", err)
		}
		arena.Heightmap.SHA256 = strings.Repeat("0", 64)
		heightmapPath := filepath.Join(canonicalDir(t), "carnaval-solar-v1.heightmap")
		if err := VerifyHeightmapFile(arena, heightmapPath); !errors.Is(err, ErrInvalidArena) {
			t.Fatalf("VerifyHeightmapFile(hash mismatch) = %v, want ErrInvalidArena", err)
		}
	})

	t.Run("tampered manifest", func(t *testing.T) {
		_, heightmapPath, manifestPath := copyCanonicalPair(t)
		var man map[string]any
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		if err := json.Unmarshal(data, &man); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		man["sha256"] = strings.Repeat("0", 64)
		tampered, err := json.Marshal(man)
		if err != nil {
			t.Fatalf("encode manifest: %v", err)
		}
		if err := os.WriteFile(manifestPath, tampered, 0o644); err != nil {
			t.Fatalf("write tampered manifest: %v", err)
		}
		arena, err := LoadArena(filepath.Join(canonicalDir(t), "map.json"))
		if err != nil {
			t.Fatalf("LoadArena: %v", err)
		}
		if err := VerifyHeightmapFile(arena, heightmapPath); !errors.Is(err, ErrInvalidArena) {
			t.Fatalf("VerifyHeightmapFile(tampered manifest) = %v, want ErrInvalidArena", err)
		}
	})

	t.Run("arena dimension mismatch", func(t *testing.T) {
		arena, err := LoadArena(filepath.Join(canonicalDir(t), "map.json"))
		if err != nil {
			t.Fatalf("LoadArena: %v", err)
		}
		arena.Heightmap.Width = 132
		heightmapPath := filepath.Join(canonicalDir(t), "carnaval-solar-v1.heightmap")
		if err := VerifyHeightmapFile(arena, heightmapPath); !errors.Is(err, ErrInvalidArena) {
			t.Fatalf("VerifyHeightmapFile(dim mismatch) = %v, want ErrInvalidArena", err)
		}
	})

	t.Run("missing heightmap", func(t *testing.T) {
		arena, err := LoadArena(filepath.Join(canonicalDir(t), "map.json"))
		if err != nil {
			t.Fatalf("LoadArena: %v", err)
		}
		if err := VerifyHeightmapFile(arena, filepath.Join(t.TempDir(), "ghost.heightmap")); !errors.Is(err, ErrInvalidArena) {
			t.Fatalf("VerifyHeightmapFile(missing) = %v, want ErrInvalidArena", err)
		}
	})
}
