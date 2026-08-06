package session

// Seven-step handshake over the Transport seam (spec R10).
//
// A session progresses:
//
//	Hello → ServerInfo (protoVer, tickRate, serverTime)
//	      → AuthRequest → AuthResponse (playerId, spawnPos, udpToken)
//	      → EnterWorld → WorldSnapshot → UDP bind → steady state
//
// Every inbound TCP frame is a full envelope frame (header + payload);
// the session decodes it with the registry, validates it against the
// current state (spec S10.2 — out-of-order messages are rejected and the
// session closes), and only then advances the state machine.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// Errors returned by the session layer.
var (
	// ErrClosed is returned when a frame is handled after Close.
	ErrClosed = errors.New("session: closed")
	// ErrProtocol wraps undecodable frames and registry failures.
	ErrProtocol = errors.New("session: protocol error")
	// ErrIllegalMessage wraps a message that is invalid for the current
	// state (spec S10.2).
	ErrIllegalMessage = errors.New("session: message not allowed in current state")
	// ErrVersionMismatch wraps a client version outside the supported
	// range (spec S5.2); a VersionMismatch has already been sent.
	ErrVersionMismatch = errors.New("session: protocol version not supported")
	// ErrAuthFailed wraps invalid credentials (spec S12.2).
	ErrAuthFailed = errors.New("session: authentication failed")
	// ErrHandshakeTimeout closes a session that resumes the handshake
	// after its deadline has passed.
	ErrHandshakeTimeout = errors.New("session: handshake timeout")
)

// Config carries the tunables the session layer needs to run the
// connection lifecycle.
type Config struct {
	// MinProtoVer / MaxProtoVer is the protocol version range the server
	// supports (spec R5). The envelope version and the client's Hello
	// ProtoVer must both fall inside it.
	MinProtoVer int32
	MaxProtoVer int32

	// TickRate is advertised in ServerInfo (spec R10).
	TickRate int32

	// Auth validates credentials (spec R12). Required.
	Auth Authenticator

	// Now supplies the current time; defaults to time.Now. Injected so
	// handshake-timeout tests are deterministic.
	Now func() time.Time

	// TokenGen issues the fresh UDP bind token for each AuthResponse;
	// defaults to 16 random bytes (spec R12, R13).
	TokenGen func() []byte

	// HandshakeTimeout bounds the whole Hello→EnterWorld sequence from
	// connection start. On expiry the next handshake frame is refused
	// with ErrHandshakeTimeout and the session closes. Zero disables.
	// NOTE: a wall-clock timer that fires with no inbound traffic is
	// server wiring (PR4) — the session enforces the deadline when a
	// frame arrives.
	HandshakeTimeout time.Duration
}

// Authenticator validates client credentials (spec R12). The concrete
// backend is injected; the session only consumes the verdict.
type Authenticator interface {
	// Authenticate returns the player id and spawn position for valid
	// credentials, or an error for invalid ones. The spawn pointer is
	// copied into AuthResponse.SpawnPos.
	Authenticate(username, password string) (playerID string, spawn *mmov1.Vec2, err error)
}

// Session drives one client connection through the lifecycle. It owns
// the state machine and communicates exclusively through the Transport
// seam, so tests mock the transport instead of spinning sockets.
type Session struct {
	reg   *protocol.Registry
	tr    network.Transport
	cfg   Config
	state State

	// protoVer is the version negotiated at Hello (validated in range);
	// outbound envelopes carry it as the wire version.
	protoVer int32

	// seq is the per-session monotonic counter for needs-ack frames
	// (spec R2/R9 — envelope seq MUST be per-session monotonic).
	seq uint32

	// udpToken is the token issued at AuthResponse; the first UDP packet
	// must carry it to bind (spec R13).
	udpToken []byte
	udpBound bool
	udpPeer  net.Addr

	// playerID / spawnPos are the authenticated identity (spec R12),
	// populated by a successful AuthRequest. The wiring layer (PR4b)
	// reads them to register the player in the simulation and to send
	// the enter-world WorldSnapshot from the spawn position.
	playerID string
	spawnPos *mmov1.Vec2

	// handshakeDeadline is now()+HandshakeTimeout at construction (or
	// zero when timeouts are disabled).
	handshakeDeadline time.Time
}

// NewSession constructs a session in the connecting state. A nil
// registry, nil transport or missing authenticator are construction
// errors; a zero/absent version range is rejected.
func NewSession(reg *protocol.Registry, tr network.Transport, cfg Config) (*Session, error) {
	if reg == nil {
		return nil, errors.New("session: nil registry")
	}
	if tr == nil {
		return nil, errors.New("session: nil transport")
	}
	if cfg.Auth == nil {
		return nil, errors.New("session: Auth authenticator is required")
	}
	if cfg.MinProtoVer <= 0 || cfg.MaxProtoVer < cfg.MinProtoVer {
		return nil, errors.New("session: invalid protocol version range")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TokenGen == nil {
		cfg.TokenGen = defaultTokenGen
	}
	s := &Session{
		reg:   reg,
		tr:    tr,
		cfg:   cfg,
		state: StateConnecting,
	}
	if cfg.HandshakeTimeout > 0 {
		s.handshakeDeadline = cfg.Now().Add(cfg.HandshakeTimeout)
	}
	return s, nil
}

// defaultTokenGen issues 16 random bytes for the UDP bind token.
func defaultTokenGen() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic for token security; fail
		// loudly rather than issuing a guessable token.
		panic(fmt.Sprintf("session: crypto/rand failed: %v", err))
	}
	return b
}

