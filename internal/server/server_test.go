package server

// Shared test scaffolding for the wiring layer plus the two behavior
// tests that need a full handshake: New validation and the enter-world
// flow (register in the sim at the spawn position, then send the REAL
// WorldSnapshot over TCP). Everything runs in-memory — mock transports,
// fake PacketConn, net.Pipe — no real listeners are opened.

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/session"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// testVersion is the protocol version the fake clients negotiate; the
// test server supports [1,9] so it is always accepted.
const testVersion = 7

// fakeAddr is a minimal net.Addr for binding and routing tests.
type fakeAddr string

func (a fakeAddr) Network() string { return "udp" }
func (a fakeAddr) String() string  { return string(a) }

// fakePacketConn records every WriteTo datagram — SendSnapshot is
// verified against it without opening a UDP socket.
type fakePacketConn struct {
	mu     sync.Mutex
	writes []fakeWrite
}

type fakeWrite struct {
	b    []byte
	addr net.Addr
}

func (f *fakePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, fakeWrite{b: append([]byte(nil), b...), addr: addr})
	return len(b), nil
}

func (f *fakePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	return 0, nil, errors.New("fakePacketConn: ReadFrom not implemented")
}

func (f *fakePacketConn) Close() error { return nil }

func (f *fakePacketConn) LocalAddr() net.Addr { return fakeAddr("0.0.0.0:0") }

func (f *fakePacketConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakePacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakePacketConn) SetWriteDeadline(t time.Time) error { return nil }

var _ net.PacketConn = (*fakePacketConn)(nil)

// mockTransport is a pass-through Transport seam for sessions that never
// need to actually write (sink/UDP routing tests).
type mockTransport struct{ closed bool }

func (m *mockTransport) SendTCP([]byte) error { return nil }
func (m *mockTransport) SendUDP([]byte) error { return nil }
func (m *mockTransport) Close() error         { m.closed = true; return nil }

var _ network.Transport = (*mockTransport)(nil)

// clientFrame encodes a message the way the client would: a full
// envelope frame over the negotiated wire version.
func clientFrame(t *testing.T, reg *protocol.Registry, msg proto.Message, flags uint8, seq uint32) []byte {
	t.Helper()
	frame, err := reg.EncodeMessage(msg, protocol.Envelope{Version: testVersion, Flags: flags, Seq: seq})
	if err != nil {
		t.Fatalf("clientFrame(%T): %v", msg, err)
	}
	return frame
}

// newTestServer builds a server with a fake UDP conn and a real sim so
// sink/routing behavior is exercised for real. No sockets are opened.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv := &Server{
		reg:     protocol.NewWorldRegistry(),
		auth:    devAuthenticator{enabled: true, spawn: game.Vec2{X: 1, Z: 2}},
		udp:     &fakePacketConn{},
		players: make(map[string]*player),
		simOps:  make(chan simOp),
	}
	sim, err := game.NewSimulation(game.SimulationConfig{Sink: srv})
	if err != nil {
		t.Fatalf("NewSimulation: %v", err)
	}
	srv.sim = sim
	return srv
}

// newInWorldSession drives a session through the full handshake over a
// mock transport (spec S10.1 happy path) so tests start from steady
// state. The dev authenticator returns username as the player id.
func newInWorldSession(t *testing.T, reg *protocol.Registry, username string) *session.Session {
	t.Helper()
	sess, err := session.NewSession(reg, &mockTransport{}, session.Config{
		MinProtoVer: 1,
		MaxProtoVer: 9,
		TickRate:    game.TickRate,
		Auth:        devAuthenticator{enabled: true, spawn: game.Vec2{}},
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	steps := []proto.Message{
		&mmov1.Hello{ProtoVer: testVersion},
		&mmov1.AuthRequest{Username: username, Password: "pw"},
		&mmov1.EnterWorld{},
	}
	for _, msg := range steps {
		if err := sess.HandleTCP(clientFrame(t, reg, msg, 0, 0)); err != nil {
			t.Fatalf("HandleTCP(%T): %v", msg, err)
		}
	}
	if sess.State() != session.StateInWorld {
		t.Fatalf("state = %s, want in-world", sess.State())
	}
	return sess
}

// addTestPlayer registers a player in the server map (as enterWorld
// does) with a mock transport and no TCP conn.
func addTestPlayer(t *testing.T, srv *Server, id string, sess *session.Session) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.players[id] = &player{sess: sess, tr: &mockTransport{}}
}

// TestNewServerValidation pins the constructor contract: empty listen
// addresses, a tick rate that would desync the fixed-dt sim, and an
// invalid protocol version range are all rejected.
func TestNewServerValidation(t *testing.T) {
	valid := Config{
		TCPAddr: ":8000", UDPAddr: ":8001",
		TickRate:    game.TickRate,
		DevAuth:     true,
		MinProtoVer: 1, MaxProtoVer: 1,
	}
	if _, err := New(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"empty tcp addr", func(c *Config) { c.TCPAddr = "" }},
		{"empty udp addr", func(c *Config) { c.UDPAddr = "" }},
		{"non-20 tick rate", func(c *Config) { c.TickRate = 10 }},
		{"inverted version range", func(c *Config) { c.MinProtoVer = 5; c.MaxProtoVer = 1 }},
		{"zero version range", func(c *Config) { c.MinProtoVer = 0; c.MaxProtoVer = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Errorf("New(%+v) must fail", cfg)
			}
		})
	}
}

