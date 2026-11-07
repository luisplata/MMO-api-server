package e2e

// P16 capacity/isolation gate over WSS: a two-player room is visible to its
// pair, a third client forced into that full room BY ID is rejected without
// consuming a slot, and the existing pair keeps playing. A separate pair in
// another room proves cross-room isolation end-to-end: movement in one room
// never appears in the other's fresh snapshots.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestWSSRoomCapacityRejectsThirdWithoutConsumingSlot(t *testing.T) {
	wssURL, shutdown := startServer(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	reg := protocol.NewWorldRegistry()

	// The pair enters through the public allocator, which fills the legacy
	// default room; the third client then targets that now-known room by ID.
	a := newWSClient(t, reg, front, proxyWSOrigin, "alice")
	spawnA, charIDA, jrA, _ := a.enterWorldIn(t, "alice", "warrior", "hero", protocol.JoinRoomNextAvailable, "")
	if jrA.GetRoomId() != "default" || jrA.GetCapacity() != 2 || jrA.GetOccupancy() != 1 {
		t.Fatalf("alice join = %+v, want default occ=1 cap=2", jrA)
	}
	b := newWSClient(t, reg, front, proxyWSOrigin, "bob")
	_, charIDB, jrB, wsB := b.enterWorldIn(t, "bob", "mage", "hero", protocol.JoinRoomNextAvailable, "")
	if jrB.GetRoomId() != "default" || jrB.GetCapacity() != 2 || jrB.GetOccupancy() != 2 {
		t.Fatalf("bob join = %+v, want default occ=2 cap=2", jrB)
	}
	// The room is visible to its pair: bob's first authoritative view carries
	// both players, and alice is told bob spawned.
	if findEntity(wsB.Entities, charIDA) == nil || findEntity(wsB.Entities, charIDB) == nil {
		t.Fatalf("bob snapshot = %v, want both players", entityIDs(wsB.Entities))
	}
	requireWS[*mmov1.SpawnEntity](t, a, "SpawnEntity bob", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == charIDB
	})

	// Third client, forced to join the full room BY ID: the lobby rejects it
	// with the zero response and leaves it in selecting.
	c := newWSClient(t, reg, front, proxyWSOrigin, "carol")
	c.connectAndAuth(t, "carol")
	if cc := c.createCharacter(t, "warrior", "hero"); !cc.GetOk() {
		t.Fatalf("carol create = %+v", cc)
	}
	if jr := c.joinRoom(t, protocol.JoinRoomByID, "default"); jr.GetRoomId() != "" || jr.GetCapacity() != 0 || jr.GetOccupancy() != 0 {
		t.Fatalf("third join to full room = %+v, want zero rejection", jr)
	}

	// The pair remains connected and unaffected: both movement directions
	// still fan out after the rejected join.
	a.send(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: moveSpeed, Yaw: moveYaw})
	requireWS[*mmov1.Snapshot](t, b, "snapshot showing alice moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDA)
		return e != nil && e.Pos != nil && e.Pos.X > spawnA.X+0.1
	})
	b.send(t, &mmov1.MoveInput{Seq: 2, Dir: &mmov1.Vec2{X: 0, Z: 1}, Speed: moveSpeed, Yaw: 0.5})
	requireWS[*mmov1.Snapshot](t, a, "snapshot showing bob moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDB)
		return e != nil && e.Pos != nil && e.Pos.Z > testSpawnZ+0.1 && e.Yaw == 0.5
	})

	// The rejected attempt consumed no slot: after one real member leaves,
	// the SAME client joins the room. A leaked reservation would still hold
	// capacity and reject it again.
	a.disconnect()
	requireWS[*mmov1.DespawnEntity](t, b, "despawn of alice", func(dp *mmov1.DespawnEntity) bool {
		return dp.EntityId == charIDA
	})
	if jr := c.joinRoom(t, protocol.JoinRoomByID, "default"); jr.GetRoomId() != "default" || jr.GetCapacity() != 2 || jr.GetOccupancy() != 2 {
		t.Fatalf("carol retry after a slot freed = %+v, want default occ=2 cap=2", jr)
	}
}

