package e2e

// Headless E2E gate over WSS (spec realtime-web-transport: WSS único):
// real clients dial the wss:// endpoint through a TLS-terminating proxy,
// complete the full lifecycle, and prove the server-authoritative
// simulation end-to-end — client A moves, client B observes A's NEW
// position and yaw in a Snapshot within the broadcast cadence. One
// WebSocket message carries exactly one protocol envelope; there is no
// separate lifecycle/realtime channel and no datagram layer.
//
// The server is started through the exported internal/server API on an
// OS-assigned loopback port: the test pre-binds and releases a
// 127.0.0.1:0 socket to learn a free port, then hands it to server.New +
// Run, retrying on the rare port-reuse race. Every client wait is bounded
// by waitTimeout — there are no unbounded reads in this file.

import (
	"math"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/template"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// protoVersion is the only protocol major v2 negotiates (server range
// 2..2, Hello{ProtoVer:2}); a v1 client is rejected with
// VersionMismatch before auth (spec CTH-4).
const protoVersion = 2

// waitTimeout bounds every client wait in this file.
const waitTimeout = 5 * time.Second

// Server tunables used by the tests: dev auth spawns players at a fixed
// point; clients move at 5 u/s (well under DefaultMaxSpeed 10) so the
// input is applied unclamped.
const (
	testSpawnX = 100.0
	testSpawnZ = 200.0
	moveSpeed  = 5.0
	moveYaw    = 1.5
)

// fixtureHeightfield loads the committed hills fixture through the
// canonical file loader — the same path cmd/server's -map flag takes —
// so the hill gate proves the file-based boot end-to-end (task 2.6/2.9).
func fixtureHeightfield(t *testing.T) *world.Heightfield {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	h, err := world.LoadHeightmap(filepath.Join(root, "internal", "world", "testdata", "hills.heightmap"))
	if err != nil {
		t.Fatalf("load hills fixture: %v", err)
	}
	return h
}

// e2eTemplateRepo loads the committed template catalog through the
// canonical file loader (the same path the server uses), so the selecting
// phase's CreateCharacter validates against real templates.
func e2eTemplateRepo(t *testing.T) template.TemplateRepository {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	repo, err := template.NewFileTemplateRepository(filepath.Join(root, "data", "templates.json"))
	if err != nil {
		t.Fatalf("load templates catalog: %v", err)
	}
	return repo
}

// e2eCharacterRepo builds a character store on a temp file so the E2E run
// never pollutes the committed data/characters.json. Characters are
// created per account at runtime (design D4 selecting phase).
func e2eCharacterRepo(t *testing.T) character.CharacterRepository {
	t.Helper()
	repo, err := character.NewFileCharacterRepository(filepath.Join(t.TempDir(), "characters.json"))
	if err != nil {
		t.Fatalf("load character store: %v", err)
	}
	return repo
}

// freeLoopbackAddr finds a free loopback TCP port by binding :0 and
// releasing it. The address is then handed to server.Run, which re-binds
// it; startServer retries if a race loses it.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe loopback listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// findEntity returns the entity state for id, or nil.
func findEntity(ents []*mmov1.EntityState, id string) *mmov1.EntityState {
	for _, e := range ents {
		if e.Id == id {
			return e
		}
	}
	return nil
}

// entityIDs lists the entity ids in a snapshot (for diagnostics).
func entityIDs(ents []*mmov1.EntityState) []string {
	ids := make([]string, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, e.Id)
	}
	return ids
}