// TestEnterWorldRegistersAndSendsRealSnapshot covers design step 4: when
// a session reaches in-world, the server registers the player in the sim
// at its spawn position and sends the REAL WorldSnapshot (the session's
// handshake WorldSnapshot is empty; the real one follows).
func TestEnterWorldRegistersAndSendsRealSnapshot(t *testing.T) {
	srv := newTestServer(t)
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	// net.Pipe is synchronous: a reader goroutine drains the client end
	// so the session's handshake replies and the real WorldSnapshot
	// writes never block.
	frames := startFrameReader(t, clientConn)

	sess, err := session.NewSession(srv.reg, &tcpConnTransport{conn: serverConn}, session.Config{
		MinProtoVer: 1,
		MaxProtoVer: 9,
		TickRate:    game.TickRate,
		Auth:        srv.auth,
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drive the handshake; each reply is drained by the reader.
	for _, msg := range []proto.Message{
		&mmov1.Hello{ProtoVer: testVersion},
		&mmov1.AuthRequest{Username: "alice", Password: "pw"},
		&mmov1.EnterWorld{},
	} {
		if err := sess.HandleTCP(clientFrame(t, srv.reg, msg, 0, 0)); err != nil {
			t.Fatalf("HandleTCP(%T): %v", msg, err)
		}
	}
	// Consume ServerInfo, AuthResponse and the empty handshake
	// WorldSnapshot.
	for i := 0; i < 3; i++ {
		nextFrame(t, frames)
	}

	// The sim-owner goroutine consumes simOps so enterWorld's register
	// op can complete. The wake channel never fires.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.runSim(ctx, make(chan time.Time))

	if err := srv.enterWorld(ctx, serverConn, sess, &tcpConnTransport{conn: serverConn}); err != nil {
		t.Fatalf("enterWorld: %v", err)
	}

	// Registered in the sim at the dev spawn position {1, 2}.
	e, ok := srv.sim.Entity("alice")
	if !ok {
		t.Fatalf("alice not registered in the simulation")
	}
	if e.Pos.X != 1 || e.Pos.Z != 2 {
		t.Errorf("alice pos = %v, want spawn (1, 2)", e.Pos)
	}
	if !srv.isRegistered("alice") {
		t.Errorf("alice missing from the server player map")
	}

	// The client receives the REAL WorldSnapshot (non-empty, encoded
	// with the negotiated version).
	env, msg := decodeFrame(t, srv, nextFrame(t, frames))
	if env.Version != testVersion {
		t.Errorf("WorldSnapshot envelope version = %d, want %d", env.Version, testVersion)
	}
	ws, ok := msg.(*mmov1.WorldSnapshot)
	if !ok {
		t.Fatalf("frame carries %T, want *mmov1.WorldSnapshot", msg)
	}
	if len(ws.Entities) != 1 || ws.Entities[0].Id != "alice" {
		t.Errorf("real WorldSnapshot = %v, want exactly alice", ws.Entities)
	}
	if ws.Entities[0].Pos == nil || ws.Entities[0].Pos.X != 1 || ws.Entities[0].Pos.Z != 2 {
		t.Errorf("WorldSnapshot alice pos = %v, want (1, 2)", ws.Entities[0].Pos)
	}
}
