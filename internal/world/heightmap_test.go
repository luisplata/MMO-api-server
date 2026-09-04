package world

// Heightmap loader tests (design D7, spec WTM-4/WTM-5).
//
// The test builds canonical .heightmap bytes in memory with the exact
// pinned contract layout (34-byte little-endian header + row-major f32
// samples), writes them to a temp dir with a matching .manifest, and
// asserts the loader accepts valid pairs and rejects every corruption
// class with a wrapped sentinel error — magic, reserved byte, version,
// dims, metadata, body length, non-finite samples, out-of-range samples,
// checksum and redundant-metadata mismatches.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// encodeHeightmap builds canonical .heightmap bytes per the pinned
// contract: "MMOHMAP" + reserved 0x00, version u16 LE, width/height u32
// LE, cell_size/origin_x/origin_z/y_scale f32 LE, then width*height
// row-major f32 samples. Tests use it both for valid fixtures and for
// crafting each corruption class.
func encodeHeightmap(t *testing.T, w, h uint32, cellSize, originX, originZ, yScale float32, samples []float32) []byte {
	t.Helper()
	buf := make([]byte, headerSize+len(samples)*4)
	copy(buf[0:], "MMOHMAP")
	buf[7] = 0
	binary.LittleEndian.PutUint16(buf[8:10], heightmapVersion)
	binary.LittleEndian.PutUint32(buf[10:14], w)
	binary.LittleEndian.PutUint32(buf[14:18], h)
	binary.LittleEndian.PutUint32(buf[18:22], math.Float32bits(cellSize))
	binary.LittleEndian.PutUint32(buf[22:26], math.Float32bits(originX))
	binary.LittleEndian.PutUint32(buf[26:30], math.Float32bits(originZ))
	binary.LittleEndian.PutUint32(buf[30:34], math.Float32bits(yScale))
	for i, s := range samples {
		binary.LittleEndian.PutUint32(buf[headerSize+i*4:], math.Float32bits(s))
	}
	return buf
}

// canonicalField is a small valid grid: 3x2, cell 10, y_scale 1, origin
// (0,0) — samples in a rising plane so every value is trivially
// verifiable.
func canonicalField() (w, h uint32, cellSize, originX, originZ, yScale float32, samples []float32) {
	return 3, 2, 10, 0, 0, 1, []float32{0, 10, 20, 100, 110, 120}
}

