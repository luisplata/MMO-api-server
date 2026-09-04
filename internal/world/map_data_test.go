package world

// Embedded default map tests (design D7, spec WTM-4 default / WTM-6).
//
// The committed reference fixture internal/world/testdata/hills.* is a
// canonical 32x32 grid (cell 10, origin (0,0), y_scale 1) with a smooth
// hill centered on the spawn point (100, 200) = sample (10, 20). These
// tests pin that the embedded default parses to the documented grid,
// that the spawn point sits on the hill, and that loading is
// deterministic (two loads → identical fields and identical samples).

import (
	"testing"
)

// TestDefaultHeightfieldLoads pins WTM-4 default: the embedded fixture
// parses through the canonical loader into the documented grid.
func TestDefaultHeightfieldLoads(t *testing.T) {
	h, err := DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	if h.Width != 32 || h.Height != 32 {
		t.Errorf("dims = %dx%d, want 32x32", h.Width, h.Height)
	}
	if h.CellSize != 10 || h.OriginX != 0 || h.OriginZ != 0 || h.YScale != 1 {
		t.Errorf("metadata = cell %v origin (%v,%v) yscale %v, want 10 (0,0) 1", h.CellSize, h.OriginX, h.OriginZ, h.YScale)
	}
	if len(h.Samples) != 32*32 {
		t.Errorf("sample count = %d, want %d", len(h.Samples), 32*32)
	}
	// Bounds: origin + (dims-1)*cell = 0 + 31*10 = 310.
	if got := h.HeightAt(310, 310); got != 0 {
		t.Errorf("HeightAt(310, 310) = %v, want 0 (corner far from the hill)", got)
	}
}

// TestHillsSpawnOnTheHill pins task 1.8: the spawn point (100, 200) is
// the hill peak — sample (10, 20) — so the character spawns on the
// terrain bump the E2E hill gate (Slice B) will assert on.
func TestHillsSpawnOnTheHill(t *testing.T) {
	h, err := DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	if got := h.HeightAt(100, 200); got != 25 {
		t.Errorf("HeightAt(100, 200) = %v, want 25 (hill peak at the spawn sample)", got)
	}
	// The hill decays smoothly: a quarter radius out it is lower than
	// the peak but still clearly above the flat base.
	if got := h.HeightAt(100+20, 200); got >= 25 || got <= 0 {
		t.Errorf("HeightAt(120, 200) = %v, want 0 < h < 25 (hill flank)", got)
	}
	// Flat base far from the hill is exactly 0.
	if got := h.HeightAt(0, 0); got != 0 {
		t.Errorf("HeightAt(0, 0) = %v, want 0 (flat base)", got)
	}
}

// TestDefaultHeightfieldDeterminism pins WTM-6 on the embedded fixture:
// two independent loads produce identical fields and identical sampling
// — the two-sim determinism guarantee Slice B builds on.
func TestDefaultHeightfieldDeterminism(t *testing.T) {
	a, err := DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	b, err := DefaultHeightfield()
	if err != nil {
		t.Fatalf("second DefaultHeightfield: %v", err)
	}
	if len(a.Samples) != len(b.Samples) {
		t.Fatalf("sample count differs across loads: %d vs %d", len(a.Samples), len(b.Samples))
	}
	for i := range a.Samples {
		if a.Samples[i] != b.Samples[i] {
			t.Fatalf("sample[%d] differs across loads: %v vs %v", i, a.Samples[i], b.Samples[i])
		}
	}
	points := [][2]float32{{100, 200}, {110, 205}, {50, 60}, {310, 310}, {7.5, 300.25}}
	for _, p := range points {
		if got := b.HeightAt(p[0], p[1]); got != a.HeightAt(p[0], p[1]) {
			t.Errorf("HeightAt(%v, %v) differs across loads: %v vs %v", p[0], p[1], a.HeightAt(p[0], p[1]), got)
		}
	}
}
