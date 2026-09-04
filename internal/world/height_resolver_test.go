package world

// HeightResolver seam tests (design D4, spec CTH-2): the tick and the
// authenticator consume terrain height through the HeightResolver
// interface, never the concrete *Heightfield — the terrain analogue of
// InterestResolver. These tests pin the seam statically (the concrete
// type satisfies the interface) and behaviorally (interface dispatch
// returns the exact same heights as the concrete method, so wiring
// through the seam changes nothing).

import "testing"

// TestHeightfieldImplementsHeightResolver pins the seam: *Heightfield
// satisfies HeightResolver, and calling HeightAt through the interface
// returns the identical f32 value the concrete method returns — the
// simulation and auth can rely on the seam without knowing the concrete
// type.
func TestHeightfieldImplementsHeightResolver(t *testing.T) {
	var _ HeightResolver = (*Heightfield)(nil)

	h, err := DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	var r HeightResolver = h
	points := [][2]float32{{100, 200}, {110.5, 205.25}, {0, 0}, {310, 310}, {7.5, 300.25}}
	for _, p := range points {
		want := h.HeightAt(p[0], p[1])
		if got := r.HeightAt(p[0], p[1]); got != want {
			t.Errorf("interface HeightAt(%v, %v) = %v, want %v (concrete)", p[0], p[1], got, want)
		}
	}
}
