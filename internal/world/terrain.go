package world

// Server-owned heightfield (design D1, spec WTM-1/WTM-2/WTM-3/WTM-6).
//
// The heightfield is a pure f32 grid: an array of width x height samples
// over a regular grid, plus the metadata that places the grid in world
// space (origin, cell_size, y_scale). Sampling is deterministic bilinear
// interpolation with OOB clamping — HeightAt never errors and never
// panics (WTM-3), which keeps the 20 Hz tick path allocation-free.

// Heightfield is a 2D height grid in world space (design D1). Sample
// (i, j) sits at world (OriginX + i*CellSize, OriginZ + j*CellSize) and
// its world height is Samples[j*Width+i] * YScale. Rows run from min Z
// (j = 0) to max Z; columns from min X (i = 0) to max X.
type Heightfield struct {
	Width, Height uint32
	// Samples is width x height row-major f32 values. The loader
	// guarantees they are finite and |sample * YScale| <= 1e4.
	Samples []float32
	// OriginX, OriginZ place sample (0,0) in world space.
	OriginX, OriginZ float32
	// CellSize is the world meters per sample step (> 0); YScale
	// converts a raw sample into world height (world Y = sample *
	// YScale, > 0).
	CellSize, YScale float32
}

// HeightAt returns the world height at (x, z) via bilinear interpolation
// of the four surrounding samples (spec WTM-2): exact at sample points,
// blended between them, and clamped to the nearest edge/corner sample
// when (x, z) falls outside the grid (WTM-3). A nil or degenerate
// Heightfield returns 0 — it never errors. The math is a fixed sequence
// of f32 operations with no iteration or RNG, so identical fields yield
// identical heights (WTM-6).
func (h *Heightfield) HeightAt(x, z float32) float32 {
	if h == nil || h.Width == 0 || h.Height == 0 || len(h.Samples) == 0 {
		return 0
	}
	// Clamp to the grid's world bounds, then convert to fractional
	// sample coordinates.
	maxX := h.OriginX + float32(h.Width-1)*h.CellSize
	maxZ := h.OriginZ + float32(h.Height-1)*h.CellSize
	cx := clampf(x, h.OriginX, maxX)
	cz := clampf(z, h.OriginZ, maxZ)
	fx := (cx - h.OriginX) / h.CellSize
	fz := (cz - h.OriginZ) / h.CellSize

	i0 := int(fx)
	j0 := int(fz)
	if i0 > int(h.Width)-1 {
		i0 = int(h.Width) - 1
	}
	if j0 > int(h.Height)-1 {
		j0 = int(h.Height) - 1
	}
	i1 := i0 + 1
	if i1 > int(h.Width)-1 {
		i1 = int(h.Width) - 1
	}
	j1 := j0 + 1
	if j1 > int(h.Height)-1 {
		j1 = int(h.Height) - 1
	}

	// Bilinear blend in sample space; the world scale applies once at
	// the end (world Y = sample * YScale).
	tx := fx - float32(i0)
	tz := fz - float32(j0)
	w := int(h.Width)
	h00 := h.Samples[j0*w+i0]
	h10 := h.Samples[j0*w+i1]
	h01 := h.Samples[j1*w+i0]
	h11 := h.Samples[j1*w+i1]
	top := h00*(1-tx) + h10*tx
	bottom := h01*(1-tx) + h11*tx
	return (top*(1-tz) + bottom*tz) * h.YScale
}

// clampf bounds v to [lo, hi].
func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
