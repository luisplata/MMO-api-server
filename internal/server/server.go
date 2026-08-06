// Package server wires the protocol, session, network and simulation
// layers into a running MMO server (PR4b wiring, design): a TCP accept
// loop drives sessions through the handshake, a UDP loop feeds
// MoveInputs, and a single sim-owner goroutine runs the deterministic
// 20 Hz tick, registers/removes players and fans out interest events.
//
// The package lives in internal/server (not cmd/server) so the headless
// E2E batch can import it; cmd/server/main.go is a thin flag parser.
package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/session"
)

// Config carries the server tunables (design PR4b).
type Config struct {
	// TCPAddr is the listener for the reliable channel (handshake,
	// auth, spawn/despawn). Default ":8000".
	TCPAddr string
	// UDPAddr is the socket for MoveInput and snapshots. Default ":8001".
	UDPAddr string
	// TickRate is the simulation cadence; v1 is fixed at game.TickRate
	// (20 Hz) because internal/game integrates a constant dt per step.
	TickRate int32
	// DevAuth accepts any credentials and returns username as the
	// player id (v1 dev mode). False rejects all — a placeholder for
	// real authentication.
	DevAuth bool
	// SpawnX/SpawnZ is the fixed spawn position issued by dev auth
	// (design: v1 spawns players at a fixed point).
	SpawnX float32
	SpawnZ float32
	// MinProtoVer/MaxProtoVer is the supported protocol version range
	// negotiated at the handshake (spec R5).
	MinProtoVer int32
	MaxProtoVer int32
	// HandshakeTimeout bounds the Hello→EnterWorld sequence; zero
	// disables it.
	HandshakeTimeout time.Duration
}

// player ties one authenticated session to its wire resources: the
// Transport the session drives and the raw TCP conn used for the
// server's own reliable writes (the real WorldSnapshot and the
// spawn/despawn fanout).
type player struct {
	sess *session.Session
	tr   network.Transport
	tcp  net.Conn
}

