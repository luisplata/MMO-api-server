package session

// Session lifecycle tests over a mocked Transport (spec R10–R12).
//
// Coverage: S10.1 happy path (Hello → ServerInfo → AuthRequest →
// AuthResponse → EnterWorld → WorldSnapshot → in-world); S10.2
// out-of-order messages rejected with the session closed; S11.2 close
// from any state, idempotent; S12.1 valid credentials yield
// playerId/spawnPos/udpToken; S12.2 invalid credentials yield a not-ok
// AuthResponse and a closed session; S5.2 version mismatch yields
// VersionMismatch and closes; plus the D5 ack policy (acknowledge
// receipt, no retransmit) and the handshake timeout failure path.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// testVersion is the client protocol version used in test frames; the
// test server supports [1,9] so it is always accepted.
const testVersion = 7

// mockTransport records every frame the session sends and whether Close
// was called — the session is driven exclusively through this seam.
type mockTransport struct {
	tcpFrames [][]byte
	udpFrames [][]byte
	closed    bool
	closeErr  error
}

func (m *mockTransport) SendTCP(frame []byte) error {
	m.tcpFrames = append(m.tcpFrames, append([]byte(nil), frame...))
	return nil
}

func (m *mockTransport) SendUDP(frame []byte) error {
	m.udpFrames = append(m.udpFrames, append([]byte(nil), frame...))
	return nil
}

func (m *mockTransport) Close() error {
	m.closed = true
	return m.closeErr
}

var _ network.Transport = (*mockTransport)(nil)

// fakeClock is an injectable time source so handshake-timeout tests are
// deterministic (no real sleeping).
type fakeClock struct{ cur time.Time }

func (f *fakeClock) Now() time.Time { return f.cur }

func (f *fakeClock) Advance(d time.Duration) { f.cur = f.cur.Add(d) }

// fakeAuth accepts exactly the credentials in ok and always assigns the
// fixed player identity + spawn on success.
type fakeAuth struct {
	ok       map[string]string
	playerID string
	spawn    mmov1.Vec2
}

func (f *fakeAuth) Authenticate(username, password string) (string, *mmov1.Vec2, error) {
	if want, ok := f.ok[username]; ok && want == password {
		return f.playerID, &mmov1.Vec2{X: f.spawn.X, Z: f.spawn.Z}, nil
	}
	return "", nil, fmt.Errorf("invalid credentials for %q", username)
}

// fixedToken returns a TokenGen that always issues the same token, for
// deterministic bind tests.
func fixedToken(tok []byte) func() []byte {
	return func() []byte { return append([]byte(nil), tok...) }
}

func newTestSession(t *testing.T, mut func(*Config)) (*Session, *mockTransport, *fakeClock) {
	t.Helper()
	tr := &mockTransport{}
	clock := &fakeClock{cur: time.UnixMilli(1_700_000_000_000)}
	cfg := Config{
		MinProtoVer: 1,
		MaxProtoVer: 9,
		TickRate:    20,
		Auth: &fakeAuth{
			ok:       map[string]string{"alice": "pw"},
			playerID: "p1",
			spawn:    mmov1.Vec2{X: 1.5, Z: -2.5},
		},
		Now: clock.Now,
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := NewSession(protocol.NewWorldRegistry(), tr, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return s, tr, clock
}

// clientFrame encodes a message the way the Unity client would: as a
// full envelope frame over the negotiated wire version.
func clientFrame(t *testing.T, reg *protocol.Registry, msg proto.Message, flags uint8, seq uint32) []byte {
	t.Helper()
	frame, err := reg.EncodeMessage(msg, protocol.Envelope{Version: testVersion, Flags: flags, Seq: seq})
	if err != nil {
		t.Fatalf("clientFrame(%T): %v", msg, err)
	}
	return frame
}

// rawFrame encodes a message with an explicit envelope version, for
// version-mismatch tests that must send out-of-range frames.
func rawFrame(t *testing.T, reg *protocol.Registry, msg proto.Message, version uint16) []byte {
	t.Helper()
	frame, err := reg.EncodeMessage(msg, protocol.Envelope{Version: version, Flags: 0, Seq: 0})
	if err != nil {
		t.Fatalf("rawFrame(%T): %v", msg, err)
	}
	return frame
}

// sentFrame decodes the i-th TCP frame the session sent into its
// envelope and typed message.
func sentFrame(t *testing.T, reg *protocol.Registry, tr *mockTransport, i int) (*protocol.Envelope, proto.Message) {
	t.Helper()
	if i >= len(tr.tcpFrames) {
		t.Fatalf("session sent %d TCP frames, want frame %d", len(tr.tcpFrames), i)
	}
	env, err := protocol.DecodeEnvelope(tr.tcpFrames[i])
	if err != nil {
		t.Fatalf("decode sent frame %d: %v", i, err)
	}
	msg, err := reg.DecodeMessage(*env)
	if err != nil {
		t.Fatalf("decode sent message %d: %v", i, err)
	}
	return env, msg
}

// --- handshake progression helpers -------------------------------------

func sendHello(t *testing.T, s *Session, reg *protocol.Registry) {
	t.Helper()
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.Hello{ProtoVer: testVersion}, 0, 0)); err != nil {
		t.Fatalf("Hello: %v", err)
	}
}

