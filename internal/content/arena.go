package content

// Canonical arena loader and heightmap alignment gate (M1).
//
// data/canonical/map.json describes carnaval-solar-v1: world extent,
// playable bounds, the heightmap identity, 18 pillars, two spawns and the
// water band. The combat slice will consume this geometry; today it is a
// boot-time validation gate so a mismatched client/server artifact pair is
// rejected before any listener opens.
//
// VerifyHeightmapFile is the alignment contract the client asked for: the
// arena's declared sha256/dims/cell/origin/y_scale must agree with the
// real .heightmap + .manifest pair on disk.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/luisplata/mmo-api-server/internal/world"
)

// ErrInvalidArena wraps every arena validation or alignment failure.
var ErrInvalidArena = errors.New("content: invalid canonical arena")

const (
	// expectedPillars is the pinned pillar count for carnaval-solar-v1.
	expectedPillars = 18
	// expectedSpawns is the pinned spawn count (indices 0 and 1).
	expectedSpawns = 2
	// boundsTolerance is the exact-dimension tolerance for bounds/world.
	boundsTolerance = 1e-6
	// coverageTolerance is the derived-coverage tolerance in world units.
	coverageTolerance = 1e-3
	// floatTolerance compares the f32 heightmap header against f64 JSON.
	floatTolerance = 1e-6
)

// Arena mirrors the canonical map.json schema.
type Arena struct {
	MapID      string         `json:"mapId"`
	MapHash    string         `json:"mapHash"`
	World      ArenaWorld     `json:"world"`
	Bounds     ArenaBounds    `json:"bounds"`
	Heightmap  HeightmapBlock `json:"heightmap"`
	Water      []WaterBand    `json:"water"`
	Pillars    []Pillar       `json:"pillars"`
	Spawns     []Spawn        `json:"spawns"`
	VisualOnly []VisualOnly   `json:"visualOnly"`
	Meta       ArenaMeta      `json:"meta"`
}

// ArenaWorld mirrors map.json's world block.
type ArenaWorld struct {
	WidthU    float64 `json:"widthU"`
	HeightU   float64 `json:"heightU"`
	PxPerUnit float64 `json:"pxPerUnit"`
	Origin    string  `json:"origin"`
	Flat      bool    `json:"flat"`
	Height    float64 `json:"height"`
}

// ArenaBounds mirrors the playable bounds.
type ArenaBounds struct {
	MinX          float64 `json:"minX"`
	MaxX          float64 `json:"maxX"`
	MinZ          float64 `json:"minZ"`
	MaxZ          float64 `json:"maxZ"`
	ClampToRadius bool    `json:"clampToRadius"`
}

