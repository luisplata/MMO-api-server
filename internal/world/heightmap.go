package world

// Canonical heightmap loader (design D2/D7, spec WTM-4/WTM-5).
//
// The pinned handoff contract (docs/HEIGHTMAP.md) is a fixed 34-byte
// little-endian header plus a row-major f32 body, shipped with a JSON
// .manifest sidecar:
//
//	0  8  magic = "MMOHMAP" (7 ASCII) + reserved byte 0x00 (nonzero -> reject)
//	8  2  version u16 = 1
//	10 4  width u32 (1..2048)
//	14 4  height u32 (1..2048)
//	18 4  cell_size f32 (>0, world meters/sample)
//	22 4  origin_x f32 (world X of sample (0,0))
//	26 4  origin_z f32 (world Z of sample (0,0))
//	30 4  y_scale f32 (>0; world Y = sample * y_scale)
//	34 .. width*height f32 row-major (row 0 = min Z, col 0 = min X)
//
// Samples MUST be finite and |sample * y_scale| <= 1e4 (world meters);
// there is NO no-data sentinel — the author bakes gaps into the grid.
// Body is bounded at 16 MiB (2048x2048x4). The .manifest sidecar
// carries the sha256 of the .heightmap bytes plus redundant metadata;
// the loader verifies the checksum and cross-checks every header
// duplicate (version, dims, cell_size, origin, y_scale). ANY failure is
// a wrapped %w error and the caller must refuse to start — there is
// never a partial map.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// Pinned format constants (design D2, docs/HEIGHTMAP.md).
const (
	// heightmapMagic is the 7-ASCII-byte magic of the .heightmap header.
	heightmapMagic = "MMOHMAP"
	// heightmapVersion is the only supported format version.
	heightmapVersion uint16 = 1
	// headerSize is the fixed header length: 8 + 2 + 4 + 4 + 4 + 4 + 4 + 4.
	headerSize = 34
	// maxDim bounds each grid dimension (1..2048).
	maxDim = 2048
	// maxBodySize bounds the sample body at 16 MiB (2048*2048*4).
	maxBodySize = 16 * 1024 * 1024
	// maxWorldHeight is the documented safe height range in world
	// meters: |sample * y_scale| <= 1e4.
	maxWorldHeight = 1e4
)

// Loader error sentinels — each corruption class is distinguishable so
// callers (cmd/server, cmd/heightmap-validate) can report the reason.
var (
	// ErrTruncated wraps a file shorter than the fixed header or with a
	// body length that does not match width*height*4.
	ErrTruncated = errors.New("world: truncated heightmap")
	// ErrBadMagic wraps a wrong "MMOHMAP" magic.
	ErrBadMagic = errors.New("world: bad heightmap magic")
	// ErrBadReserved wraps a nonzero reserved byte.
	ErrBadReserved = errors.New("world: heightmap reserved byte must be 0")
	// ErrBadVersion wraps an unsupported format version.
	ErrBadVersion = errors.New("world: unsupported heightmap version")
	// ErrBadDims wraps dimensions outside 1..2048.
	ErrBadDims = errors.New("world: heightmap dimensions out of range")
	// ErrBadMetadata wraps non-finite or non-positive cell_size/y_scale
	// or non-finite origin values.
	ErrBadMetadata = errors.New("world: invalid heightmap metadata")
	// ErrBadBody wraps a body length mismatch (truncated or trailing).
	ErrBadBody = errors.New("world: heightmap body length mismatch")
	// ErrNonFinite wraps a NaN or infinite sample.
	ErrNonFinite = errors.New("world: heightmap sample is not finite")
	// ErrOutOfRange wraps a sample whose world height exceeds the
	// documented safe range.
	ErrOutOfRange = errors.New("world: heightmap sample out of range")
	// ErrManifest wraps any manifest failure: unreadable JSON, sha256
	// mismatch, or redundant metadata that contradicts the header.
	ErrManifest = errors.New("world: heightmap manifest mismatch")
)

// Manifest mirrors the <name>.manifest JSON sidecar (design D2). The
// loader cross-checks the header-redundant fields (version, width,
// height, cell_size, origin_x, origin_z, y_scale) and the sha256; the
// file name and min/max heights are informational.
type Manifest struct {
	File      string  `json:"file"`
	SHA256    string  `json:"sha256"`
	Version   uint16  `json:"version"`
	Width     uint32  `json:"width"`
	Height    uint32  `json:"height"`
	CellSize  float32 `json:"cell_size"`
	OriginX   float32 `json:"origin_x"`
	OriginZ   float32 `json:"origin_z"`
	YScale    float32 `json:"y_scale"`
	MinHeight float32 `json:"min_height"`
	MaxHeight float32 `json:"max_height"`
}

// LoadHeightmap reads a canonical <name>.heightmap with its <name>
// .manifest sidecar and returns the parsed Heightfield (design D7). It
// derives the manifest path from the .heightmap path by swapping the
// extension. Any validation failure is wrapped (%w) and returns no
// partial map — the server must refuse to start.
func LoadHeightmap(path string) (*Heightfield, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("world: read heightmap %s: %w", path, err)
	}
	manifestPath := manifestPathFor(path)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("world: read manifest %s: %w", manifestPath, err)
	}
	return parseHeightmap(data, manifestData)
}