func sendAuth(t *testing.T, s *Session, reg *protocol.Registry) {
	t.Helper()
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.AuthRequest{Username: "alice", Password: "pw"}, 0, 0)); err != nil {
		t.Fatalf("AuthRequest: %v", err)
	}
}

func sendEnterWorld(t *testing.T, s *Session, reg *protocol.Registry) {
	t.Helper()
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.EnterWorld{}, 0, 0)); err != nil {
		t.Fatalf("EnterWorld: %v", err)
	}
}

func inWorld(t *testing.T, s *Session, reg *protocol.Registry) {
	t.Helper()
	sendHello(t, s, reg)
	sendAuth(t, s, reg)
	sendEnterWorld(t, s, reg)
}

// --- tests ---------------------------------------------------------------

// TestHandshakeHappyPath covers S10.1 end to end: the session advances
// connecting → handshaking → authenticating → entering → in-world and
// emits ServerInfo (protoVer/tickRate/serverTime), AuthResponse
// (ok/playerId/spawnPos/udpToken) and a needs-ack WorldSnapshot — all
// over the mocked transport, which must never be closed.
func TestHandshakeHappyPath(t *testing.T) {
	s, tr, clock := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()

	if s.State() != StateConnecting {
		t.Fatalf("fresh session state = %s, want connecting", s.State())
	}

	// 1. Hello → ServerInfo (S10.1 first leg).
	sendHello(t, s, reg)
	if s.State() != StateHandshaking {
		t.Errorf("after Hello state = %s, want handshaking", s.State())
	}
	env, msg := sentFrame(t, reg, tr, 0)
	si, ok := msg.(*mmov1.ServerInfo)
	if !ok {
		t.Fatalf("frame 0 = %T, want ServerInfo", msg)
	}
	if si.ProtoVer != testVersion {
		t.Errorf("ServerInfo.ProtoVer = %d, want negotiated %d", si.ProtoVer, testVersion)
	}
	if si.TickRate != 20 {
		t.Errorf("ServerInfo.TickRate = %d, want 20", si.TickRate)
	}
	if si.ServerTime != clock.cur.UnixMilli() {
		t.Errorf("ServerInfo.ServerTime = %d, want %d (server clock)", si.ServerTime, clock.cur.UnixMilli())
	}
	if env.Version != testVersion {
		t.Errorf("ServerInfo envelope version = %d, want %d", env.Version, testVersion)
	}
	if env.Flags&protocol.FlagNeedsAck != 0 {
		t.Errorf("ServerInfo must not request an ack (reliable channel)")
	}

	// 2. AuthRequest → AuthResponse ok (S10.1 second leg, S12.1).
	sendAuth(t, s, reg)
	if s.State() != StateEntering {
		t.Errorf("after auth state = %s, want entering", s.State())
	}
	_, msg = sentFrame(t, reg, tr, 1)
	ar, ok := msg.(*mmov1.AuthResponse)
	if !ok {
		t.Fatalf("frame 1 = %T, want AuthResponse", msg)
	}
	if !ar.Ok {
		t.Fatalf("AuthResponse.Ok = false, want true")
	}
	if ar.PlayerId != "p1" {
		t.Errorf("AuthResponse.PlayerId = %q, want %q", ar.PlayerId, "p1")
	}
	if ar.SpawnPos == nil || ar.SpawnPos.X != 1.5 || ar.SpawnPos.Z != -2.5 {
		t.Errorf("AuthResponse.SpawnPos = %v, want (1.5, -2.5)", ar.SpawnPos)
	}
	if len(ar.UdpToken) == 0 {
		t.Errorf("AuthResponse.UdpToken is empty, want a fresh token")
	}

	// 3. EnterWorld → WorldSnapshot, then steady state (S10.1 third leg).
	// The client marks EnterWorld needs-ack with its own seq; the server
	// must answer with the WorldSnapshot AND an Ack (D5: ack receipt).
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.EnterWorld{}, protocol.FlagNeedsAck, 10)); err != nil {
		t.Fatalf("EnterWorld: %v", err)
	}
	if s.State() != StateInWorld {
		t.Errorf("after EnterWorld state = %s, want in-world", s.State())
	}
	env, msg = sentFrame(t, reg, tr, 2)
	ws, ok := msg.(*mmov1.WorldSnapshot)
	if !ok {
		t.Fatalf("frame 2 = %T, want WorldSnapshot", msg)
	}
	_ = ws
	if env.Flags&protocol.FlagNeedsAck == 0 {
		t.Errorf("WorldSnapshot must be needs-ack (seq/ack for authority)")
	}
	if env.Seq != 1 {
		t.Errorf("WorldSnapshot seq = %d, want 1 (first needs-ack frame)", env.Seq)
	}
	// The needs-ack EnterWorld is acknowledged with the client's seq.
	env, msg = sentFrame(t, reg, tr, 3)
	ack, ok := msg.(*mmov1.Ack)
	if !ok {
		t.Fatalf("frame 3 = %T, want Ack for the needs-ack EnterWorld", msg)
	}
	if ack.Seq != 10 {
		t.Errorf("Ack.Seq = %d, want 10 (echo of the EnterWorld seq)", ack.Seq)
	}
	if env.Flags&protocol.FlagNeedsAck != 0 {
		t.Errorf("an Ack reply must not itself request an ack")
	}

	if tr.closed {
		t.Errorf("happy path must not close the transport")
	}
}

