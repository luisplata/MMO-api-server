package e2e

// Headless E2E gate (spec R20/S20.1 + R9/S9.1-S9.2, design PR4b): two
// real clients connect to an in-process server over loopback TCP + UDP,
// complete the full lifecycle, and prove the server-authoritative
// simulation end-to-end — client A moves, client B observes A's NEW
// position and yaw in a Snapshot over UDP within the broadcast cadence.
// The same run verifies channel separation (lifecycle over TCP, MoveInput
// + snapshots over UDP) and that no reliable-UDP (KCP) layer wraps the
// datagrams.
//
// The server is started through the exported internal/server API on
// OS-assigned loopback ports: the test pre-binds and releases 127.0.0.1:0
// sockets to learn two free ports, then hands them to server.New + Run,
// retrying on the rare port-reuse race. Every client wait is bounded by
// waitTimeout (select + time.After) — there are no unbounded reads in
// this file.

import (
	"context"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/server"
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

// lifecycleTypes are the messages that MUST ride the reliable channel
// (spec R9/S9.1): connection lifecycle, handshake, auth, spawn/despawn
// and reliable commands.
var lifecycleTypes = map[string]bool{
	"Hello": true, "ServerInfo": true, "AuthRequest": true,
	"AuthResponse": true, "EnterWorld": true, "WorldSnapshot": true,
	"SpawnEntity": true, "DespawnEntity": true, "VersionMismatch": true,
	"Ack": true,
}

// realtimeTypes are the messages that MUST ride UDP (spec R9/S9.1):
// MoveInput (client→server) and position snapshots (server→client).
var realtimeTypes = map[string]bool{
	"MoveInput": true,
	"Snapshot":  true,
}

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

// startServer launches an in-process server via the exported
// internal/server API on OS-assigned loopback ports. It returns the TCP
// and UDP addresses clients dial, plus a shutdown func that cancels the
// run context and waits for the graceful teardown (Run returns). The
// server boots on the committed hills fixture (the -map load path), so
// the derived-Y hill gate is active in every test here.
func startServer(t *testing.T) (tcpAddr, udpAddr string, shutdown func()) {
	t.Helper()
	templates := e2eTemplateRepo(t)
	characters := e2eCharacterRepo(t)
	for attempt := 0; ; attempt++ {
		tcpAddr, udpAddr = freeLoopbackPorts(t)
		srv, err := server.New(server.Config{
			TCPAddr: tcpAddr, UDPAddr: udpAddr,
			TickRate: game.TickRate,
			DevAuth:  true,
			SpawnX:   testSpawnX, SpawnZ: testSpawnZ,
			MinProtoVer: protoVersion, MaxProtoVer: protoVersion,
			Heights:    fixtureHeightfield(t),
			Templates:  templates,
			Characters: characters,
		})
		if err != nil {
			t.Fatalf("server.New: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Run(ctx) }()
		select {
		case err := <-done:
			// Run exited immediately: a port was stolen between the
			// probe's release and the server's bind. Retry with fresh
			// ephemeral ports (bounded).
			cancel()
			if attempt < 5 {
				continue
			}
			t.Fatalf("server.Run exited before serving: %v", err)
		case <-time.After(100 * time.Millisecond):
			// Both listeners bound and the three loops are running; Run
			// now blocks until cancel.
			return tcpAddr, udpAddr, func() { cancel(); <-done }
		}
	}
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

// freeLoopbackPorts finds two free loopback ports by binding :0 sockets
// and releasing them. The addresses are then handed to server.Run, which
// re-binds them; startServer retries if a race loses one.
func freeLoopbackPorts(t *testing.T) (string, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe tcp listen: %v", err)
	}
	tcpAddr := ln.Addr().String()
	_ = ln.Close()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe udp listen: %v", err)
	}
	udpAddr := pc.LocalAddr().String()
	_ = pc.Close()
	return tcpAddr, udpAddr
}

// tcpMsg is one decoded TCP frame (or a decode failure).
type tcpMsg struct {
	env *protocol.Envelope
	msg proto.Message
	err error
}

// udpMsg is one decoded UDP datagram (or a decode failure).
type udpMsg struct {
	env *protocol.Envelope
	msg proto.Message
	err error
}

// client is a real wire client: a TCP conn for the lifecycle and a UDP
// socket for the bind token, MoveInputs and incoming snapshots. Every
// received frame is decoded with protocol.DecodeEnvelope + reg.DecodeMessage
// and classified by message type for the channel-separation assertions.
type client struct {
	t    *testing.T
	reg  *protocol.Registry
	name string

	tcp    net.Conn
	udp    net.PacketConn
	srvUDP net.Addr

	tcpCh chan tcpMsg // decoded TCP frames, closed when the conn drops
	udpCh chan udpMsg // decoded UDP datagrams, closed when the socket drops

	mu       sync.Mutex
	tcpSeen  map[string]int // received message types over TCP
	udpSeen  map[string]int // received message types over UDP
	udpBad   int            // datagrams that failed raw-envelope decode
	sentTCP  map[string]int // message types written over TCP
	sentUDP  map[string]int // message types written over UDP
	sentBind int            // raw bind-token datagrams written over UDP
}

// newClient dials the server TCP listener (retrying while the server
// finishes binding) and opens a fresh loopback UDP socket, then starts
// the two reader goroutines. Conn/socket teardown is registered via
// t.Cleanup.
func newClient(t *testing.T, reg *protocol.Registry, tcpAddr, udpAddr, name string) *client {
	t.Helper()
	var conn net.Conn
	var err error
	for i := 0; i < 40; i++ {
		conn, err = net.DialTimeout("tcp", tcpAddr, 200*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("%s: dial %s: %v", name, tcpAddr, err)
	}
	udpPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("%s: open udp socket: %v", name, err)
	}
	srvUDP, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		t.Fatalf("%s: resolve %s: %v", name, udpAddr, err)
	}
	c := &client{
		t: t, reg: reg, name: name,
		tcp: conn, udp: udpPC, srvUDP: srvUDP,
		tcpCh: make(chan tcpMsg, 64), udpCh: make(chan udpMsg, 64),
		tcpSeen: make(map[string]int), udpSeen: make(map[string]int),
		sentTCP: make(map[string]int), sentUDP: make(map[string]int),
	}
	t.Cleanup(func() { _ = conn.Close(); _ = udpPC.Close() })
	go c.readTCP()
	go c.readUDP()
	return c
}

// readTCP drains TCP frames, decodes each as a raw envelope and classifies
// it. The channel closes when the conn drops.
func (c *client) readTCP() {
	defer close(c.tcpCh)
	for {
		frame, err := network.ReadFrame(c.tcp)
		if err != nil {
			return // conn closed — no more frames
		}
		env, err := protocol.DecodeEnvelope(frame)
		if err != nil {
			c.tcpCh <- tcpMsg{err: err}
			continue
		}
		msg, err := c.reg.DecodeMessage(*env)
		if err != nil {
			c.tcpCh <- tcpMsg{err: err}
			continue
		}
		c.mu.Lock()
		c.tcpSeen[string(msg.ProtoReflect().Descriptor().Name())]++
		c.mu.Unlock()
		c.tcpCh <- tcpMsg{env: env, msg: msg}
	}
}

// readUDP drains datagrams, decoding each as a RAW envelope — this is the
// no-KCP proof (spec S9.2): a reliable-UDP wrapper would put garbage where
// the v1 magic lives and every datagram would fail DecodeEnvelope. The
// channel closes when the socket drops.
func (c *client) readUDP() {
	defer close(c.udpCh)
	for {
		payload, _, err := network.ReadDatagram(c.udp)
		if err != nil {
			return // socket closed — no more datagrams
		}
		env, err := protocol.DecodeEnvelope(payload)
		if err != nil {
			c.mu.Lock()
			c.udpBad++
			c.mu.Unlock()
			c.udpCh <- udpMsg{err: err}
			continue
		}
		msg, err := c.reg.DecodeMessage(*env)
		if err != nil {
			c.udpCh <- udpMsg{err: err}
			continue
		}
		c.mu.Lock()
		c.udpSeen[string(msg.ProtoReflect().Descriptor().Name())]++
		c.mu.Unlock()
		c.udpCh <- udpMsg{env: env, msg: msg}
	}
}

// sendTCP writes msg as a length-prefixed envelope frame over the
// lifecycle channel.
func (c *client) sendTCP(t *testing.T, msg proto.Message) {
	t.Helper()
	frame, err := c.reg.EncodeMessage(msg, protocol.Envelope{Version: protoVersion})
	if err != nil {
		t.Fatalf("%s: encode %T: %v", c.name, msg, err)
	}
	c.mu.Lock()
	c.sentTCP[string(msg.ProtoReflect().Descriptor().Name())]++
	c.mu.Unlock()
	if err := network.WriteFrame(c.tcp, frame); err != nil {
		t.Fatalf("%s: write %T over tcp: %v", c.name, msg, err)
	}
}

// sendUDP writes msg as one envelope datagram over the real-time channel.
func (c *client) sendUDP(t *testing.T, msg proto.Message) {
	t.Helper()
	frame, err := c.reg.EncodeMessage(msg, protocol.Envelope{Version: protoVersion})
	if err != nil {
		t.Fatalf("%s: encode %T: %v", c.name, msg, err)
	}
	c.mu.Lock()
	c.sentUDP[string(msg.ProtoReflect().Descriptor().Name())]++
	c.mu.Unlock()
	if err := network.SendDatagram(c.udp, c.srvUDP, frame); err != nil {
		t.Fatalf("%s: send %T over udp: %v", c.name, msg, err)
	}
}

// bindUDP sends the raw udpToken as the first datagram (spec R13): the
// v1 contract has no BindRequest message — the datagram payload IS the
// token.
func (c *client) bindUDP(t *testing.T, token []byte) {
	t.Helper()
	c.mu.Lock()
	c.sentBind++
	c.mu.Unlock()
	if err := network.SendDatagram(c.udp, c.srvUDP, token); err != nil {
		t.Fatalf("%s: send bind token over udp: %v", c.name, err)
	}
}

func (c *client) tcpSeenSnapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string]int, len(c.tcpSeen))
	for k, v := range c.tcpSeen {
		m[k] = v
	}
	return m
}