// parseHeightmap is the validation core shared by LoadHeightmap and the
// embedded default map: it runs every pinned check in order and fails
// fast at the first violation.
func parseHeightmap(data, manifestData []byte) (*Heightfield, error) {
	var man Manifest
	if err := json.Unmarshal(manifestData, &man); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrManifest, err)
	}
	if len(data) < headerSize {
		return nil, fmt.Errorf("%w: %d bytes, header is %d", ErrTruncated, len(data), headerSize)
	}
	if string(data[0:7]) != heightmapMagic {
		return nil, fmt.Errorf("%w: got %q", ErrBadMagic, data[0:7])
	}
	if data[7] != 0 {
		return nil, fmt.Errorf("%w: got %d", ErrBadReserved, data[7])
	}
	version := binary.LittleEndian.Uint16(data[8:10])
	if version != heightmapVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrBadVersion, version, heightmapVersion)
	}
	width := binary.LittleEndian.Uint32(data[10:14])
	height := binary.LittleEndian.Uint32(data[14:18])
	if width < 1 || width > maxDim || height < 1 || height > maxDim {
		return nil, fmt.Errorf("%w: %dx%d (bounds 1..%d)", ErrBadDims, width, height, maxDim)
	}
	cellSize := math.Float32frombits(binary.LittleEndian.Uint32(data[18:22]))
	originX := math.Float32frombits(binary.LittleEndian.Uint32(data[22:26]))
	originZ := math.Float32frombits(binary.LittleEndian.Uint32(data[26:30]))
	yScale := math.Float32frombits(binary.LittleEndian.Uint32(data[30:34]))
	if !isFinite(cellSize) || cellSize <= 0 {
		return nil, fmt.Errorf("%w: cell_size %v", ErrBadMetadata, cellSize)
	}
	if !isFinite(originX) || !isFinite(originZ) {
		return nil, fmt.Errorf("%w: origin (%v, %v)", ErrBadMetadata, originX, originZ)
	}
	if !isFinite(yScale) || yScale <= 0 {
		return nil, fmt.Errorf("%w: y_scale %v", ErrBadMetadata, yScale)
	}

	bodyLen := int64(width) * int64(height) * 4
	if bodyLen > maxBodySize {
		return nil, fmt.Errorf("%w: body %d exceeds %d bytes", ErrBadBody, bodyLen, maxBodySize)
	}
	if int64(len(data)-headerSize) != bodyLen {
		return nil, fmt.Errorf("%w: file body %d, want %d", ErrBadBody, len(data)-headerSize, bodyLen)
	}

	samples := make([]float32, width*height)
	for i := range samples {
		s := math.Float32frombits(binary.LittleEndian.Uint32(data[headerSize+i*4:]))
		if !isFinite(s) {
			return nil, fmt.Errorf("%w: sample[%d] = %v", ErrNonFinite, i, s)
		}
		if world := s * yScale; world > maxWorldHeight || world < -maxWorldHeight {
			return nil, fmt.Errorf("%w: sample[%d] = %v (world %v), bounds +-%v", ErrOutOfRange, i, s, world, maxWorldHeight)
		}
		samples[i] = s
	}

	if err := verifyManifest(man, data, version, width, height, cellSize, originX, originZ, yScale); err != nil {
		return nil, err
	}
	return &Heightfield{
		Width:    width,
		Height:   height,
		Samples:  samples,
		OriginX:  originX,
		OriginZ:  originZ,
		CellSize: cellSize,
		YScale:   yScale,
	}, nil
}

// verifyManifest cross-checks the sha256 over the .heightmap bytes and
// every header-redundant field against the parsed header (design D7:
// checksum + redundant metadata; min_height/max_height stay
// informational).
func verifyManifest(man Manifest, data []byte, version uint16, width, height uint32, cellSize, originX, originZ, yScale float32) error {
	sum := sha256.Sum256(data)
	if man.SHA256 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%w: sha256 %s, want %x", ErrManifest, man.SHA256, sum[:])
	}
	if man.Version != version {
		return fmt.Errorf("%w: version %d, want %d", ErrManifest, man.Version, version)
	}
	if man.Width != width || man.Height != height {
		return fmt.Errorf("%w: dims %dx%d, want %dx%d", ErrManifest, man.Width, man.Height, width, height)
	}
	if man.CellSize != cellSize {
		return fmt.Errorf("%w: cell_size %v, want %v", ErrManifest, man.CellSize, cellSize)
	}
	if man.OriginX != originX || man.OriginZ != originZ {
		return fmt.Errorf("%w: origin (%v, %v), want (%v, %v)", ErrManifest, man.OriginX, man.OriginZ, originX, originZ)
	}
	if man.YScale != yScale {
		return fmt.Errorf("%w: y_scale %v, want %v", ErrManifest, man.YScale, yScale)
	}
	return nil
}

// manifestPathFor derives the sidecar path: <name>.heightmap becomes
// <name>.manifest (the pinned sidecar naming).
func manifestPathFor(heightmapPath string) string {
	ext := ".heightmap"
	if strings.HasSuffix(heightmapPath, ext) {
		return strings.TrimSuffix(heightmapPath, ext) + ".manifest"
	}
	return heightmapPath + ".manifest"
}

// isFinite reports whether f is neither NaN nor ±Inf.
func isFinite(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0)
}