// TestNeedsAckGetsAckReply covers the D5 receipt acknowledgement during
// the handshake: a needs-ack Hello is answered with ServerInfo AND an
// Ack carrying the client's seq, and the session stays alive.
func TestNeedsAckGetsAckReply(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()

	err := s.HandleTCP(clientFrame(t, reg, &mmov1.Hello{ProtoVer: testVersion}, protocol.FlagNeedsAck, 5))
	if err != nil {
		t.Fatalf("HandleTCP(Hello needs-ack): %v", err)
	}
	if tr.closed {
		t.Fatalf("a needs-ack Hello must not close the session")
	}
	if s.State() != StateHandshaking {
		t.Errorf("state = %s, want handshaking", s.State())
	}
	_, msg := sentFrame(t, reg, tr, 0)
	if _, ok := msg.(*mmov1.ServerInfo); !ok {
		t.Fatalf("frame 0 = %T, want ServerInfo", msg)
	}
	env, msg := sentFrame(t, reg, tr, 1)
	ack, ok := msg.(*mmov1.Ack)
	if !ok {
		t.Fatalf("frame 1 = %T, want Ack", msg)
	}
	if ack.Seq != 5 {
		t.Errorf("Ack.Seq = %d, want 5 (echo of the received seq)", ack.Seq)
	}
	if env.Seq != 0 {
		t.Errorf("Ack reply must not itself request an ack (seq = %d, want 0)", env.Seq)
	}
}