func (c *client) udpSeenSnapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string]int, len(c.udpSeen))
	for k, v := range c.udpSeen {
		m[k] = v
	}
	return m
}

func (c *client) sentTCPCounts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string]int, len(c.sentTCP))
	for k, v := range c.sentTCP {
		m[k] = v
	}
	return m
}

func (c *client) sentUDPCounts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string]int, len(c.sentUDP))
	for k, v := range c.sentUDP {
		m[k] = v
	}
	return m
}

func (c *client) udpBadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.udpBad
}

// requireTCP waits for the next TCP frame whose decoded message is of
// type T and satisfies opt (default: any). Other frames are skipped (and
// already recorded by the reader). An undecodable frame or a closed
// reader fails the test. Every wait is bounded by waitTimeout.
func requireTCP[T proto.Message](t *testing.T, c *client, what string, opt ...func(T) bool) T {
	t.Helper()
	var zero T
	want := func(T) bool { return true }
	if len(opt) > 0 {
		want = opt[0]
	}
	deadline := time.Now().Add(waitTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("%s: timed out waiting for TCP %s; types seen: %v", c.name, what, c.tcpSeenSnapshot())
			return zero
		}
		select {
		case m, ok := <-c.tcpCh:
			if !ok {
				t.Fatalf("%s: tcp reader closed before %s arrived; types seen: %v", c.name, what, c.tcpSeenSnapshot())
				return zero
			}
			if m.err != nil {
				t.Fatalf("%s: undecodable tcp frame while waiting for %s: %v", c.name, what, m.err)
				return zero
			}
			if msg, ok := m.msg.(T); ok && want(msg) {
				return msg
			}
		case <-time.After(remaining):
			t.Fatalf("%s: timed out waiting for TCP %s; types seen: %v", c.name, what, c.tcpSeenSnapshot())
			return zero
		}
	}
}