// State reports the current lifecycle state.
func (s *Session) State() State { return s.state }

// UDPToken returns the bind token issued at AuthResponse, or nil before
// authentication succeeds.
func (s *Session) UDPToken() []byte { return s.udpToken }

// UDPBound reports whether a UDP peer has been bound (spec R13).
func (s *Session) UDPBound() bool { return s.udpBound }

// WireVersion returns the negotiated protocol version for encoding
// outbound frames. Before negotiation (no Hello yet) it reports the
// server's max supported version — the same fallback the handshake
// uses for VersionMismatch frames.
func (s *Session) WireVersion() uint16 { return s.wireVersion() }

// PlayerID returns the authenticated player id, or "" before a
// successful AuthRequest (spec R12).
func (s *Session) PlayerID() string { return s.playerID }

// SpawnPos returns the authenticated player's spawn position, or nil
// before a successful AuthRequest. The wiring layer registers the
// player at this position in the simulation (design D3).
func (s *Session) SpawnPos() *mmov1.Vec2 { return s.spawnPos }

// HandleTCP processes one inbound TCP frame and drives the state
// machine. On a protocol violation (undecodable frame, out-of-order
// message, version mismatch, auth failure, expired deadline) the session
// closes itself and returns a descriptive error.
func (s *Session) HandleTCP(frame []byte) error {
	if s.state.IsTerminal() {
		return ErrClosed
	}
	if s.handshakeExpired() {
		return s.closeWith(ErrHandshakeTimeout)
	}
	env, err := protocol.DecodeEnvelope(frame)
	if err != nil {
		return s.closeWith(fmt.Errorf("%w: %v", ErrProtocol, err))
	}
	if err := s.checkVersion(env.Version); err != nil {
		return err
	}
	msg, err := s.reg.DecodeMessage(*env)
	if err != nil {
		return s.closeWith(fmt.Errorf("%w: %v", ErrProtocol, err))
	}
	if err := s.dispatch(env, msg); err != nil {
		return err
	}
	// D5 ack policy: acknowledge receipt of needs-ack frames with Ack
	// (no retransmission in v1 — inputs are idempotent and cheap, and
	// the reliable channel already covers the handshake). An Ack is
	// never answered by another Ack.
	if _, isAck := msg.(*mmov1.Ack); !isAck && env.Flags&protocol.FlagNeedsAck != 0 {
		return s.sendTCP(&mmov1.Ack{Seq: env.Seq}, false)
	}
	return nil
}

// dispatch routes a decoded message to its handler. Unknown message
// types are a protocol violation (spec S6.2).
func (s *Session) dispatch(env *protocol.Envelope, msg proto.Message) error {
	switch m := msg.(type) {
	case *mmov1.Hello:
		return s.handleHello(env, m)
	case *mmov1.AuthRequest:
		return s.handleAuth(m)
	case *mmov1.EnterWorld:
		return s.handleEnterWorld()
	case *mmov1.Ack:
		// An Ack is bookkeeping: with no retransmission in v1 there is
		// nothing to resend, so we simply accept it in any state. It can
		// never be an out-of-order violation.
		return nil
	default:
		return s.protocolError("message %s is not part of the lifecycle", msg.ProtoReflect().Descriptor().Name())
	}
}

// handleHello negotiates the protocol version (spec R5, S5.1/S5.2):
// connecting + Hello → handshaking, answered with ServerInfo.
func (s *Session) handleHello(env *protocol.Envelope, hello *mmov1.Hello) error {
	if s.state != StateConnecting {
		return s.protocolError("Hello received in %s", s.state)
	}
	// The envelope version already passed the range check; the client's
	// announced ProtoVer must be in range too (S5.2).
	if hello.ProtoVer < s.cfg.MinProtoVer || hello.ProtoVer > s.cfg.MaxProtoVer {
		_ = s.sendTCP(&mmov1.VersionMismatch{MinVer: s.cfg.MinProtoVer, MaxVer: s.cfg.MaxProtoVer}, false)
		return s.closeWith(fmt.Errorf("%w: client announced %d, supported [%d, %d]",
			ErrVersionMismatch, hello.ProtoVer, s.cfg.MinProtoVer, s.cfg.MaxProtoVer))
	}
	if err := s.moveTo(StateHandshaking); err != nil {
		return err
	}
	s.protoVer = hello.ProtoVer
	return s.sendTCP(&mmov1.ServerInfo{
		ProtoVer:   hello.ProtoVer,
		TickRate:   s.cfg.TickRate,
		ServerTime: s.cfg.Now().UnixMilli(),
	}, false)
}