// TestVersionMismatchAtEnvelope covers S5.2 for the envelope version:
// a frame whose envelope version is outside the supported range yields
// VersionMismatch with the supported range and the session closes.
func TestVersionMismatchAtEnvelope(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()

	err := s.HandleTCP(rawFrame(t, reg, &mmov1.Hello{ProtoVer: testVersion}, 99))
	if !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("err = %v, want ErrVersionMismatch", err)
	}
	if s.State() != StateClosed || !tr.closed {
		t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
	}
	env, msg := sentFrame(t, reg, tr, 0)
	vm, ok := msg.(*mmov1.VersionMismatch)
	if !ok {
		t.Fatalf("frame 0 = %T, want VersionMismatch", msg)
	}
	if vm.MinVer != 1 || vm.MaxVer != 9 {
		t.Errorf("VersionMismatch = [%d,%d], want [1,9]", vm.MinVer, vm.MaxVer)
	}
	if env.Type != 9 {
		t.Errorf("VersionMismatch envelope type = %d, want 9", env.Type)
	}
}

// TestVersionMismatchAtHello covers S5.2 for the client's announced
// version: a Hello whose ProtoVer is out of range — even over an
// in-range envelope — is answered with VersionMismatch and closes.
func TestVersionMismatchAtHello(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()

	err := s.HandleTCP(clientFrame(t, reg, &mmov1.Hello{ProtoVer: 99}, 0, 0))
	if !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("err = %v, want ErrVersionMismatch", err)
	}
	if s.State() != StateClosed || !tr.closed {
		t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
	}
	_, msg := sentFrame(t, reg, tr, 0)
	vm, ok := msg.(*mmov1.VersionMismatch)
	if !ok {
		t.Fatalf("frame 0 = %T, want VersionMismatch", msg)
	}
	if vm.MinVer != 1 || vm.MaxVer != 9 {
		t.Errorf("VersionMismatch = [%d,%d], want [1,9]", vm.MinVer, vm.MaxVer)
	}
}

// TestOutOfOrderMessageCloses covers S10.2: a message that is invalid
// for the current state is rejected with a protocol error and the
// session closes.
func TestOutOfOrderMessageCloses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Session, reg *protocol.Registry)
		msg   proto.Message
	}{
		{"EnterWorld before Hello", func(t *testing.T, s *Session, reg *protocol.Registry) {}, &mmov1.EnterWorld{}},
		{"AuthRequest before Hello", func(t *testing.T, s *Session, reg *protocol.Registry) {}, &mmov1.AuthRequest{Username: "alice", Password: "pw"}},
		{"EnterWorld before auth", sendHello, &mmov1.EnterWorld{}},
		{"second Hello", sendHello, &mmov1.Hello{ProtoVer: testVersion}},
		{"AuthRequest after auth", func(t *testing.T, s *Session, reg *protocol.Registry) { sendHello(t, s, reg); sendAuth(t, s, reg) }, &mmov1.AuthRequest{Username: "alice", Password: "pw"}},
		{"Hello in in-world", inWorld, &mmov1.Hello{ProtoVer: testVersion}},
		{"EnterWorld in in-world", inWorld, &mmov1.EnterWorld{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tr, _ := newTestSession(t, nil)
			reg := protocol.NewWorldRegistry()
			tc.setup(t, s, reg)

			err := s.HandleTCP(clientFrame(t, reg, tc.msg, 0, 0))
			if !errors.Is(err, ErrIllegalMessage) {
				t.Errorf("err = %v, want ErrIllegalMessage", err)
			}
			if s.State() != StateClosed || !tr.closed {
				t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
			}
		})
	}
}