// requireUDP is the UDP analogue of requireTCP; a datagram that fails
// raw-envelope decode fails the test (a KCP wrapper would do exactly
// that).
func requireUDP[T proto.Message](t *testing.T, c *client, what string, opt ...func(T) bool) T {
	t.Helper()
	var zero T
	want := func(T) bool { return true }
	if len(opt) > 0 {
		want = opt[0]
	}
	deadline := time.Now().Add(waitTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("%s: timed out waiting for UDP %s; types seen: %v", c.name, what, c.udpSeenSnapshot())
			return zero
		}
		select {
		case m, ok := <-c.udpCh:
			if !ok {
				t.Fatalf("%s: udp reader closed before %s arrived; types seen: %v", c.name, what, c.udpSeenSnapshot())
				return zero
			}
			if m.err != nil {
				t.Fatalf("%s: undecodable udp datagram while waiting for %s: %v", c.name, what, m.err)
				return zero
			}
			if msg, ok := m.msg.(T); ok && want(msg) {
				return msg
			}
		case <-time.After(remaining):
			t.Fatalf("%s: timed out waiting for UDP %s; types seen: %v", c.name, what, c.udpSeenSnapshot())
			return zero
		}
	}
}

// connectAndAuth drives one client through the TCP handshake to the
// `selecting` phase (design D4): Hello→ServerInfo, AuthRequest→
// AuthResponse (no spawn). It returns the AuthResponse and leaves the
// session in `selecting`, where List/Create/Select are the only valid
// messages and EnterWorld is rejected until a character is chosen.
func (c *client) connectAndAuth(t *testing.T, username string) *mmov1.AuthResponse {
	t.Helper()

	c.sendTCP(t, &mmov1.Hello{ProtoVer: protoVersion})
	si := requireTCP[*mmov1.ServerInfo](t, c, "ServerInfo")
	if si.ProtoVer != protoVersion {
		t.Errorf("%s: ServerInfo.ProtoVer = %d, want %d", c.name, si.ProtoVer, protoVersion)
	}
	if si.TickRate != game.TickRate {
		t.Errorf("%s: ServerInfo.TickRate = %d, want %d", c.name, si.TickRate, game.TickRate)
	}
	if si.ServerTime == 0 {
		t.Errorf("%s: ServerInfo.ServerTime = 0, want a real timestamp", c.name)
	}

	c.sendTCP(t, &mmov1.AuthRequest{Username: username, Password: "pw"})
	ar := requireTCP[*mmov1.AuthResponse](t, c, "AuthResponse")
	if !ar.Ok {
		t.Fatalf("%s: auth rejected for %q: %s", c.name, username, ar.ErrorMessage)
	}
	if ar.PlayerId != username {
		t.Errorf("%s: AuthResponse.PlayerId = %q, want %q", c.name, ar.PlayerId, username)
	}
	if len(ar.UdpToken) == 0 {
		t.Errorf("%s: AuthResponse.UdpToken is empty", c.name)
	}
	// Design D4: auth no longer resolves the spawn — SelectCharacter does.
	if ar.SpawnPos != nil {
		t.Errorf("%s: AuthResponse.SpawnPos = %v, want nil (resolved by Select, design D4)", c.name, ar.SpawnPos)
	}
	return ar
}

