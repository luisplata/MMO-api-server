package server

// defaultSpawnResolver is the server's built-in session.SpawnResolver
// (design D4: Select resolves the character's spawn + stats + templateId).
// It resolves every selected character to the configured spawn point, with
// the terrain height in Y (spec CTH-3). Tests inject a custom resolver via
// Config.Spawn; the real server uses this one when none is supplied.

import (
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// defaultSpawnResolver resolves a selected character's spawn to the fixed
// server spawn point. It is decoupled from the character store — the
// character's own spawn point is resolved by the game layer (PR5).
type defaultSpawnResolver struct {
	spawnX  float32
	spawnZ  float32
	heights world.HeightResolver
}

// SpawnFor implements session.SpawnResolver: it returns the configured
// spawn point with the terrain height in Y (flat 0 when no resolver).
func (d *defaultSpawnResolver) SpawnFor(characterID string) *mmov1.Vec3 {
	y := float32(0)
	if d.heights != nil {
		y = d.heights.HeightAt(d.spawnX, d.spawnZ)
	}
	return &mmov1.Vec3{X: d.spawnX, Y: y, Z: d.spawnZ}
}