// handleAuth authenticates the client (spec R12): handshaking +
// AuthRequest → authenticating, then → entering on success with an ok
// AuthResponse carrying playerId/spawnPos/udpToken (S12.1); on failure
// a not-ok AuthResponse is sent and the session closes (S12.2).
func (s *Session) handleAuth(req *mmov1.AuthRequest) error {
	if s.state != StateHandshaking {
		return s.protocolError("AuthRequest received in %s", s.state)
	}
	if err := s.moveTo(StateAuthenticating); err != nil {
		return err
	}
	playerID, spawn, err := s.cfg.Auth.Authenticate(req.Username, req.Password)
	if err != nil {
		_ = s.sendTCP(&mmov1.AuthResponse{Ok: false, ErrorMessage: err.Error()}, false)
		return s.closeWith(fmt.Errorf("%w: %v", ErrAuthFailed, err))
	}
	if err := s.moveTo(StateEntering); err != nil {
		return err
	}
	s.udpToken = s.cfg.TokenGen()
	s.playerID = playerID
	s.spawnPos = spawn
	return s.sendTCP(&mmov1.AuthResponse{
		Ok:       true,
		PlayerId: playerID,
		SpawnPos: spawn,
		UdpToken: s.udpToken,
	}, false)
}

// handleEnterWorld answers the client's readiness (spec S10.1):
// entering + EnterWorld → in-world, acknowledged with a needs-ack
// WorldSnapshot carrying the next monotonic seq.
func (s *Session) handleEnterWorld() error {
	if s.state != StateEntering {
		return s.protocolError("EnterWorld received in %s", s.state)
	}
	if err := s.moveTo(StateInWorld); err != nil {
		return err
	}
	// v1 sends an empty WorldSnapshot: the session layer has no world
	// state of its own — PR4 (world sim) fills the entity list.
	return s.sendTCP(&mmov1.WorldSnapshot{}, true)
}

// moveTo validates a state-machine transition and applies it, closing
// the session with a protocol error if the machine rejects the move
// (spec S11.1).
func (s *Session) moveTo(dst State) error {
	next, err := s.state.Transition(dst)
	if err != nil {
		return s.protocolError("%s -> %s: %v", s.state, dst, err)
	}
	s.state = next
	return nil
}

// checkVersion validates the envelope version against the supported
// range (spec S5.2); on mismatch a VersionMismatch is sent and the
// session closes.
func (s *Session) checkVersion(v uint16) error {
	if v >= uint16(s.cfg.MinProtoVer) && v <= uint16(s.cfg.MaxProtoVer) {
		return nil
	}
	_ = s.sendTCP(&mmov1.VersionMismatch{MinVer: s.cfg.MinProtoVer, MaxVer: s.cfg.MaxProtoVer}, false)
	return s.closeWith(fmt.Errorf("%w: got %d, supported [%d, %d]",
		ErrVersionMismatch, v, s.cfg.MinProtoVer, s.cfg.MaxProtoVer))
}

// handshakeExpired reports whether the handshake deadline has passed
// for a session that has not yet reached steady state.
func (s *Session) handshakeExpired() bool {
	if s.cfg.HandshakeTimeout <= 0 || s.state >= StateInWorld {
		return false
	}
	return !s.cfg.Now().Before(s.handshakeDeadline)
}

// sendTCP encodes msg as a full envelope frame and writes it on the
// reliable channel. needsAck stamps a fresh monotonic seq and sets the
// needs-ack flag (spec R2/R9).
func (s *Session) sendTCP(msg proto.Message, needsAck bool) error {
	var flags uint8
	var seq uint32
	if needsAck {
		flags = protocol.FlagNeedsAck
		s.seq++
		seq = s.seq
	}
	frame, err := s.reg.EncodeMessage(msg, protocol.Envelope{
		Version: s.wireVersion(),
		Flags:   flags,
		Seq:     seq,
	})
	if err != nil {
		return err
	}
	return s.tr.SendTCP(frame)
}

// wireVersion returns the negotiated version, or the server's current
// major before negotiation (e.g. when sending VersionMismatch).
func (s *Session) wireVersion() uint16 {
	if s.protoVer != 0 {
		return uint16(s.protoVer)
	}
	return uint16(s.cfg.MaxProtoVer)
}

// protocolError closes the session and wraps the message in
// ErrIllegalMessage (spec S10.2).
func (s *Session) protocolError(format string, args ...any) error {
	return s.closeWith(fmt.Errorf("%w: %s", ErrIllegalMessage, fmt.Sprintf(format, args...)))
}

// closeWith closes the session and returns the error that caused it.
func (s *Session) closeWith(err error) error {
	_ = s.Close()
	return err
}

// Close terminates the session from any state (spec S11.2): it moves to
// closed through the machine, releases the UDP binding and tears down
// the transport. Idempotent; a transport Close error is propagated.
func (s *Session) Close() error {
	if s.state.IsTerminal() {
		return nil
	}
	s.state, _ = s.state.Transition(StateClosed) // legal from every live state
	s.udpBound = false
	s.udpPeer = nil
	s.udpToken = nil
	return s.tr.Close()
}
