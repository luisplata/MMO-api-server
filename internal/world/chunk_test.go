package world

// Chunk grid math tests (spec R18, S18.1).
//
// Coverage: cell computation (floor semantics for negatives, exact
// boundary multiples), the 5x5 view set for any cell, cell membership,
// and enter/leave cell sets on a crossing (S18.2 event math).

import (
	"testing"
)

func TestCellAt(t *testing.T) {
	tests := []struct {
		name string
		x, z float32
		want Cell
	}{
		{"origin", 0, 0, Cell{0, 0}},
		{"positive interior", 63.9, 63.9, Cell{0, 0}},
		{"positive boundary exactly", 64, 0, Cell{1, 0}},
		{"exact multiples", 128, -64, Cell{2, -1}},
		{"negative just below origin", -0.1, -0.1, Cell{-1, -1}},
		{"negative boundary exactly", -64, -64, Cell{-1, -1}},
		{"negative interior", -1, -1, Cell{-1, -1}},
		{"far negative floors down", -129, 63, Cell{-3, 0}},
		{"large values", 5000, -3000, Cell{78, -47}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CellAt(tc.x, tc.z); got != tc.want {
				t.Errorf("CellAt(%v, %v) = %v, want %v", tc.x, tc.z, got, tc.want)
			}
		})
	}
}

// TestCellAtUsesCellSize proves the grid size is the CellSize constant:
// a coordinate just below 64 must still land in cell 0.
func TestCellAtUsesCellSize(t *testing.T) {
	if CellSize != 64 {
		t.Fatalf("CellSize = %d, want 64 (grid definition, spec R18)", CellSize)
	}
	if got := CellAt(CellSize-0.01, 0); got != (Cell{0, 0}) {
		t.Errorf("CellAt(63.99, 0) = %v, want cell 0 (64-unit cells)", got)
	}
	if got := CellAt(float32(CellSize), 0); got != (Cell{1, 0}) {
		t.Errorf("CellAt(64, 0) = %v, want cell 1", got)
	}
}

func TestCellView(t *testing.T) {
	// View of the origin: cells with X and Z in [-2, 2] — 25 cells
	// (view radius 2 -> 5x5, spec R18).
	got := (Cell{0, 0}).View()
	if len(got) != 25 {
		t.Fatalf("View of (0,0) = %d cells, want 25 (5x5)", len(got))
	}
	seen := make(map[Cell]bool, len(got))
	for _, c := range got {
		if c.X < -2 || c.X > 2 || c.Z < -2 || c.Z > 2 {
			t.Errorf("View of (0,0) contains out-of-range cell %v", c)
		}
		if seen[c] {
			t.Errorf("View of (0,0) contains duplicate cell %v", c)
		}
		seen[c] = true
	}
	// Every cell in the range must be present (completeness).
	for x := int32(-2); x <= 2; x++ {
		for z := int32(-2); z <= 2; z++ {
			if !seen[Cell{x, z}] {
				t.Errorf("View of (0,0) missing cell (%d,%d)", x, z)
			}
		}
	}
}

// TestCellViewOffOrigin proves the view set is relative to the cell, not
// hardcoded around the origin.
func TestCellViewOffOrigin(t *testing.T) {
	got := (Cell{3, -4}).View()
	if len(got) != 25 {
		t.Fatalf("View of (3,-4) = %d cells, want 25", len(got))
	}
	for _, c := range got {
		if c.X < 1 || c.X > 5 || c.Z < -6 || c.Z > -2 {
			t.Errorf("View of (3,-4) contains out-of-range cell %v", c)
		}
	}
}

func TestCellViewRadius(t *testing.T) {
	if ViewRadius != 2 {
		t.Fatalf("ViewRadius = %d, want 2 (spec R18)", ViewRadius)
	}
	// View size must be (2*ViewRadius+1)^2.
	want := (2*ViewRadius + 1) * (2*ViewRadius + 1)
	if got := len((Cell{0, 0}).View()); got != want {
		t.Errorf("View size = %d, want %d", got, want)
	}
}