// TestBadFrameCloses covers robustness: an undecodable frame (truncated
// envelope, unregistered type) is a protocol error that closes the
// session rather than being ignored.
func TestBadFrameCloses(t *testing.T) {
	reg := protocol.NewWorldRegistry()

	t.Run("truncated envelope", func(t *testing.T) {
		s, tr, _ := newTestSession(t, nil)
		// 3 bytes cannot form the 11-byte header.
		err := s.HandleTCP([]byte{0x4D, 0x4D, 0x00})
		if !errors.Is(err, ErrProtocol) {
			t.Errorf("err = %v, want ErrProtocol", err)
		}
		if s.State() != StateClosed || !tr.closed {
			t.Errorf("session must be closed after a bad frame (state=%s closed=%v)", s.State(), tr.closed)
		}
	})

	t.Run("unknown type id", func(t *testing.T) {
		s, tr, _ := newTestSession(t, nil)
		// A well-formed envelope for type 99, which the registry does not
		// know (only 1–10 are registered).
		err := s.HandleTCP(rawFrameType(t, reg, 99))
		if !errors.Is(err, ErrProtocol) {
			t.Errorf("err = %v, want ErrProtocol", err)
		}
		if s.State() != StateClosed || !tr.closed {
			t.Errorf("session must close on unknown type (state=%s closed=%v)", s.State(), tr.closed)
		}
	})
}

// rawFrameType builds a well-formed envelope frame for a type id without
// needing a registered message (payload can be empty — the registry
// rejects the type before unmarshal).
func rawFrameType(t *testing.T, reg *protocol.Registry, typeID uint16) []byte {
	t.Helper()
	// EncodeMessage refuses unknown types, so hand-build the frame.
	env := protocol.Envelope{Version: testVersion, Type: typeID, Flags: 0, Seq: 0}
	frame, err := env.Encode()
	if err != nil {
		t.Fatalf("encode frame type %d: %v", typeID, err)
	}
	return frame
}

// TestAuthFailureCloses covers S12.2: invalid credentials are answered
// with a not-ok AuthResponse carrying an error message, then the session
// closes.
func TestAuthFailureCloses(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	sendHello(t, s, reg)

	err := s.HandleTCP(clientFrame(t, reg, &mmov1.AuthRequest{Username: "alice", Password: "wrong"}, 0, 0))
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed", err)
	}
	if s.State() != StateClosed || !tr.closed {
		t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
	}
	_, msg := sentFrame(t, reg, tr, 1)
	ar, ok := msg.(*mmov1.AuthResponse)
	if !ok {
		t.Fatalf("frame 1 = %T, want AuthResponse", msg)
	}
	if ar.Ok {
		t.Errorf("AuthResponse.Ok = true, want false")
	}
	if ar.ErrorMessage == "" {
		t.Errorf("AuthResponse.ErrorMessage is empty, want a reason")
	}
	if ar.UdpToken != nil {
		t.Errorf("failed auth must not issue a UDP token")
	}
}

// TestCloseFromAnyState covers S11.2: Close works from every live state,
// releases the transport, and is idempotent; afterwards the session
// rejects further frames with ErrClosed.
func TestCloseFromAnyState(t *testing.T) {
	setups := []struct {
		name  string
		setup func(t *testing.T, s *Session, reg *protocol.Registry)
	}{
		{"connecting", func(t *testing.T, s *Session, reg *protocol.Registry) {}},
		{"handshaking", sendHello},
		{"entering", func(t *testing.T, s *Session, reg *protocol.Registry) { sendHello(t, s, reg); sendAuth(t, s, reg) }},
		{"in-world", inWorld},
	}
	for _, tc := range setups {
		t.Run(tc.name, func(t *testing.T) {
			s, tr, _ := newTestSession(t, nil)
			reg := protocol.NewWorldRegistry()
			tc.setup(t, s, reg)

			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if s.State() != StateClosed {
				t.Errorf("state = %s, want closed", s.State())
			}
			if !tr.closed {
				t.Errorf("transport must be closed")
			}

			// Idempotent: a second Close is a no-op.
			if err := s.Close(); err != nil {
				t.Errorf("second Close: %v, want nil", err)
			}

			// The session rejects frames after close.
			err := s.HandleTCP(clientFrame(t, reg, &mmov1.Hello{ProtoVer: testVersion}, 0, 0))
			if !errors.Is(err, ErrClosed) {
				t.Errorf("HandleTCP after Close err = %v, want ErrClosed", err)
			}
		})
	}
}

