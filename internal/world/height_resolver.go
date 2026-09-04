package world

// Terrain height seam (design D4, spec CTH-1/CTH-2/CTH-3).
//
// HeightResolver is the terrain analogue of InterestResolver: the
// simulation and the authenticator consume height through this narrow
// interface instead of the concrete *Heightfield, so the 20 Hz tick and
// the spawn resolution stay decoupled from the map representation. A
// nil resolver means flat terrain (Y = 0) — a deployment with no map
// still simulates on level ground. A future terrain implementation
// (chunked grids, streaming) swaps in behind the same seam with zero
// churn in game/server.

// HeightResolver returns the world height at a ground-plane point.
// Contract: implementations never error and never panic — out-of-bounds
// positions clamp to the nearest edge sample (spec WTM-3), and the
// result is deterministic for identical inputs (WTM-6).
type HeightResolver interface {
	// HeightAt returns the world height at (x, z).
	HeightAt(x, z float32) float32
}