// listCharacters sends ListCharacters and returns the response.
func (c *client) listCharacters(t *testing.T) *mmov1.CharacterList {
	t.Helper()
	c.sendTCP(t, &mmov1.ListCharacters{})
	return requireTCP[*mmov1.CharacterList](t, c, "CharacterList")
}

// createCharacter sends CreateCharacter and returns the response.
func (c *client) createCharacter(t *testing.T, templateID, name string) *mmov1.CreateCharacterResponse {
	t.Helper()
	c.sendTCP(t, &mmov1.CreateCharacter{TemplateId: templateID, Name: name})
	return requireTCP[*mmov1.CreateCharacterResponse](t, c, "CreateCharacterResponse")
}

// selectCharacter sends SelectCharacter and returns the response.
func (c *client) selectCharacter(t *testing.T, charID string) *mmov1.SelectCharacterResponse {
	t.Helper()
	c.sendTCP(t, &mmov1.SelectCharacter{CharacterId: charID})
	return requireTCP[*mmov1.SelectCharacterResponse](t, c, "SelectCharacterResponse")
}

// enterWorld drives one client through the whole TCP lifecycle (spec R10
// happy path, design D4 selecting phase): Hello→ServerInfo,
// AuthRequest→AuthResponse (no spawn), CreateCharacter→
// CreateCharacterResponse, SelectCharacter→SelectCharacterResponse (the
// resolved spawn), EnterWorld→empty ack WorldSnapshot→REAL WorldSnapshot
// carrying the current world. It returns the AuthResponse (identity,
// token), the selected character's spawn, the selected CHARACTER id (the
// world entity id, design D3), and the real WorldSnapshot.
//
// Design D5 (templateId): the created Character, the SelectCharacter
// response and the returned WorldSnapshot's entity for the selected
// character all carry the character's templateId — the client picks the
// visual prefab from it. `templateID`/`name` parameterize the created
// character so callers can triangulate that the templateId is derived
// from the chosen template, not a hardcoded constant.
func (c *client) enterWorld(t *testing.T, username, templateID, name string) (*mmov1.AuthResponse, *mmov1.Vec3, string, *mmov1.WorldSnapshot) {
	t.Helper()

	ar := c.connectAndAuth(t, username)

	// Spec "List characters": a fresh account lists an empty CharacterList
	// (not an error) before any character is created — the first step of
	// the selecting flow.
	if cl := c.listCharacters(t); len(cl.Characters) != 0 {
		t.Errorf("%s: fresh account CharacterList = %d chars, want 0", c.name, len(cl.Characters))
	}

	// Selecting phase (design D4): create a character then select it. The
	// name is per-account unique; each client enters the world once.
	cc := c.createCharacter(t, templateID, name)
	if !cc.Ok {
		t.Fatalf("%s: CreateCharacter failed: %s", c.name, cc.ErrorMessage)
	}
	if cc.Character == nil {
		t.Fatalf("%s: CreateCharacterResponse.Character is nil", c.name)
	}
	charID := cc.Character.Id
	if charID == "" {
		t.Fatalf("%s: CreateCharacterResponse.Character.Id is empty", c.name)
	}
	// The created character carries the frozen template stats + templateId
	// (design D2/D5) of the template it was created from.
	if cc.Character.TemplateId != templateID {
		t.Errorf("%s: created Character.TemplateId = %q, want %q", c.name, cc.Character.TemplateId, templateID)
	}
	sc := c.selectCharacter(t, charID)
	if !sc.Ok {
		t.Fatalf("%s: SelectCharacter failed: %s", c.name, sc.ErrorMessage)
	}
	if sc.SpawnPos == nil {
		t.Fatalf("%s: SelectCharacterResponse.SpawnPos is nil", c.name)
	}
	// Hill gate (task 2.8, CTH-3): the resolved spawn Y carries the terrain
	// height at the spawn point — the fixture peaks at 25 there.
	if sc.SpawnPos.Y != 25 {
		t.Errorf("%s: SelectCharacterResponse.SpawnPos.Y = %v, want 25 (hill peak at spawn)", c.name, sc.SpawnPos.Y)
	}

	c.sendTCP(t, &mmov1.EnterWorld{})
	// The session-layer ack WorldSnapshot is empty (the session has no
	// world state); the server wiring follows with the real one. Both
	// must arrive, in that order — two distinct WorldSnapshot frames.
	requireTCP[*mmov1.WorldSnapshot](t, c, "empty WorldSnapshot", func(ws *mmov1.WorldSnapshot) bool {
		return len(ws.Entities) == 0
	})
	ws := requireTCP[*mmov1.WorldSnapshot](t, c, "real WorldSnapshot", func(ws *mmov1.WorldSnapshot) bool {
		return len(ws.Entities) > 0
	})
	// Design D3: the world entity id is the CHARACTER id, not the account
	// id. The real WorldSnapshot must carry the character.
	if findEntity(ws.Entities, charID) == nil {
		t.Fatalf("%s: real WorldSnapshot does not contain character %q (entity id): %v", c.name, charID, entityIDs(ws.Entities))
	}
	// Design D5: the snapshot's EntityState carries the character's
	// templateId — the Unity client selects the visual model from it.
	if e := findEntity(ws.Entities, charID); e.TemplateId != templateID {
		t.Errorf("%s: real WorldSnapshot entity %q templateId = %q, want %q", c.name, charID, e.TemplateId, templateID)
	}
	return ar, sc.SpawnPos, charID, ws
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

// TestTwoPlayersSeeEachOtherMoveEndToEnd is THE GATE (spec R20/S20.1):
// two in-world clients A (alice) and B (bob); A sends a MoveInput over
// UDP; B must observe A's NEW position and yaw in a Snapshot over UDP
// within the broadcast cadence. It also proves the pipeline pieces on the
// way: full lifecycle, real enter-world snapshot, interest fanout over
// TCP, UDP token binding, and the pre-move state.
func TestTwoPlayersSeeEachOtherMoveEndToEnd(t *testing.T) {
	tcpAddr, udpAddr, shutdown := startServer(t)
	t.Cleanup(shutdown)

	reg := protocol.NewWorldRegistry()

	// Triangulation (design D5): alice uses the warrior template, bob the
	// mage template, so the templateId on each entity is provably derived
	// from the chosen template and not a hardcoded constant.
	a := newClient(t, reg, tcpAddr, udpAddr, "alice")
	arA, spawnA, charIDA, wsA := a.enterWorld(t, "alice", "warrior", "hero")

	b := newClient(t, reg, tcpAddr, udpAddr, "bob")
	arB, _, charIDB, wsB := b.enterWorld(t, "bob", "mage", "hero")

	// Design D5: A's real WorldSnapshot entity for her character carries
	// the character's templateId (the visual-prefab hint for Unity).
	if e := findEntity(wsA.Entities, charIDA); e == nil {
		t.Fatalf("alice: real WorldSnapshot lacks %q (her character): %v", charIDA, entityIDs(wsA.Entities))
	} else if e.TemplateId != "warrior" {
		t.Errorf("alice: real WorldSnapshot entity %q templateId = %q, want warrior", charIDA, e.TemplateId)
	}

	// B's real WorldSnapshot (full state) already contains A's character,
	// which entered first — B sees A over TCP before A ever moves.
	if e := findEntity(wsB.Entities, charIDA); e == nil {
		t.Fatalf("bob: real WorldSnapshot lacks %q (A's character): %v", charIDA, entityIDs(wsB.Entities))
	} else if e.TemplateId != "warrior" {
		t.Errorf("bob: real WorldSnapshot entity %q templateId = %q, want warrior", charIDA, e.TemplateId)
	}

	// Interest fanout over TCP (spec S18.2, design D4): A is notified
	// that B's character spawned — the flat resolver announces the
	// newcomer to everyone else. The SpawnEntity state also carries the
	// templateId (design D5), which for bob is the mage template — proving
	// the value follows the spawned character, not the observer.
	spawnB := requireTCP[*mmov1.SpawnEntity](t, a, "SpawnEntity B", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == charIDB
	})
	if spawnB.State == nil || spawnB.State.TemplateId != "mage" {
		t.Errorf("alice: SpawnEntity for bob state templateId = %v, want mage", spawnB.State)
	}

	// Both clients bind their UDP peer with the raw token (spec R13).
	a.bindUDP(t, arA.UdpToken)
	b.bindUDP(t, arB.UdpToken)

	// Before A moves: B must receive a Snapshot over UDP showing alice at
	// the spawn position with yaw 0 — proves the UDP pipeline end-to-end
	// and pins the pre-move state.
	first := requireUDP[*mmov1.Snapshot](t, b, "first snapshot")
	alice0 := findEntity(first.Entities, charIDA)
	if alice0 == nil {
		t.Fatalf("bob: first snapshot lacks %q (A's character): %v", charIDA, entityIDs(first.Entities))
	}
	// Design D5: the UDP Snapshot EntityState also carries the character's
	// templateId, so a client that receives only snapshots can still pick
	// the visual prefab.
	if alice0.TemplateId != "warrior" {
		t.Errorf("bob: alice templateId in first snapshot = %q, want warrior", alice0.TemplateId)
	}
	if alice0.Pos == nil || alice0.Pos.X != spawnA.X || alice0.Pos.Z != spawnA.Z {
		t.Errorf("bob: alice before moving = pos %v, want spawn (%v, %v)", alice0.Pos, spawnA.X, spawnA.Z)
	}
	if alice0.Pos == nil || alice0.Pos.Y != spawnA.Y {
		t.Errorf("bob: alice before moving = y %v, want spawn y %v (terrain-derived)", alice0.Pos, spawnA.Y)
	}
	if alice0.Yaw != 0 {
		t.Errorf("bob: alice yaw before moving = %v, want 0", alice0.Yaw)
	}
	t.Logf("bob: baseline snapshot seq=%d shows alice at (%v, %v, %v) yaw %v", first.Seq, alice0.Pos.X, alice0.Pos.Y, alice0.Pos.Z, alice0.Yaw)

	// A moves: +X at 5 u/s with yaw 1.5 rad — one MoveInput over UDP
	// (spec R15). The sim applies it at the next 20 Hz tick and the
	// velocity persists, so alice's position advances monotonically.
	a.sendUDP(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: moveSpeed, Yaw: moveYaw})

	// THE GATE (spec S20.1): B observes A's NEW position and yaw in a
	// Snapshot over UDP within the broadcast cadence (bounded 5 s).
	moved := requireUDP[*mmov1.Snapshot](t, b, "snapshot showing A moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, charIDA)
		return e != nil && e.Pos != nil && e.Pos.X > spawnA.X+0.1 && e.Yaw == moveYaw
	})
	alice1 := findEntity(moved.Entities, charIDA)
	if alice1.Pos.X <= spawnA.X {
		t.Errorf("bob: alice did not advance: pos = %v, spawn X = %v", alice1.Pos, spawnA.X)
	}
	if alice1.Yaw != moveYaw {
		t.Errorf("bob: alice yaw = %v, want %v", alice1.Yaw, moveYaw)
	}
	if moved.Seq <= first.Seq {
		t.Errorf("bob: snapshot seq not monotonic: first=%d moved=%d", first.Seq, moved.Seq)
	}
	// THE HILL GATE (task 2.8, CTH-7): B sees A's Y equal to the terrain
	// height at A's broadcast position — derived server-side from the
	// fixture, never client-supplied.
	heights := fixtureHeightfield(t)
	wantY := heights.HeightAt(alice1.Pos.X, alice1.Pos.Z)
	if math.Abs(float64(alice1.Pos.Y-wantY)) > 1e-3 {
		t.Errorf("bob: alice Y = %v, want terrain %v at (%v, %v) [hill gate]", alice1.Pos.Y, wantY, alice1.Pos.X, alice1.Pos.Z)
	}
	t.Logf("GATE PASSED: bob saw alice at (%.2f, %.2f, %.2f) yaw %.2f rad (snapshot seq %d), spawn was (%v, %v, %v)",
		alice1.Pos.X, alice1.Pos.Y, alice1.Pos.Z, alice1.Yaw, moved.Seq, spawnA.X, spawnA.Y, spawnA.Z)
}