func TestWSSRoomsIsolateMovementEndToEnd(t *testing.T) {
	wssURL, shutdown := startServer(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	reg := protocol.NewWorldRegistry()

	// Default room: alice + bob via NextAvailable.
	a := newWSClient(t, reg, front, proxyWSOrigin, "alice")
	_, charIDA, _, _ := a.enterWorldIn(t, "alice", "warrior", "hero", protocol.JoinRoomNextAvailable, "")
	b := newWSClient(t, reg, front, proxyWSOrigin, "bob")
	_, charIDB, _, _ := b.enterWorldIn(t, "bob", "mage", "hero", protocol.JoinRoomNextAvailable, "")

	// Second room: with default full, the only public way in is the
	// NextAvailable create-on-miss path. Carol creates the room; dave reuses
	// it. No ByID call may mint a room.
	c := newWSClient(t, reg, front, proxyWSOrigin, "carol")
	spawnC, charIDC, jrC, wsC := c.enterWorldIn(t, "carol", "warrior", "hero", protocol.JoinRoomNextAvailable, "")
	secondRoom := jrC.GetRoomId()
	if secondRoom == "" || secondRoom == "default" || jrC.GetOccupancy() != 1 {
		t.Fatalf("carol join = %+v, want a new non-default room occ=1", jrC)
	}
	if len(wsC.Entities) != 1 || findEntity(wsC.Entities, charIDC) == nil {
		t.Fatalf("carol snapshot = %v, want only carol", entityIDs(wsC.Entities))
	}
	d := newWSClient(t, reg, front, proxyWSOrigin, "dave")
	_, charIDD, jrD, wsD := d.enterWorldIn(t, "dave", "mage", "hero", protocol.JoinRoomNextAvailable, "")
	if jrD.GetRoomId() != secondRoom || jrD.GetOccupancy() != 2 {
		t.Fatalf("dave join = %+v, want %s occ=2", jrD, secondRoom)
	}
	if len(wsD.Entities) != 2 || findEntity(wsD.Entities, charIDC) == nil || findEntity(wsD.Entities, charIDD) == nil {
		t.Fatalf("dave snapshot = %v, want the second-room pair", entityIDs(wsD.Entities))
	}
	requireWS[*mmov1.SpawnEntity](t, c, "SpawnEntity dave", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == charIDD
	})

	// Carol moves in the second room; dave observes it, proving that room's
	// live sim.
	c.send(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: moveSpeed, Yaw: moveYaw})
	iso := requireWS[*mmov1.Snapshot](t, d, "snapshot showing carol moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDC)
		return e != nil && e.Pos != nil && e.Pos.X > spawnC.X+0.1
	})
	if len(iso.Entities) != 2 || findEntity(iso.Entities, charIDA) != nil || findEntity(iso.Entities, charIDB) != nil {
		t.Fatalf("second-room snapshot leaked default-room players: %v", entityIDs(iso.Entities))
	}

	// Freshness marker in the default room: bob moves and alice's resulting
	// snapshot (generated after the second room existed and carol moved) must
	// only carry the default pair.
	b.send(t, &mmov1.MoveInput{Seq: 2, Dir: &mmov1.Vec2{X: 0, Z: 1}, Speed: moveSpeed, Yaw: 0.5})
	fresh := requireWS[*mmov1.Snapshot](t, a, "snapshot showing bob moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDB)
		return e != nil && e.Pos != nil && e.Pos.Z > testSpawnZ+0.1
	})
	if len(fresh.Entities) != 2 || findEntity(fresh.Entities, charIDA) == nil || findEntity(fresh.Entities, charIDB) == nil {
		t.Fatalf("default snapshot = %v, want the default pair only", entityIDs(fresh.Entities))
	}
	if findEntity(fresh.Entities, charIDC) != nil || findEntity(fresh.Entities, charIDD) != nil {
		t.Fatalf("default snapshot leaked second-room players: %v", entityIDs(fresh.Entities))
	}
}
