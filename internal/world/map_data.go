package world

// Embedded default map (design D7, spec WTM-4 default).
//
// The server ships with a committed reference fixture — the canonical
// "hills" heightmap — embedded via go:embed, so a deployment with no
// -map flag still simulates on real terrain (the hills E2E gate depends
// on it). The fixture is produced by the Unity-side exporter contract
// (docs/HEIGHTMAP.md); this package only parses and samples it. The
// default parses through the SAME loader core as a -map override, so a
// broken embedded artifact fails at boot exactly like a broken file.

import (
	_ "embed"
)

//go:embed testdata/hills.heightmap
var embeddedHillsHeightmap []byte

//go:embed testdata/hills.manifest
var embeddedHillsManifest []byte

// DefaultHeightfield parses the embedded hills fixture through the
// canonical loader and returns the active heightfield for a server that
// has no -map override (spec WTM-4 default). A corrupt embedded
// artifact is a wrapped %w error — the server must refuse to start.
func DefaultHeightfield() (*Heightfield, error) {
	return parseHeightmap(embeddedHillsHeightmap, embeddedHillsManifest)
}