func TestCellContains(t *testing.T) {
	tests := []struct {
		name string
		cell Cell
		x, z float32
		want bool
	}{
		{"origin origin", Cell{0, 0}, 0, 0, true},
		{"interior high edge", Cell{0, 0}, 63.9, 63.9, true},
		{"interior negative edge", Cell{0, 0}, -0.01, -0.01, false},
		{"boundary moves to next cell", Cell{0, 0}, 64, 0, false},
		{"cell 1 contains 64", Cell{1, 0}, 64, 0, true},
		{"cell -1 contains -64", Cell{-1, 0}, -64, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cell.Contains(tc.x, tc.z); got != tc.want {
				t.Errorf("%v.Contains(%v, %v) = %v, want %v", tc.cell, tc.x, tc.z, got, tc.want)
			}
		})
	}
}

func TestCellChange(t *testing.T) {
	// Moving east from cell (0,0) to (1,0): the view slides by one
	// column. Entered = the new east column (X=3, 5 cells); left = the
	// old west column (X=-2, 5 cells).
	entered, left := CellChange(Cell{0, 0}, Cell{1, 0})
	if len(entered) != 5 {
		t.Fatalf("CellChange east: entered = %d cells, want 5", len(entered))
	}
	for _, c := range entered {
		if c.X != 3 {
			t.Errorf("CellChange east entered %v, want X=3 column", c)
		}
	}
	if len(left) != 5 {
		t.Fatalf("CellChange east: left = %d cells, want 5", len(left))
	}
	for _, c := range left {
		if c.X != -2 {
			t.Errorf("CellChange east left %v, want X=-2 column", c)
		}
	}
}

func TestCellChangeDiagonal(t *testing.T) {
	// Moving north-east from (0,0) to (1,1): entered = east column (X=3)
	// plus north row (Z=3) minus the corner (3,3) counted twice = 9;
	// left = west column (X=-2) plus south row (Z=-2) minus (2,2)? No —
	// the corner of the OLD view is (-2,-2). Verify by set algebra.
	entered, left := CellChange(Cell{0, 0}, Cell{1, 1})
	enteredSet := make(map[Cell]bool, len(entered))
	for _, c := range entered {
		enteredSet[c] = true
	}
	leftSet := make(map[Cell]bool, len(left))
	for _, c := range left {
		leftSet[c] = true
	}
	// Entered = View(1,1) \ View(0,0): cells with X=3 (any Z) or Z=3
	// (any X). Left = View(0,0) \ View(1,1): cells with X=-2 or Z=-2.
	if len(entered) != 9 {
		t.Errorf("diagonal entered = %d cells, want 9", len(entered))
	}
	if len(left) != 9 {
		t.Errorf("diagonal left = %d cells, want 9", len(left))
	}
	for x := int32(1); x <= 3; x++ {
		for z := int32(1); z <= 3; z++ {
			if x == 3 || z == 3 {
				if !enteredSet[Cell{x, z}] {
					t.Errorf("entered missing (%d,%d)", x, z)
				}
			}
		}
	}
	for x := int32(-2); x <= 0; x++ {
		for z := int32(-2); z <= 0; z++ {
			if x == -2 || z == -2 {
				if !leftSet[Cell{x, z}] {
					t.Errorf("left missing (%d,%d)", x, z)
				}
			}
		}
	}
}

// TestCellChangeSameCell proves a no-op move yields no enter/leave sets.
func TestCellChangeSameCell(t *testing.T) {
	entered, left := CellChange(Cell{0, 0}, Cell{0, 0})
	if len(entered) != 0 || len(left) != 0 {
		t.Errorf("CellChange(same) = entered %d, left %d; want both empty",
			len(entered), len(left))
	}
}
