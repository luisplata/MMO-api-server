// Package world implements the chunk interest management (spec R18)
// behind the InterestResolver seam (design D4).
//
// The world is divided into a grid of fixed-size cells (64 units,
// view radius 2 cells = a 5x5 visible chunk set). All cell math is
// pure — no I/O, no state — so the grid rules are trivially testable
// and the chunk-based fanout can later be swapped in without touching
// the wire contract (spec S19.2).
package world

import (
	"math"
)

// CellSize is the edge length of one square cell in world units
// (spec R18 — tunable constant).
const CellSize = 64

// ViewRadius is how many cells a player can see around their own cell
// (spec R18). The visible set is (2*ViewRadius+1)^2 = 5x5 cells.
const ViewRadius = 2

// Cell identifies one square cell of the chunk grid. Two int32s are
// comparable, so a Cell is usable directly as a map key.
type Cell struct {
	X int32
	Z int32
}

// CellAt returns the cell containing the given world coordinates.
// Division floors toward negative infinity so coordinates just below
// zero land in cell -1 (never cell 0 by truncation).
func CellAt(x, z float32) Cell {
	return Cell{X: floorDiv(x), Z: floorDiv(z)}
}

// floorDiv divides v by CellSize and floors toward negative infinity.
func floorDiv(v float32) int32 {
	return int32(math.Floor(float64(v) / CellSize))
}

// Contains reports whether the coordinates fall inside the cell.
func (c Cell) Contains(x, z float32) bool {
	return CellAt(x, z) == c
}

// View returns the player's visible chunk set: every cell within
// ViewRadius cells (Chebyshev distance) — a 5x5 square (spec R18).
func (c Cell) View() []Cell {
	cells := make([]Cell, 0, (2*ViewRadius+1)*(2*ViewRadius+1))
	for x := c.X - ViewRadius; x <= c.X+ViewRadius; x++ {
		for z := c.Z - ViewRadius; z <= c.Z+ViewRadius; z++ {
			cells = append(cells, Cell{X: x, Z: z})
		}
	}
	return cells
}

// CellChange returns the cells a player enters and leaves when their
// cell moves from prev to next — the raw material for enter/leave
// events (spec S18.2). A no-op move returns two empty sets.
func CellChange(prev, next Cell) (entered, left []Cell) {
	if prev == next {
		return nil, nil
	}
	prevView := prev.View()
	nextView := next.View()

	nextSet := make(map[Cell]bool, len(nextView))
	for _, c := range nextView {
		nextSet[c] = true
	}
	prevSet := make(map[Cell]bool, len(prevView))
	for _, c := range prevView {
		prevSet[c] = true
		if !nextSet[c] {
			left = append(left, c)
		}
	}
	for _, c := range nextView {
		if !prevSet[c] {
			entered = append(entered, c)
		}
	}
	return entered, left
}
