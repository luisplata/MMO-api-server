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
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/server"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// protoVersion is the only protocol version v1 negotiates (server range
// 1..1, Hello{ProtoVer:1}).
const protoVersion = 1

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

// startServer launches an in-process server via the exported
// internal/server API on OS-assigned loopback ports. It returns the TCP
// and UDP addresses clients dial, plus a shutdown func that cancels the
// run context and waits for the graceful teardown (Run returns).
func startServer(t *testing.T) (tcpAddr, udpAddr string, shutdown func()) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		tcpAddr, udpAddr = freeLoopbackPorts(t)
		srv, err := server.New(server.Config{
			TCPAddr: tcpAddr, UDPAddr: udpAddr,
			TickRate: game.TickRate,
			DevAuth:  true,
			SpawnX:   testSpawnX, SpawnZ: testSpawnZ,
			MinProtoVer: protoVersion, MaxProtoVer: protoVersion,
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

// enterWorld drives one client through the whole TCP lifecycle (spec R10
// happy path): Hello→ServerInfo, AuthRequest→AuthResponse, EnterWorld→
// empty ack WorldSnapshot→REAL WorldSnapshot carrying the current world.
// It returns the AuthResponse (identity, token, spawn) and the real
// WorldSnapshot.
func (c *client) enterWorld(t *testing.T, username string) (*mmov1.AuthResponse, *mmov1.WorldSnapshot) {
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
	if ar.SpawnPos == nil || ar.SpawnPos.X != testSpawnX || ar.SpawnPos.Z != testSpawnZ {
		t.Errorf("%s: AuthResponse.SpawnPos = %v, want (%v, %v)", c.name, ar.SpawnPos, testSpawnX, testSpawnZ)
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
	if findEntity(ws.Entities, username) == nil {
		t.Fatalf("%s: real WorldSnapshot does not contain %q: %v", c.name, username, entityIDs(ws.Entities))
	}
	return ar, ws
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

	a := newClient(t, reg, tcpAddr, udpAddr, "alice")
	arA, _ := a.enterWorld(t, "alice")

	b := newClient(t, reg, tcpAddr, udpAddr, "bob")
	arB, wsB := b.enterWorld(t, "bob")

	// B's real WorldSnapshot (full state) already contains alice, who
	// entered first — B sees A over TCP before A ever moves.
	if e := findEntity(wsB.Entities, "alice"); e == nil {
		t.Fatalf("bob: real WorldSnapshot lacks alice: %v", entityIDs(wsB.Entities))
	}

	// Interest fanout over TCP (spec S18.2, design D4): alice is notified
	// that bob spawned — the flat resolver announces the newcomer to
	// everyone else.
	requireTCP[*mmov1.SpawnEntity](t, a, "SpawnEntity bob", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == "bob"
	})

	// Both clients bind their UDP peer with the raw token (spec R13).
	a.bindUDP(t, arA.UdpToken)
	b.bindUDP(t, arB.UdpToken)

	// Before A moves: B must receive a Snapshot over UDP showing alice at
	// the spawn position with yaw 0 — proves the UDP pipeline end-to-end
	// and pins the pre-move state.
	first := requireUDP[*mmov1.Snapshot](t, b, "first snapshot")
	alice0 := findEntity(first.Entities, "alice")
	if alice0 == nil {
		t.Fatalf("bob: first snapshot lacks alice: %v", entityIDs(first.Entities))
	}
	if alice0.Pos == nil || alice0.Pos.X != arA.SpawnPos.X || alice0.Pos.Z != arA.SpawnPos.Z {
		t.Errorf("bob: alice before moving = pos %v, want spawn (%v, %v)", alice0.Pos, arA.SpawnPos.X, arA.SpawnPos.Z)
	}
	if alice0.Yaw != 0 {
		t.Errorf("bob: alice yaw before moving = %v, want 0", alice0.Yaw)
	}
	t.Logf("bob: baseline snapshot seq=%d shows alice at (%v, %v) yaw %v", first.Seq, alice0.Pos.X, alice0.Pos.Z, alice0.Yaw)

	// A moves: +X at 5 u/s with yaw 1.5 rad — one MoveInput over UDP
	// (spec R15). The sim applies it at the next 20 Hz tick and the
	// velocity persists, so alice's position advances monotonically.
	a.sendUDP(t, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 1, Z: 0}, Speed: moveSpeed, Yaw: moveYaw})

	// THE GATE (spec S20.1): B observes A's NEW position and yaw in a
	// Snapshot over UDP within the broadcast cadence (bounded 5 s).
	moved := requireUDP[*mmov1.Snapshot](t, b, "snapshot showing alice moved", func(sn *mmov1.Snapshot) bool {
		e := findEntity(sn.Entities, "alice")
		return e != nil && e.Pos != nil && e.Pos.X > arA.SpawnPos.X+0.1 && e.Yaw == moveYaw
	})
	alice1 := findEntity(moved.Entities, "alice")
	if alice1.Pos.X <= arA.SpawnPos.X {
		t.Errorf("bob: alice did not advance: pos = %v, spawn X = %v", alice1.Pos, arA.SpawnPos.X)
	}
	if alice1.Yaw != moveYaw {
		t.Errorf("bob: alice yaw = %v, want %v", alice1.Yaw, moveYaw)
	}
	if moved.Seq <= first.Seq {
		t.Errorf("bob: snapshot seq not monotonic: first=%d moved=%d", first.Seq, moved.Seq)
	}
	t.Logf("GATE PASSED: bob saw alice at (%.2f, %.2f) yaw %.2f rad (snapshot seq %d), spawn was (%v, %v)",
		alice1.Pos.X, alice1.Pos.Z, alice1.Yaw, moved.Seq, arA.SpawnPos.X, arA.SpawnPos.Z)
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
	arA, _ := a.enterWorld(t, "alice")
	b := newClient(t, reg, tcpAddr, udpAddr, "bob")
	arB, _ := b.enterWorld(t, "bob")

	// Lifecycle + interest over TCP: alice is notified that bob spawned.
	requireTCP[*mmov1.SpawnEntity](t, a, "SpawnEntity bob", func(sp *mmov1.SpawnEntity) bool {
		return sp.EntityId == "bob"
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