// manifestFor builds the canonical JSON manifest for the given
// .heightmap bytes and identity fields. sha256 is computed from data.
func manifestFor(file string, data []byte, version uint16, width, height uint32, cellSize, originX, originZ, yScale, minH, maxH float32) []byte {
	sum := sha256.Sum256(data)
	m := Manifest{
		File:      file,
		SHA256:    hex.EncodeToString(sum[:]),
		Version:   version,
		Width:     width,
		Height:    height,
		CellSize:  cellSize,
		OriginX:   originX,
		OriginZ:   originZ,
		YScale:    yScale,
		MinHeight: minH,
		MaxHeight: maxH,
	}
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
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

// TestLoadHeightmapValidRoundTrip pins WTM-4 load: a canonical
// .heightmap + matching .manifest loads into the exact Heightfield the
// bytes describe.
func TestLoadHeightmapValidRoundTrip(t *testing.T) {
	w, h, cellSize, originX, originZ, yScale, samples := canonicalField()
	data := encodeHeightmap(t, w, h, cellSize, originX, originZ, yScale, samples)
	man := manifestFor("map", data, heightmapVersion, w, h, cellSize, originX, originZ, yScale, 0, 120)
	hmPath := writePair(t, "map", data, man)

	got, err := LoadHeightmap(hmPath)
	if err != nil {
		t.Fatalf("LoadHeightmap: %v", err)
	}
	if got.Width != w || got.Height != h {
		t.Errorf("dims = %dx%d, want %dx%d", got.Width, got.Height, w, h)
	}
	if got.CellSize != cellSize || got.OriginX != originX || got.OriginZ != originZ || got.YScale != yScale {
		t.Errorf("metadata = cell %v origin (%v,%v) yscale %v, want %v (%v,%v) %v",
			got.CellSize, got.OriginX, got.OriginZ, got.YScale, cellSize, originX, originZ, yScale)
	}
	if len(got.Samples) != len(samples) {
		t.Fatalf("sample count = %d, want %d", len(got.Samples), len(samples))
	}
	for i, s := range samples {
		if got.Samples[i] != s {
			t.Errorf("sample[%d] = %v, want %v", i, got.Samples[i], s)
		}
	}
	// Sanity: the loaded grid must sample like the source data.
	if got.HeightAt(25, 10) != 120 { // sample (2,1)
		t.Errorf("HeightAt(25, 10) = %v, want 120", got.HeightAt(25, 10))
	}
}

// TestLoadHeightmapRejectsCorruptions is the loader error table (WTM-4
// magic/version, WTM-5 corrupt/non-finite): every corruption class must
// fail LoadHeightmap wrapped with its sentinel — never a partial map.
func TestLoadHeightmapRejectsCorruptions(t *testing.T) {
	w, h, cellSize, originX, originZ, yScale, samples := canonicalField()
	valid := encodeHeightmap(t, w, h, cellSize, originX, originZ, yScale, samples)
	validMan := manifestFor("map", valid, heightmapVersion, w, h, cellSize, originX, originZ, yScale, 0, 120)

	// corruptions builds a (name, data, manifest, wantErr) row. mutate
	// receives a deep copy of the valid bytes and may rewrite it.
	type corruption struct {
		name string
		data func([]byte) []byte
		man  func() []byte
		want error
	}
	cases := []corruption{
		{
			name: "wrong magic",
			data: func(b []byte) []byte { b[0] = 'X'; return b },
			want: ErrBadMagic,
		},
		{
			name: "reserved byte nonzero",
			data: func(b []byte) []byte { b[7] = 1; return b },
			want: ErrBadReserved,
		},
		{
			name: "unsupported version",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint16(b[8:10], 2); return b },
			want: ErrBadVersion,
		},
		{
			name: "zero width",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[10:14], 0); return b },
			want: ErrBadDims,
		},
		{
			name: "width above max",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[10:14], 2049); return b },
			want: ErrBadDims,
		},
		{
			name: "zero height",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[14:18], 0); return b },
			want: ErrBadDims,
		},
		{
			name: "height above max",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[14:18], 2049); return b },
			want: ErrBadDims,
		},
		{
			name: "zero cell_size",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[18:22], math.Float32bits(0)); return b },
			want: ErrBadMetadata,
		},
		{
			name: "negative cell_size",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[18:22], math.Float32bits(-5)); return b },
			want: ErrBadMetadata,
		},
		{
			name: "NaN cell_size",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[18:22], math.Float32bits(float32(math.NaN())))
				return b
			},
			want: ErrBadMetadata,
		},
		{
			name: "NaN origin_x",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[22:26], math.Float32bits(float32(math.NaN())))
				return b
			},
			want: ErrBadMetadata,
		},
		{
			name: "infinite origin_z",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[26:30], math.Float32bits(float32(math.Inf(1))))
				return b
			},
			want: ErrBadMetadata,
		},
		{
			name: "zero y_scale",
			data: func(b []byte) []byte { binary.LittleEndian.PutUint32(b[30:34], math.Float32bits(0)); return b },
			want: ErrBadMetadata,
		},
		{
			name: "truncated body",
			data: func(b []byte) []byte { return b[:len(b)-4] },
			want: ErrBadBody,
		},
		{
			name: "trailing garbage after body",
			data: func(b []byte) []byte { return append(b, 0xAB, 0xCD) },
			want: ErrBadBody,
		},
		{
			name: "NaN sample",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[headerSize+2*4:], math.Float32bits(float32(math.NaN())))
				return b
			},
			want: ErrNonFinite,
		},
		{
			name: "infinite sample",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[headerSize+3*4:], math.Float32bits(float32(math.Inf(-1))))
				return b
			},
			want: ErrNonFinite,
		},
		{
			name: "sample out of range",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[headerSize+4*4:], math.Float32bits(10_001))
				return b
			},
			want: ErrOutOfRange,
		},
		{
			name: "sample exceeds range after y_scale",
			data: func(b []byte) []byte {
				binary.LittleEndian.PutUint32(b[headerSize+4*4:], math.Float32bits(5000))
				binary.LittleEndian.PutUint32(b[30:34], math.Float32bits(3))
				return b
			},
			want: ErrOutOfRange,
		},
		{
			name: "sha256 mismatch",
			data: func(b []byte) []byte { return b },
			man: func() []byte {
				sum := sha256.Sum256([]byte("tampered"))
				m := Manifest{SHA256: hex.EncodeToString(sum[:])}
				out, _ := json.Marshal(m)
				return out
			},
			want: ErrManifest,
		},
		{
			name: "redundant width mismatch",
			data: func(b []byte) []byte { return b },
			man: func() []byte {
				return manifestFor("map", valid, heightmapVersion, 7, h, cellSize, originX, originZ, yScale, 0, 120)
			},
			want: ErrManifest,
		},
		{
			name: "redundant version mismatch",
			data: func(b []byte) []byte { return b },
			man:  func() []byte { return manifestFor("map", valid, 99, w, h, cellSize, originX, originZ, yScale, 0, 120) },
			want: ErrManifest,
		},
		{
			name: "redundant cell_size mismatch",
			data: func(b []byte) []byte { return b },
			man: func() []byte {
				return manifestFor("map", valid, heightmapVersion, w, h, 99, originX, originZ, yScale, 0, 120)
			},
			want: ErrManifest,
		},
		{
			name: "redundant origin_x mismatch",
			data: func(b []byte) []byte { return b },
			man: func() []byte {
				return manifestFor("map", valid, heightmapVersion, w, h, cellSize, 123, originZ, yScale, 0, 120)
			},
			want: ErrManifest,
		},
		{
			name: "redundant y_scale mismatch",
			data: func(b []byte) []byte { return b },
			man: func() []byte {
				return manifestFor("map", valid, heightmapVersion, w, h, cellSize, originX, originZ, 5, 0, 120)
			},
			want: ErrManifest,
		},
		{
			name: "manifest is not JSON",
			data: func(b []byte) []byte { return b },
			man:  func() []byte { return []byte("{not json") },
			want: ErrManifest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			if tc.data != nil {
				data = tc.data(data)
			}
			man := validMan
			if tc.man != nil {
				man = tc.man()
			}
			hmPath := writePair(t, "map", data, man)
			_, err := LoadHeightmap(hmPath)
			if !errors.Is(err, tc.want) {
				t.Errorf("LoadHeightmap err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestLoadHeightmapTooShort: a file shorter than the 34-byte header must
// fail as truncated, never parse a partial header.
func TestLoadHeightmapTooShort(t *testing.T) {
	dir := t.TempDir()
	hmPath := filepath.Join(dir, "tiny.heightmap")
	manPath := filepath.Join(dir, "tiny.manifest")
	if err := os.WriteFile(hmPath, []byte("MMOHMAP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadHeightmap(hmPath)
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("err = %v, want ErrTruncated", err)
	}
}

// TestLoadHeightmapMissingFiles: a missing .heightmap or missing
// .manifest must surface as an error (wrapped os error), not a panic.
func TestLoadHeightmapMissingFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadHeightmap(filepath.Join(dir, "ghost.heightmap"))
	if err == nil {
		t.Errorf("missing heightmap file must error")
	}
	hmPath := filepath.Join(dir, "map.heightmap")
	if err := os.WriteFile(hmPath, encodeHeightmap(t, 1, 1, 1, 0, 0, 1, []float32{0}), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .manifest sidecar.
	if _, err := LoadHeightmap(hmPath); err == nil {
		t.Errorf("missing manifest must error")
	}
}

// TestLoadHeightmapExactBodyBound proves the loader accepts the maximum
// documented body: 2048x2048 samples = 16 MiB (design D2 bounded dims).
func TestLoadHeightmapExactBodyBound(t *testing.T) {
	const dim = 2048
	samples := make([]float32, dim*dim)
	for i := range samples {
		samples[i] = 0.5
	}
	data := encodeHeightmap(t, dim, dim, 1, 0, 0, 1, samples)
	man := manifestFor("big", data, heightmapVersion, dim, dim, 1, 0, 0, 1, 0.5, 0.5)
	hmPath := writePair(t, "big", data, man)
	got, err := LoadHeightmap(hmPath)
	if err != nil {
		t.Fatalf("LoadHeightmap(max dims): %v", err)
	}
	if got.Width != dim || got.Height != dim {
		t.Errorf("dims = %dx%d, want %dx%d", got.Width, got.Height, dim, dim)
	}
}

// TestManifestMinMaxAreInformational: the manifest min_height/max_height
// are documented but NOT cross-checked (only header-redundant fields
// are); a stale min/max must still load.
func TestManifestMinMaxAreInformational(t *testing.T) {
	w, h, cellSize, originX, originZ, yScale, samples := canonicalField()
	data := encodeHeightmap(t, w, h, cellSize, originX, originZ, yScale, samples)
	man := manifestFor("map", data, heightmapVersion, w, h, cellSize, originX, originZ, yScale, 999, -999)
	hmPath := writePair(t, "map", data, man)
	if _, err := LoadHeightmap(hmPath); err != nil {
		t.Errorf("stale min/max must not fail the load: %v", err)
	}
}
