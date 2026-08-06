package game

// Snapshot tests (spec R16/R17, S16.1/S17.1/S17.2/S17.3).
//
// Coverage: broadcast cadence (10 Hz = every other 20 Hz tick) with
// per-player stagger, the client-side sequence discipline (duplicates
// and seq <= last-applied dropped), and the full-state snapshot
// assembler (x/z/velocity/yaw present, no pitch/roll — design D3).

import (
	"testing"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestShouldSnapshot(t *testing.T) {
	tests := []struct {
		name  string
		tick  uint64
		index int
		want  bool
	}{
		{"p0 even ticks", 0, 0, true},
		{"p0 odd ticks skip", 1, 0, false},
		{"p0 tick 2", 2, 0, true},
		{"p1 starts one tick later", 0, 1, false},
		{"p1 odd ticks", 1, 1, true},
		{"p1 tick 2", 2, 1, false},
		{"p2 same phase as p0", 2, 2, true},
		{"p3 same phase as p1", 3, 3, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldSnapshot(tc.tick, tc.index); got != tc.want {
				t.Errorf("ShouldSnapshot(tick=%d, idx=%d) = %v, want %v",
					tc.tick, tc.index, got, tc.want)
			}
		})
	}
}

// TestShouldSnapshotTenHertzPerPlayer proves each player's stream fires
// 10 times per simulated second (20 ticks) — exactly half the tick rate.
func TestShouldSnapshotTenHertzPerPlayer(t *testing.T) {
	for idx := 0; idx < 4; idx++ {
		count := 0
		for tick := uint64(0); tick < 20; tick++ {
			if ShouldSnapshot(tick, idx) {
				count++
			}
		}
		if count != 10 {
			t.Errorf("player %d broadcasts %d times per 20 ticks, want 10 (10 Hz)", idx, count)
		}
	}
}

func TestSeqFilter(t *testing.T) {
	f := &SeqFilter{}
	tests := []struct {
		name string
		seq  uint32
		want bool
	}{
		{"first accepted", 7, true},
		{"duplicate rejected", 7, false},
		{"stale rejected", 5, false},
		{"out-of-order older rejected", 6, false},
		{"newer accepted", 9, true},
		{"equal to last rejected", 9, false},
		{"bigger jump accepted", 100, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Accept(tc.seq); got != tc.want {
				t.Errorf("SeqFilter.Accept(%d) = %v, want %v (last=%d)", tc.seq, got, tc.want, f.last)
			}
		})
	}
}

// TestSeqFilterWrapIsDropped documents the seq watermark boundary: the
// wire seq is int32 (max ~2.1e9; at 10 Hz server-wide that is years of
// continuous emission, and a process restart resets the counter). A seq
// that wrapped around is treated as stale and dropped — dropping is the
// safe failure mode (never wrongly applied).
func TestSeqFilterWrapIsDropped(t *testing.T) {
	f := &SeqFilter{}
	if !f.Accept(^uint32(0)) {
		t.Fatal("max uint32 seq must be accepted")
	}
	if f.Accept(0) {
		t.Error("wrapped seq 0 after max must be dropped as stale, not applied")
	}
}

func TestFullStateAssembler(t *testing.T) {
	asm := FullStateAssembler{}
	entities := []*Entity{
		{ID: "p1", Pos: Vec2{1.5, -2.5}, Velocity: Vec2{3, 0}, Yaw: 0.5},
		{ID: "p2", Pos: Vec2{-8, 4}, Velocity: Vec2{0, 0}, Yaw: 3.14},
	}
	snap := asm.Assemble(42, entities)
	if snap.Seq != 42 {
		t.Errorf("snapshot seq = %d, want 42", snap.Seq)
	}
	if len(snap.Entities) != 2 {
		t.Fatalf("snapshot entities = %d, want 2 (full state)", len(snap.Entities))
	}
	// Order preserved (stable broadcast order).
	if snap.Entities[0].Id != "p1" || snap.Entities[1].Id != "p2" {
		t.Errorf("entity order = [%s %s], want [p1 p2]", snap.Entities[0].Id, snap.Entities[1].Id)
	}
	// S16.1: x, z, velocity and yaw must all be present (no pitch/roll).
	es := snap.Entities[0]
	if es.Pos == nil || !almostEqual(es.Pos.X, 1.5) || !almostEqual(es.Pos.Z, -2.5) {
		t.Errorf("p1 pos = %v, want (1.5, -2.5)", es.Pos)
	}
	if es.Velocity == nil || !almostEqual(es.Velocity.X, 3) || !almostEqual(es.Velocity.Z, 0) {
		t.Errorf("p1 velocity = %v, want (3, 0)", es.Velocity)
	}
	if !almostEqual(es.Yaw, 0.5) {
		t.Errorf("p1 yaw = %v, want 0.5", es.Yaw)
	}
	// A player with zero velocity still carries an explicit velocity.
	if snap.Entities[1].Velocity == nil || snap.Entities[1].Velocity.X != 0 {
		t.Errorf("p2 velocity = %v, want explicit (0, 0)", snap.Entities[1].Velocity)
	}
}

// TestFullStateAssemblerEmpty proves an empty world yields an empty
// (but valid, seq-carrying) snapshot — the real full-state path ran.
func TestFullStateAssemblerEmpty(t *testing.T) {
	snap := (FullStateAssembler{}).Assemble(1, nil)
	if snap == nil {
		t.Fatal("Assemble(nil) returned nil snapshot")
	}
	if snap.Seq != 1 {
		t.Errorf("empty snapshot seq = %d, want 1", snap.Seq)
	}
	if snap.Entities == nil || len(snap.Entities) != 0 {
		t.Errorf("empty snapshot entities = %v, want empty non-nil slice", snap.Entities)
	}
}

// TestSnapshotMessageHasNoPitchRoll proves the wire shape carries only
// the ground-plane state (design D3): the EntityState descriptor has no
// pitch/roll fields. This is the S16.1 structural guarantee.
func TestSnapshotMessageHasNoPitchRoll(t *testing.T) {
	es := &mmov1.EntityState{}
	fds := es.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fds.Len(); i++ {
		name := string(fds.Get(i).Name())
		if name == "pitch" || name == "roll" || name == "y" {
			t.Errorf("EntityState must not carry %q (camera is client-local, D3)", name)
		}
	}
}