// TestTwoPlayersSeeEachOtherMoveEndToEnd is the P10 GATE over WSS: two
// sessions on one wss:// endpoint complete the full login/character/world
// lifecycle, enter the default room, observe each other, exchange
// MoveInputs as one envelope per WebSocket message, and disconnect
// cleanly. After A leaves, B receives A's despawn and keeps receiving
// snapshots; A reconnects with the same character and is seen again.
func TestTwoPlayersSeeEachOtherMoveEndToEnd(t *testing.T) {
	wssURL, shutdown := startServer(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	reg := protocol.NewWorldRegistry()

	// Triangulation (design D5): alice uses the warrior template, bob the
	// mage template, so each templateId is derived from its own character.
	a := newWSClient(t, reg, front, proxyWSOrigin, "alice")
	spawnA, charIDA, wsA := a.enterWorld(t, "alice", "warrior", "hero")
	b := newWSClient(t, reg, front, proxyWSOrigin, "bob")
	spawnB, charIDB, wsB := b.enterWorld(t, "bob", "mage", "hero")

	// A's snapshot (taken before B existed) carries only A; B's snapshot,
	// after live entry, carries both with their own templateId.
	if e := findEntity(wsA.Entities, charIDA); e == nil || e.TemplateId != "warrior" {
		t.Fatalf("alice: A entity = %v, want template warrior", e)
	}
	if e := findEntity(wsB.Entities, charIDA); e == nil || e.TemplateId != "warrior" {
		t.Fatalf("bob: A entity = %v, want template warrior", e)
	}
	if e := findEntity(wsB.Entities, charIDB); e == nil || e.TemplateId != "mage" {
		t.Fatalf("bob: B entity = %v, want template mage", e)
	}

	// Interest fanout rides the same socket: A is told B spawned.
	requireWS[*mmov1.SpawnEntity](t, a, "SpawnEntity B", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == charIDB
	})

	// A moves +X; B observes the new position, yaw, and server-derived Y.
	a.send(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: moveSpeed, Yaw: moveYaw})
	moved := requireWS[*mmov1.Snapshot](t, b, "snapshot showing A moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDA)
		return e != nil && e.Pos != nil && e.Pos.X > spawnA.X+0.1 && e.Yaw == moveYaw
	})
	alice := findEntity(moved.Entities, charIDA)
	if wantY := fixtureHeightfield(t).HeightAt(alice.Pos.X, alice.Pos.Z); math.Abs(float64(alice.Pos.Y-wantY)) > 1e-3 {
		t.Errorf("bob: alice Y = %v, want terrain %v at (%v, %v) [hill gate]", alice.Pos.Y, wantY, alice.Pos.X, alice.Pos.Z)
	}

	// Triangulation, other direction: B moves and A observes it too.
	b.send(t, &mmov1.MoveInput{Seq: 2, Dir: &mmov1.Vec2{X: 0, Z: 1}, Speed: moveSpeed, Yaw: 0.5})
	requireWS[*mmov1.Snapshot](t, a, "snapshot showing B moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDB)
		return e != nil && e.Pos != nil && e.Pos.Z > spawnB.Z+0.1 && e.Yaw == 0.5
	})

	// Clean disconnect: A closes; B despawns it and keeps its own stream.
	a.disconnect()
	requireWS[*mmov1.DespawnEntity](t, b, "despawn of A", func(dp *mmov1.DespawnEntity) bool {
		return dp.EntityId == charIDA
	})
	after := requireWS[*mmov1.Snapshot](t, b, "snapshot after A left", func(sn *mmov1.Snapshot) bool {
		return findEntity(sn.Entities, charIDA) == nil
	})
	if findEntity(after.Entities, charIDB) == nil {
		t.Fatalf("bob vanished from his own snapshot after alice left: %v", entityIDs(after.Entities))
	}

	// Clean reconnect: the same account still owns the character, and B
	// sees it spawn again.
	a2 := newWSClient(t, reg, front, proxyWSOrigin, "alice-again")
	a2.connectAndAuth(t, "alice")
	if cl := a2.listCharacters(t); len(cl.Characters) != 1 || cl.Characters[0].Id != charIDA {
		t.Fatalf("alice reconnect: CharacterList = %v, want only %q", cl.Characters, charIDA)
	}
	if sc := a2.selectCharacter(t, charIDA); !sc.Ok {
		t.Fatalf("alice reconnect: select rejected: %s", sc.ErrorMessage)
	}
	if snap := a2.enterWorldNow(t, charIDA); findEntity(snap.Entities, charIDB) == nil {
		t.Fatalf("alice reconnect snapshot lacks bob: %v", entityIDs(snap.Entities))
	}
	requireWS[*mmov1.SpawnEntity](t, b, "SpawnEntity A again", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == charIDA
	})
}