// HeightmapBlock mirrors the heightmap identity/metadata the arena declares.
type HeightmapBlock struct {
	File      string   `json:"file"`
	SHA256    string   `json:"sha256"`
	Format    string   `json:"format"`
	Version   int      `json:"version"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	CellSize  float64  `json:"cellSize"`
	OriginX   float64  `json:"originX"`
	OriginZ   float64  `json:"originZ"`
	YScale    float64  `json:"yScale"`
	CoverageU Coverage `json:"coverageU"`
}

// Coverage mirrors the declared world-unit coverage of the grid.
type Coverage struct {
	X float64 `json:"x"`
	Z float64 `json:"z"`
}

// WaterBand mirrors one slow-water Z band.
type WaterBand struct {
	Z0         float64 `json:"z0"`
	Z1         float64 `json:"z1"`
	SpeedScale float64 `json:"speedScale"`
}

// Pillar mirrors one pillar circle.
type Pillar struct {
	ID int     `json:"id"`
	X  float64 `json:"x"`
	Z  float64 `json:"z"`
	RU float64 `json:"rU"`
}

// Spawn mirrors one team spawn point.
type Spawn struct {
	Index int     `json:"index"`
	X     float64 `json:"x"`
	Z     float64 `json:"z"`
}

// VisualOnly mirrors a non-collidable decoration hint.
type VisualOnly struct {
	Kind     string  `json:"kind"`
	HeightPx float64 `json:"heightPx"`
}

// ArenaMeta mirrors map.json's generation metadata.
type ArenaMeta struct {
	SourceCommit       string `json:"sourceCommit"`
	GeneratedAt        string `json:"generatedAt"`
	PillarSeed         int    `json:"pillarSeed"`
	PillarRule         string `json:"pillarRule"`
	RadiusIsCollidable bool   `json:"radiusIsCollidable"`
}

// LoadArena reads and validates a canonical map.json from disk.
func LoadArena(path string) (*Arena, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrInvalidArena, path, err)
	}
	return ParseArena(data)
}

// ParseArena decodes and validates canonical arena bytes. It is pure and
// never returns a partially validated Arena.
func ParseArena(data []byte) (*Arena, error) {
	var arena Arena
	if err := json.Unmarshal(data, &arena); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidArena, err)
	}
	if err := validateArena(&arena); err != nil {
		return nil, err
	}
	return &arena, nil
}

// validateArena runs every pinned arena check in order and fails fast.
func validateArena(arena *Arena) error {
	if arena.MapID == "" {
		return fmt.Errorf("%w: mapId is empty", ErrInvalidArena)
	}
	if !isHex(arena.MapHash, 8) {
		return fmt.Errorf("%w: mapHash %q is not 8 hex chars", ErrInvalidArena, arena.MapHash)
	}
	if err := validateWorldAndBounds(arena); err != nil {
		return err
	}
	if err := validateHeightmapBlock(arena); err != nil {
		return err
	}
	if err := validatePillars(arena); err != nil {
		return err
	}
	if err := validateSpawns(arena); err != nil {
		return err
	}
	return validateWater(arena)
}

// validateWorldAndBounds checks positive world dimensions and bounds that
// reproduce them.
func validateWorldAndBounds(arena *Arena) error {
	if arena.World.WidthU <= 0 || arena.World.HeightU <= 0 {
		return fmt.Errorf("%w: world is %vx%v, want both > 0", ErrInvalidArena, arena.World.WidthU, arena.World.HeightU)
	}
	b := arena.Bounds
	if b.MinX >= b.MaxX || b.MinZ >= b.MaxZ {
		return fmt.Errorf("%w: bounds %v..%v x %v..%v need min < max", ErrInvalidArena, b.MinX, b.MaxX, b.MinZ, b.MaxZ)
	}
	if math.Abs((b.MaxX-b.MinX)-arena.World.WidthU) > boundsTolerance {
		return fmt.Errorf("%w: bounds width %v does not match world.widthU %v", ErrInvalidArena, b.MaxX-b.MinX, arena.World.WidthU)
	}
	if math.Abs((b.MaxZ-b.MinZ)-arena.World.HeightU) > boundsTolerance {
		return fmt.Errorf("%w: bounds height %v does not match world.heightU %v", ErrInvalidArena, b.MaxZ-b.MinZ, arena.World.HeightU)
	}
	return nil
}

// validateHeightmapBlock checks the declared heightmap identity and that
// its derived coverage matches the world extent.
func validateHeightmapBlock(arena *Arena) error {
	hm := arena.Heightmap
	if !isHex(hm.SHA256, 64) {
		return fmt.Errorf("%w: heightmap.sha256 %q is not 64 hex chars", ErrInvalidArena, hm.SHA256)
	}
	if hm.Width <= 1 || hm.Height <= 1 {
		return fmt.Errorf("%w: heightmap grid %dx%d needs both > 1", ErrInvalidArena, hm.Width, hm.Height)
	}
	if hm.CellSize <= 0 {
		return fmt.Errorf("%w: heightmap.cellSize %v must be > 0", ErrInvalidArena, hm.CellSize)
	}
	if !isFinite(hm.OriginX) || !isFinite(hm.OriginZ) {
		return fmt.Errorf("%w: heightmap origin (%v, %v) is not finite", ErrInvalidArena, hm.OriginX, hm.OriginZ)
	}
	coverageX := float64(hm.Width-1) * hm.CellSize
	coverageZ := float64(hm.Height-1) * hm.CellSize
	if math.Abs(coverageX-arena.World.WidthU) > coverageTolerance {
		return fmt.Errorf("%w: heightmap covers %v u in X, world.widthU is %v", ErrInvalidArena, coverageX, arena.World.WidthU)
	}
	if math.Abs(coverageZ-arena.World.HeightU) > coverageTolerance {
		return fmt.Errorf("%w: heightmap covers %v u in Z, world.heightU is %v", ErrInvalidArena, coverageZ, arena.World.HeightU)
	}
	return nil
}

// validatePillars checks the pinned count, unique ids and that every
// pillar circle lies fully inside the playable bounds.
func validatePillars(arena *Arena) error {
	if len(arena.Pillars) != expectedPillars {
		return fmt.Errorf("%w: %d pillars, want %d", ErrInvalidArena, len(arena.Pillars), expectedPillars)
	}
	b := arena.Bounds
	seen := make(map[int]struct{}, len(arena.Pillars))
	for i, p := range arena.Pillars {
		if _, dup := seen[p.ID]; dup {
			return fmt.Errorf("%w: duplicate pillar id %d", ErrInvalidArena, p.ID)
		}
		seen[p.ID] = struct{}{}
		if p.RU <= 0 {
			return fmt.Errorf("%w: pillar %d radius %v must be > 0", ErrInvalidArena, p.ID, p.RU)
		}
		if p.X-p.RU < b.MinX || p.X+p.RU > b.MaxX || p.Z-p.RU < b.MinZ || p.Z+p.RU > b.MaxZ {
			return fmt.Errorf("%w: pillar %d at (%v, %v) r %v falls outside the playable bounds", ErrInvalidArena, i, p.X, p.Z, p.RU)
		}
	}
	return nil
}

// validateSpawns checks exactly two spawns, unique indices 0 and 1,
// distinct positions, both inside bounds.
func validateSpawns(arena *Arena) error {
	if len(arena.Spawns) != expectedSpawns {
		return fmt.Errorf("%w: %d spawns, want %d", ErrInvalidArena, len(arena.Spawns), expectedSpawns)
	}
	seen := make(map[int]struct{}, expectedSpawns)
	b := arena.Bounds
	for i, s := range arena.Spawns {
		if s.Index != 0 && s.Index != 1 {
			return fmt.Errorf("%w: spawns[%d] index %d, want 0 or 1", ErrInvalidArena, i, s.Index)
		}
		if _, dup := seen[s.Index]; dup {
			return fmt.Errorf("%w: duplicate spawn index %d", ErrInvalidArena, s.Index)
		}
		seen[s.Index] = struct{}{}
		if s.X < b.MinX || s.X > b.MaxX || s.Z < b.MinZ || s.Z > b.MaxZ {
			return fmt.Errorf("%w: spawn %d at (%v, %v) outside bounds", ErrInvalidArena, s.Index, s.X, s.Z)
		}
	}
	if len(seen) != expectedSpawns {
		return fmt.Errorf("%w: spawn indices %v, want exactly 0 and 1", ErrInvalidArena, seen)
	}
	a, b2 := arena.Spawns[0], arena.Spawns[1]
	if math.Abs(a.X-b2.X) <= floatTolerance && math.Abs(a.Z-b2.Z) <= floatTolerance {
		return fmt.Errorf("%w: spawns 0 and 1 coincide at (%v, %v)", ErrInvalidArena, a.X, a.Z)
	}
	return nil
}

// validateWater checks each water band's ordering, speed scale and Z
// containment.
func validateWater(arena *Arena) error {
	b := arena.Bounds
	for i, band := range arena.Water {
		if band.Z0 >= band.Z1 {
			return fmt.Errorf("%w: water[%d] z0 %v must be < z1 %v", ErrInvalidArena, i, band.Z0, band.Z1)
		}
		if band.SpeedScale <= 0 || band.SpeedScale > 1 {
			return fmt.Errorf("%w: water[%d] speedScale %v outside (0, 1]", ErrInvalidArena, i, band.SpeedScale)
		}
		if band.Z0 < b.MinZ || band.Z1 > b.MaxZ {
			return fmt.Errorf("%w: water[%d] %v..%v outside Z bounds %v..%v", ErrInvalidArena, i, band.Z0, band.Z1, b.MinZ, b.MaxZ)
		}
	}
	return nil
}

// VerifyHeightmapFile is the arena<->heightmap alignment gate (M1): it
// re-hashes the .heightmap bytes against arena.heightmap.sha256, loads the
// pair through world.LoadHeightmap (which verifies the manifest) and
// cross-checks dims, cell size, origin, y_scale and derived coverage.
func VerifyHeightmapFile(arena *Arena, heightmapPath string) error {
	if arena == nil {
		return fmt.Errorf("%w: nil arena", ErrInvalidArena)
	}
	data, err := os.ReadFile(heightmapPath)
	if err != nil {
		return fmt.Errorf("%w: read heightmap %s: %w", ErrInvalidArena, heightmapPath, err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, arena.Heightmap.SHA256) {
		return fmt.Errorf("%w: heightmap sha256 %s, arena expects %s", ErrInvalidArena, got, arena.Heightmap.SHA256)
	}
	heightfield, err := world.LoadHeightmap(heightmapPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidArena, err)
	}
	hm := arena.Heightmap
	if int(heightfield.Width) != hm.Width || int(heightfield.Height) != hm.Height {
		return fmt.Errorf("%w: heightmap grid %dx%d, arena expects %dx%d",
			ErrInvalidArena, heightfield.Width, heightfield.Height, hm.Width, hm.Height)
	}
	if math.Abs(float64(heightfield.CellSize)-hm.CellSize) > floatTolerance {
		return fmt.Errorf("%w: heightmap cellSize %v, arena expects %v", ErrInvalidArena, heightfield.CellSize, hm.CellSize)
	}
	if math.Abs(float64(heightfield.OriginX)-hm.OriginX) > floatTolerance ||
		math.Abs(float64(heightfield.OriginZ)-hm.OriginZ) > floatTolerance {
		return fmt.Errorf("%w: heightmap origin (%v, %v), arena expects (%v, %v)",
			ErrInvalidArena, heightfield.OriginX, heightfield.OriginZ, hm.OriginX, hm.OriginZ)
	}
	if math.Abs(float64(heightfield.YScale)-hm.YScale) > floatTolerance {
		return fmt.Errorf("%w: heightmap yScale %v, arena expects %v", ErrInvalidArena, heightfield.YScale, hm.YScale)
	}
	coverageX := float64(heightfield.Width-1) * float64(heightfield.CellSize)
	coverageZ := float64(heightfield.Height-1) * float64(heightfield.CellSize)
	if math.Abs(coverageX-arena.World.WidthU) > coverageTolerance || math.Abs(coverageZ-arena.World.HeightU) > coverageTolerance {
		return fmt.Errorf("%w: heightmap covers %vx%v u, world is %vx%v",
			ErrInvalidArena, coverageX, coverageZ, arena.World.WidthU, arena.World.HeightU)
	}
	return nil
}

// isHex reports whether s is exactly n hexadecimal characters.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// isFinite reports whether f is neither NaN nor ±Inf.
func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