// Server owns the listeners, the session registry and the simulation.
// The player map is guarded by mu: the UDP loop, the per-conn
// goroutines and the sim-owner goroutine all touch it.
type Server struct {
	cfg  Config
	reg  *protocol.Registry
	sim  *game.Simulation
	auth session.Authenticator

	// udp is the packet conn snapshots are sent over. It is assigned in
	// Run before any goroutine starts and never mutated afterwards, so
	// the sink may read it without the mutex.
	udp net.PacketConn

	mu      sync.Mutex
	players map[string]*player

	// simOps serializes every simulation mutation (join/leave) onto the
	// sim-owner goroutine — internal/game's RegisterPlayer/RemovePlayer/
	// TakeInterestEvents are NOT safe to call concurrently with Step.
	simOps chan simOp

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New constructs the server (registry, dev auth, simulation wired to
// this server as its sink) without opening any sockets. Run opens them.
func New(cfg Config) (*Server, error) {
	if cfg.TCPAddr == "" || cfg.UDPAddr == "" {
		return nil, errors.New("server: TCP and UDP listen addresses are required")
	}
	if cfg.TickRate != game.TickRate {
		return nil, fmt.Errorf("server: tick rate %d not supported: v1 simulation cadence is fixed at %d Hz by internal/game",
			cfg.TickRate, game.TickRate)
	}
	if cfg.MinProtoVer <= 0 || cfg.MaxProtoVer < cfg.MinProtoVer {
		return nil, errors.New("server: invalid protocol version range")
	}
	srv := &Server{
		cfg:     cfg,
		reg:     protocol.NewWorldRegistry(),
		auth:    devAuthenticator{enabled: cfg.DevAuth, spawn: game.Vec2{X: cfg.SpawnX, Z: cfg.SpawnZ}},
		players: make(map[string]*player),
		simOps:  make(chan simOp),
	}
	sim, err := game.NewSimulation(game.SimulationConfig{Sink: srv})
	if err != nil {
		return nil, err
	}
	srv.sim = sim
	return srv, nil
}

// Run starts the listeners and the three loops (sim, UDP, accept) and
// blocks until ctx is cancelled (SIGINT/SIGTERM via signal.NotifyContext
// in main), then shuts down gracefully: listeners close, sessions and
// conns are torn down, and every goroutine is joined.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel

	ln, err := net.Listen("tcp", s.cfg.TCPAddr)
	if err != nil {
		return fmt.Errorf("server: tcp listen %s: %w", s.cfg.TCPAddr, err)
	}
	defer ln.Close()

	udpConn, err := net.ListenPacket("udp", s.cfg.UDPAddr)
	if err != nil {
		return fmt.Errorf("server: udp listen %s: %w", s.cfg.UDPAddr, err)
	}
	defer udpConn.Close()
	s.udp = udpConn // immutable after this point; see Server.udp docs

	ticker := time.NewTicker(time.Second / time.Duration(s.cfg.TickRate))
	defer ticker.Stop()

	s.wg.Add(3)
	go func() {
		defer s.wg.Done()
		if err := s.runSim(ctx, ticker.C); err != nil {
			log.Printf("server: simulation loop failed: %v", err)
			s.cancel()
		}
	}()
	go func() {
		defer s.wg.Done()
		if err := s.udpLoop(ctx, udpConn); err != nil {
			log.Printf("server: udp loop failed: %v", err)
			s.cancel()
		}
	}()
	go func() {
		defer s.wg.Done()
		if err := s.acceptLoop(ctx, ln); err != nil {
			log.Printf("server: accept loop failed: %v", err)
			s.cancel()
		}
	}()

	<-ctx.Done()

	// Unblock per-conn goroutines blocked in ReadFrame and release the
	// UDP binding of every session.
	_ = ln.Close()
	_ = udpConn.Close()
	s.mu.Lock()
	for _, p := range s.players {
		_ = p.sess.Close()
		_ = p.tcp.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
	return nil
}

// acceptLoop accepts TCP conns and starts one handleConn goroutine per
// connection (each registered in the wait group; Add is safe here
// because the accept loop itself holds a count until it exits).
func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // listener closed during shutdown
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// handleConn drives one session over its TCP conn: it reads length-
// prefixed frames, feeds them to the session, and when the session
// reaches in-world it registers the player in the simulation and sends
// the REAL WorldSnapshot. On any read error (io.EOF at a clean
// boundary, oversize frame, network failure) or session protocol error
// the connection is cleaned up.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	tr := &tcpConnTransport{conn: conn}
	sess, err := session.NewSession(s.reg, tr, session.Config{
		MinProtoVer:      s.cfg.MinProtoVer,
		MaxProtoVer:      s.cfg.MaxProtoVer,
		TickRate:         s.cfg.TickRate,
		Auth:             s.auth,
		Now:              time.Now,
		HandshakeTimeout: s.cfg.HandshakeTimeout,
	})
	if err != nil {
		_ = conn.Close()
		return
	}
	defer s.cleanupConn(ctx, conn, sess)

	for {
		frame, err := network.ReadFrame(conn)
		if err != nil {
			return
		}
		if err := sess.HandleTCP(frame); err != nil {
			return // protocol violation — the session already closed itself
		}
		pid := sess.PlayerID()
		if sess.State() == session.StateInWorld && pid != "" && !s.isRegistered(pid) {
			if err := s.enterWorld(ctx, conn, sess, tr); err != nil {
				// Join failed (e.g. duplicate player id): drop the
				// connection so the client can retry cleanly.
				log.Printf("server: player %q join failed: %v", pid, err)
				return
			}
		}
	}
}

// cleanupConn tears down a session: the session closes its transport
// (which closes the conn), the player is removed from the simulation
// (emitting its despawn event) and dropped from the server map.
func (s *Server) cleanupConn(ctx context.Context, conn net.Conn, sess *session.Session) {
	pid := sess.PlayerID()
	_ = sess.Close()
	if pid != "" {
		// RemovePlayer is a no-op for players that never joined and
		// returns ctx.Err during shutdown — both are benign.
		_ = s.simDo(ctx, func() error { return s.sim.RemovePlayer(pid) })
		s.mu.Lock()
		delete(s.players, pid)
		s.mu.Unlock()
	}
	_ = conn.Close()
}

// isRegistered reports whether the player is in the server map.
func (s *Server) isRegistered(pid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.players[pid]
	return ok
}

// enterWorld registers the authenticated player in the simulation at
// their spawn position and sends the REAL WorldSnapshot over TCP.
//
// v1 behavior: the session's handshake already delivered an EMPTY
// WorldSnapshot as the EnterWorld ack (the session layer has no world
// state); this real one follows and carries the current world state, so
// the client's first authoritative view is non-empty.
func (s *Server) enterWorld(ctx context.Context, conn net.Conn, sess *session.Session, tr network.Transport) error {
	pid := sess.PlayerID()
	s.mu.Lock()
	if _, dup := s.players[pid]; dup {
		s.mu.Unlock()
		return fmt.Errorf("server: player %q already connected", pid)
	}
	s.players[pid] = &player{sess: sess, tr: tr, tcp: conn}
	s.mu.Unlock()

	frame, err := s.registerAndSnapshot(ctx, pid, vecFromProto(sess.SpawnPos()), sess.WireVersion())
	if err != nil {
		s.mu.Lock()
		delete(s.players, pid)
		s.mu.Unlock()
		return err
	}
	return network.WriteFrame(conn, frame)
}