// TestSelectingPhaseRejections pins the character-management rejections
// (spec character-management, design D4): a bad/invalid template Create,
// a duplicate-name Create and a wrong-owner Select must each be answered
// with a not-ok response over TCP and must NOT mutate the session's
// active character. It drives two real clients to the `selecting` phase
// (no EnterWorld) and exercises each rejection over the wire — the same
// server instance the gate uses, proving the session layer rejects
// without closing the session.
func TestSelectingPhaseRejections(t *testing.T) {
	tcpAddr, udpAddr, shutdown := startServer(t)
	t.Cleanup(shutdown)

	reg := protocol.NewWorldRegistry()
	alice := newClient(t, reg, tcpAddr, udpAddr, "alice")
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
	bob := newClient(t, reg, tcpAddr, udpAddr, "bob")
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

// TestChannelSeparationAndNoKCPWrapper pins spec R9 (S9.1/S9.2): the TCP
// channel carries lifecycle (handshake, auth, spawn/despawn), the UDP
// channel carries MoveInput + snapshots, and every datagram is a raw v1
// envelope — no KCP or other reliable-UDP layer wraps anything.
func TestChannelSeparationAndNoKCPWrapper(t *testing.T) {
	tcpAddr, udpAddr, shutdown := startServer(t)
	t.Cleanup(shutdown)

	reg := protocol.NewWorldRegistry()
	a := newClient(t, reg, tcpAddr, udpAddr, "alice")
	arA, _, _, _ := a.enterWorld(t, "alice", "warrior", "hero")
	b := newClient(t, reg, tcpAddr, udpAddr, "bob")
	arB, _, charIDB, _ := b.enterWorld(t, "bob", "warrior", "hero")

	// Lifecycle + interest over TCP: A is notified that B's character
	// spawned (design D3 — the fanout uses the character id).
	requireTCP[*mmov1.SpawnEntity](t, a, "SpawnEntity B", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == charIDB
	})

	// Exercise the real-time channel: A binds then sends MoveInput over
	// UDP; B binds and receives the resulting Snapshot stream over UDP.
	a.bindUDP(t, arA.UdpToken)
	b.bindUDP(t, arB.UdpToken)
	a.sendUDP(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 0, Z: 1}, Speed: 3, Yaw: 0.7})
	requireUDP[*mmov1.Snapshot](t, b, "snapshot over UDP")

	// ---- Channel separation (spec S9.1) ----
	// Received over UDP: only real-time messages — never lifecycle.
	for name, n := range b.udpSeenSnapshot() {
		if lifecycleTypes[name] {
			t.Errorf("bob: lifecycle message %s (%d) arrived over UDP", name, n)
		}
	}
	// Received over TCP: only lifecycle — never real-time.
	for name, n := range b.tcpSeenSnapshot() {
		if realtimeTypes[name] {
			t.Errorf("bob: real-time message %s (%d) arrived over TCP", name, n)
		}
	}
	// Both directions of the real-time channel must actually be used.
	if n := b.udpSeenSnapshot()["Snapshot"]; n == 0 {
		t.Errorf("bob: received no Snapshot over UDP")
	}
	if n := b.tcpSeenSnapshot()["Snapshot"]; n != 0 {
		t.Errorf("bob: received Snapshot(s) over TCP: %d", n)
	}

	// Client-side discipline: lifecycle outbound on TCP only, MoveInput
	// on UDP only, and the raw bind token as a datagram (not an
	// envelope).
	sentUDP := a.sentUDPCounts()
	for name := range sentUDP {
		if lifecycleTypes[name] {
			t.Errorf("alice: sent lifecycle message %s over UDP", name)
		}
	}
	if n := sentUDP["MoveInput"]; n == 0 {
		t.Errorf("alice: sent no MoveInput over UDP")
	}
	sentTCP := a.sentTCPCounts()
	if n := sentTCP["MoveInput"]; n != 0 {
		t.Errorf("alice: sent MoveInput over TCP: %d", n)
	}
	for _, name := range []string{"Hello", "AuthRequest", "EnterWorld"} {
		if n := sentTCP[name]; n == 0 {
			t.Errorf("alice: never sent %s over TCP", name)
		}
	}
	if b.sentBind == 0 {
		t.Errorf("bob: never sent the raw bind-token datagram over UDP")
	}

	// ---- No reliable-UDP wrapper (spec S9.2) ----
	// Every datagram B received decoded as a raw v1 envelope (magic
	// 0x4D4D): a KCP-like wrapper would have failed the magic check.
	if n := b.udpBadCount(); n != 0 {
		t.Errorf("bob: %d UDP datagrams failed to decode as raw envelopes (KCP wrapper?)", n)
	}
	assertNoKCPDependency(t)
}

// assertNoKCPDependency pins spec S9.2 statically: the module declares no
// reliable-UDP dependency (kcp-go, smux, quic, ...). The behavioral
// half — every datagram is a raw v1 envelope — is asserted in
// TestChannelSeparationAndNoKCPWrapper.
func assertNoKCPDependency(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	lower := strings.ToLower(string(mod))
	for _, dep := range []string{"kcp", "smux", "quic-go", "reliable-udp"} {
		if strings.Contains(lower, dep) {
			t.Errorf("go.mod declares a reliable-UDP dependency: %q", dep)
		}
	}
}