// TestSelectingPhaseRejections pins the character-management rejections
// (spec character-management, design D4): a bad/invalid template Create,
// a duplicate-name Create and a wrong-owner Select must each be answered
// with a not-ok response over WSS and must NOT mutate the session's
// active character. It drives two real clients to the `selecting` phase
// (no EnterWorld) and exercises each rejection over the wire — the same
// server instance the gate uses, proving the session layer rejects
// without closing the session.
func TestSelectingPhaseRejections(t *testing.T) {
	wssURL, shutdown := startServer(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)

	reg := protocol.NewWorldRegistry()
	alice := newWSClient(t, reg, front, proxyWSOrigin, "alice")
	alice.connectAndAuth(t, "alice")

	// Bad/invalid template: CreateCharacter with a template id that does
	// not exist in the catalog → not-ok, nothing created, no mutation.
	bad := alice.createCharacter(t, "nope", "BadChar")
	if bad.Ok {
		t.Errorf("alice: CreateCharacter with unknown template = ok, want not-ok")
	}
	if bad.ErrorMessage == "" {
		t.Errorf("alice: CreateCharacter with unknown template has empty ErrorMessage")
	}
	if bad.Character != nil {
		t.Errorf("alice: CreateCharacter with unknown template created a character: %+v", bad.Character)
	}

	// Duplicate name: create "hero", then try "Hero" again (per-account
	// uniqueness is case-insensitive, design D7).
	first := alice.createCharacter(t, "warrior", "hero")
	if !first.Ok {
		t.Fatalf("alice: first CreateCharacter failed: %s", first.ErrorMessage)
	}
	if first.Character == nil {
		t.Fatalf("alice: first CreateCharacter returned no character")
	}
	// Spec "List characters": after a successful Create the account lists
	// exactly its character — the non-empty counterpart of the empty list
	// asserted in enterWorld, proving the list flow end-to-end.
	if cl := alice.listCharacters(t); len(cl.Characters) != 1 {
		t.Errorf("alice: CharacterList after create = %d chars, want 1", len(cl.Characters))
	}
	dup := alice.createCharacter(t, "warrior", "Hero")
	if dup.Ok {
		t.Errorf("alice: duplicate-name CreateCharacter = ok, want not-ok")
	}
	if dup.ErrorMessage == "" {
		t.Errorf("alice: duplicate-name CreateCharacter has empty ErrorMessage")
	}
	if dup.Character != nil {
		t.Errorf("alice: duplicate-name CreateCharacter created a character: %+v", dup.Character)
	}

	// Wrong-owner Select: bob creates his own character, then alice tries
	// to select it. The session must answer not-ok and leave alice's
	// active character unchanged.
	bob := newWSClient(t, reg, front, proxyWSOrigin, "bob")
	bob.connectAndAuth(t, "bob")
	bobChar := bob.createCharacter(t, "warrior", "hero")
	if !bobChar.Ok {
		t.Fatalf("bob: CreateCharacter failed: %s", bobChar.ErrorMessage)
	}
	if bobChar.Character == nil {
		t.Fatalf("bob: CreateCharacter returned no character")
	}

	// alice is still in selecting (she never selected her own character).
	// Selecting bob's character must be rejected as not-owned.
	foreign := alice.selectCharacter(t, bobChar.Character.Id)
	if foreign.Ok {
		t.Errorf("alice: SelectCharacter of bob's character = ok, want not-ok")
	}
	if foreign.ErrorMessage == "" {
		t.Errorf("alice: SelectCharacter of bob's character has empty ErrorMessage")
	}
}
