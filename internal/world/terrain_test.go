package world

// Heightfield sampling tests (design D3, spec WTM-2/WTM-3/WTM-6).
//
// Coverage: bilinear interpolation on a 4-corner ramp (interior blend,
// exact at samples), edge/corner OOB clamping (never panics, never
// errors), degenerate 1-wide grids, non-unit cell_size/y_scale, and
// determinism (identical fields yield identical heights).

import (
	"testing"
)

// rampField is a 2x2 bilinear ramp over [0,5]x[0,5]: samples rise
// 100 per X step and 200 per Z step, so HeightAt must blend them
// exactly like a plane.
func rampField() *Heightfield {
	return &Heightfield{
		Width:    2,
		Height:   2,
		Samples:  []float32{0, 100, 200, 300}, // row-major: (0,0),(1,0),(0,1),(1,1)
		OriginX:  0,
		OriginZ:  0,
		CellSize: 5,
		YScale:   1,
	}
}

func almostHeight(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-4
}

// TestHeightAtInteriorBilinear pins WTM-2: inside the 4-corner ramp the
// height is the bilinear blend of the four surrounding samples. With a
// plane the blend is exact: 100*fx + 200*fz.
func TestHeightAtInteriorBilinear(t *testing.T) {
	h := rampField()
	cases := []struct {
		name string
		x, z float32
		want float32
	}{
		{"ramp midpoint", 2.5, 2.5, 150},      // 100*0.5 + 200*0.5
		{"spec WTM-2 point", 3.5, 4.25, 240},  // 100*0.7 + 200*0.85
		{"near origin corner", 0.5, 0.25, 20}, // 100*0.1 + 200*0.05
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.HeightAt(tc.x, tc.z); !almostHeight(got, tc.want) {
				t.Errorf("HeightAt(%v, %v) = %v, want %v", tc.x, tc.z, got, tc.want)
			}
		})
	}
}

// TestHeightAtExactAtSamples pins WTM-2: sampling exactly on a grid
// point returns that sample's height (sample x y_scale), with zero
// influence from the neighbors.
func TestHeightAtExactAtSamples(t *testing.T) {
	h := rampField()
	cases := []struct {
		x, z float32
		want float32
	}{
		{0, 0, 0},
		{5, 0, 100},
		{0, 5, 200},
		{5, 5, 300},
	}
	for _, tc := range cases {
		if got := h.HeightAt(tc.x, tc.z); got != tc.want {
			t.Errorf("HeightAt(%v, %v) = %v, want %v (exact sample)", tc.x, tc.z, got, tc.want)
		}
	}
}

// TestHeightAtOOBClampsToEdge pins WTM-3: coordinates outside the grid
// clamp to the nearest edge/corner sample instead of panicking or
// wrapping.
func TestHeightAtOOBClampsToEdge(t *testing.T) {
	h := rampField()
	cases := []struct {
		name string
		x, z float32
		want float32
	}{
		{"west of grid", -3, 5, 200},    // left edge at top row: sample (0,1)
		{"north of grid", 2.5, 99, 250}, // top row at mid column: 200 + (300-200)*0.5
		{"south of grid", 2.5, -99, 50}, // bottom row at mid column: 0 + (100-0)*0.5
		{"east of grid", 99, 2.5, 200},  // right column at mid row: 100 + (300-100)*0.5
		{"far corner south-west", -99, -99, 0},
		{"far corner north-east", 99, 99, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.HeightAt(tc.x, tc.z); !almostHeight(got, tc.want) {
				t.Errorf("HeightAt(%v, %v) = %v, want %v (clamped edge)", tc.x, tc.z, got, tc.want)
			}
		})
	}
}

// TestHeightAtDegenerateGrids: a 1-wide or 1-tall grid must sample the
// single column/row without indexing out of bounds — the degenerate
// edge of bilinear.
func TestHeightAtDegenerateGrids(t *testing.T) {
	col := &Heightfield{Width: 1, Height: 3, Samples: []float32{1, 2, 3}, OriginX: 0, OriginZ: 0, CellSize: 1, YScale: 1}
	if got := col.HeightAt(99, 1.5); !almostHeight(got, 2.5) {
		t.Errorf("1-wide grid HeightAt(99, 1.5) = %v, want 2.5 (column interpolation)", got)
	}
	row := &Heightfield{Width: 3, Height: 1, Samples: []float32{1, 2, 3}, OriginX: 0, OriginZ: 0, CellSize: 1, YScale: 1}
	if got := row.HeightAt(1.5, -99); !almostHeight(got, 2.5) {
		t.Errorf("1-tall grid HeightAt(1.5, -99) = %v, want 2.5 (row interpolation)", got)
	}
	// A single sample is exact everywhere (clamped to it).
	one := &Heightfield{Width: 1, Height: 1, Samples: []float32{7}, OriginX: 0, OriginZ: 0, CellSize: 1, YScale: 1}
	if got := one.HeightAt(-5, 300); got != 7 {
		t.Errorf("1x1 grid HeightAt(-5, 300) = %v, want 7", got)
	}
}

// TestHeightAtNonUnitCellSizeAndYScale: world Y = bilinear sample x
// y_scale, and cell_size converts world meters to sample indices.
func TestHeightAtNonUnitCellSizeAndYScale(t *testing.T) {
	h := &Heightfield{
		Width:    2,
		Height:   2,
		Samples:  []float32{10, 20, 30, 40},
		OriginX:  100,
		OriginZ:  -50,
		CellSize: 2,
		YScale:   3,
	}
	// (102, -48) is sample (1, 1) = 40 -> world height 120.
	if got := h.HeightAt(102, -48); got != 120 {
		t.Errorf("HeightAt(102, -48) = %v, want 120 (sample 40 x y_scale 3)", got)
	}
	// Mid-cell blend: (101, -49) = 0.5/0.5 -> 25 x 3 = 75.
	if got := h.HeightAt(101, -49); !almostHeight(got, 75) {
		t.Errorf("HeightAt(101, -49) = %v, want 75", got)
	}
}

// TestHeightAtNeverErrors pins the "never errors" contract: a nil or
// zero-value Heightfield must return 0 without panicking.
func TestHeightAtNeverErrors(t *testing.T) {
	var nilField *Heightfield
	if got := nilField.HeightAt(1, 2); got != 0 {
		t.Errorf("nil Heightfield HeightAt = %v, want 0", got)
	}
	var zero Heightfield
	if got := zero.HeightAt(1, 2); got != 0 {
		t.Errorf("zero-value Heightfield HeightAt = %v, want 0", got)
	}
	empty := &Heightfield{Width: 4, Height: 4} // no samples
	if got := empty.HeightAt(1, 2); got != 0 {
		t.Errorf("Heightfield without samples HeightAt = %v, want 0", got)
	}
}

// TestHeightAtDeterminism pins WTM-6 at the sampler level: identical
// fields produce identical heights — repeated sampling and a freshly
// built twin yield the same f32 values.
func TestHeightAtDeterminism(t *testing.T) {
	a := rampField()
	b := rampField()
	points := [][2]float32{{0.123, 4.876}, {3.5, 4.25}, {-9, 7}, {5, 5}}
	for _, p := range points {
		first := a.HeightAt(p[0], p[1])
		if got := a.HeightAt(p[0], p[1]); got != first {
			t.Errorf("HeightAt(%v, %v) not stable across calls: %v then %v", p[0], p[1], first, got)
		}
		if got := b.HeightAt(p[0], p[1]); got != first {
			t.Errorf("twin field HeightAt(%v, %v) = %v, want %v", p[0], p[1], got, first)
		}
	}
}
