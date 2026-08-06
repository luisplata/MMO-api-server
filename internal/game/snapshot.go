package game

// Snapshot broadcast (spec R16/R17, design D3).
//
// Per-player snapshots go out at 10 Hz (every other 20 Hz tick),
// staggered so two players never broadcast on the same tick (S17.1).
// The server stamps each emission with a globally monotonic seq; the
// client-side discipline (S17.2/S17.3) — drop duplicates and anything
// with seq <= the last applied — is modelled by SeqFilter. The
// SnapshotAssembler seam fills the wire container with full state in
// v1; chunk/delta variants swap in later with zero protocol churn
// (S19.2).

import (
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// ShouldSnapshot reports whether the player at broadcast index should
// receive a snapshot on the given tick (spec S17.1): every other 20 Hz
// tick, offset per player so streams are staggered (~10 Hz each).
func ShouldSnapshot(tick uint64, playerIndex int) bool {
	return (tick+uint64(playerIndex))%2 == 0
}

// SeqFilter models the client-side snapshot sequence discipline
// (spec S17.2/S17.3): a snapshot is applied only when its seq is
// strictly greater than the last applied. Duplicates and stale or
// out-of-order snapshots are dropped. It is also the server's guide
// for what a healthy client keeps.
type SeqFilter struct {
	last uint32
	has  bool
}

// Accept reports whether the snapshot with the given seq should be
// applied, updating the last-applied watermark on acceptance.
func (f *SeqFilter) Accept(seq uint32) bool {
	if f.has && seq <= f.last {
		return false
	}
	f.last = seq
	f.has = true
	return true
}

// SnapshotAssembler is the seam that fills the wire Snapshot container
// (design D3, spec S19.2). v1: FullStateAssembler (every entity, no
// deltas). Future: chunk-scoped or delta-encoded assemblies — the
// .proto message never changes.
type SnapshotAssembler interface {
	// Assemble builds the snapshot for the given server seq from the
	// current entity set.
	Assemble(seq uint32, entities []*Entity) *mmov1.Snapshot
}

// FullStateAssembler is the v1 snapshot assembler: the full entity set
// in stable order, with position (x, z), velocity and yaw — and nothing
// else (design D3, spec S16.1).
type FullStateAssembler struct{}

// Assemble maps entities to their wire state. Entities are copied into
// fresh messages so the sim never shares mutable state with the wire.
func (FullStateAssembler) Assemble(seq uint32, entities []*Entity) *mmov1.Snapshot {
	snap := &mmov1.Snapshot{Seq: int32(seq), Entities: make([]*mmov1.EntityState, 0, len(entities))}
	for _, e := range entities {
		snap.Entities = append(snap.Entities, &mmov1.EntityState{
			Id:       e.ID,
			Pos:      &mmov1.Vec2{X: e.Pos.X, Z: e.Pos.Z},
			Velocity: &mmov1.Vec2{X: e.Velocity.X, Z: e.Velocity.Z},
			Yaw:      e.Yaw,
		})
	}
	return snap
}