// TestClosePropagatesTransportError: a transport that fails to close is
// still marked closed on the session side, but the error surfaces.
func TestClosePropagatesTransportError(t *testing.T) {
	s, tr, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)
	want := errors.New("teardown failed")
	tr.closeErr = want

	if err := s.Close(); !errors.Is(err, want) {
		t.Errorf("Close err = %v, want %v", err, want)
	}
	if s.State() != StateClosed {
		t.Errorf("state = %s, want closed even when transport Close fails", s.State())
	}
}

// TestHandshakeTimeout covers the timeout failure path: a session that
// resumes the handshake after its deadline expires is closed with
// ErrHandshakeTimeout and nothing further is sent.
func TestHandshakeTimeout(t *testing.T) {
	s, tr, clock := newTestSession(t, func(c *Config) { c.HandshakeTimeout = 10 * time.Millisecond })
	reg := protocol.NewWorldRegistry()

	// Within the deadline the handshake still works.
	clock.Advance(5 * time.Millisecond)
	sendHello(t, s, reg)
	if s.State() != StateHandshaking {
		t.Fatalf("within deadline state = %s, want handshaking", s.State())
	}

	// Past the deadline the next handshake step is refused.
	clock.Advance(6 * time.Millisecond)
	err := s.HandleTCP(clientFrame(t, reg, &mmov1.AuthRequest{Username: "alice", Password: "pw"}, 0, 0))
	if !errors.Is(err, ErrHandshakeTimeout) {
		t.Errorf("err = %v, want ErrHandshakeTimeout", err)
	}
	if s.State() != StateClosed || !tr.closed {
		t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
	}
}

// TestNoTimeoutAfterInWorld: the handshake deadline is irrelevant once
// the session reaches steady state.
func TestNoTimeoutAfterInWorld(t *testing.T) {
	s, tr, clock := newTestSession(t, func(c *Config) { c.HandshakeTimeout = 10 * time.Millisecond })
	reg := protocol.NewWorldRegistry()
	inWorld(t, s, reg)

	clock.Advance(time.Hour)
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.Ack{Seq: 1}, 0, 0)); err != nil {
		t.Fatalf("HandleTCP in steady state after deadline: %v, want nil", err)
	}
	if tr.closed {
		t.Errorf("steady-state session must not be closed by the handshake deadline")
	}
}

// TestNewSessionValidation pins the constructor contract: nil registry,
// nil transport or missing authenticator are construction errors.
func TestNewSessionValidation(t *testing.T) {
	reg := protocol.NewWorldRegistry()
	tr := &mockTransport{}
	auth := &fakeAuth{ok: map[string]string{}}
	cfg := func() Config {
		return Config{MinProtoVer: 1, MaxProtoVer: 9, TickRate: 20, Auth: auth, Now: time.Now}
	}

	if _, err := NewSession(nil, tr, cfg()); err == nil {
		t.Errorf("nil registry must be rejected")
	}
	if _, err := NewSession(reg, nil, cfg()); err == nil {
		t.Errorf("nil transport must be rejected")
	}
	bad := cfg()
	bad.Auth = nil
	if _, err := NewSession(reg, tr, bad); err == nil {
		t.Errorf("missing authenticator must be rejected")
	}
	bad2 := cfg()
	bad2.MinProtoVer = 10
	bad2.MaxProtoVer = 5
	if _, err := NewSession(reg, tr, bad2); err == nil {
		t.Errorf("inverted version range must be rejected")
	}
}
