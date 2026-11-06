// Package server wires the protocol, session, network and simulation
// layers into a running MMO server: the private WSS upstream (TLS
// terminates at the proxy) drives sessions through the handshake and
// carries lifecycle plus realtime frames, and a single sim-owner goroutine
// runs the deterministic 20 Hz tick, registers/removes players and fans
// out interest events.
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
	"net/http"
	"sync"
	"time"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/session"
	"github.com/luisplata/mmo-api-server/internal/template"
	"github.com/luisplata/mmo-api-server/internal/world"
)

// Config carries the server tunables (design PR4b).
type Config struct {
	// WSSAddr is the private upstream WebSocket listener (TLS terminates
	// at the proxy). It is the server's only listen address; empty is a
	// boot error.
	WSSAddr string
	// WSSOrigins is the explicit Origin allowlist; wildcards are rejected.
	WSSOrigins []string
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
	// Heights supplies the terrain sampler for the derived Y — used by
	// both the simulation (tick) and dev auth (spawn Y) (design D4, spec
	// CTH-2/CTH-3); nil selects the flat default (Y = 0).
	Heights world.HeightResolver
	// HandshakeTimeout bounds the Hello→EnterWorld sequence; zero
	// disables it.
	HandshakeTimeout time.Duration

	// Templates is the character-template catalog the session's
	// character-management flow consults (design D1). Optional; when nil
	// the session answers CreateCharacter with a not-ok response.
	Templates template.TemplateRepository
	// Characters is the character store (design D1). Optional; when nil
	// the session answers List/Create/Select with a not-ok/empty response.
	Characters character.CharacterRepository
	// Spawn resolves the spawn position for a selected character (design
	// D4). Optional; when nil the server builds a default resolver from
	// SpawnX/SpawnZ + Heights.
	Spawn session.SpawnResolver
}

// player ties one authenticated session to its wire resources: the
// bounded outbound Sink (design P2/P3) that owns all outbound delivery —
// realtime snapshots via the depth-1 slot and the reliable writes (the
// real WorldSnapshot and the spawn/despawn fanout) via the bounded queue.
type player struct {
	sess *session.Session
	sink network.Sink
	// roomID is the room that owns this player's character (design:
	// Routing). It is set when the player enters the world and is the
	// address used for the scoped leave/despawn.
	roomID rooms.RoomID
}

// Server owns the WSS listener, the session registry and the simulation.
// The player map is guarded by mu: the per-conn goroutines and the
// sim-owner goroutine both touch it.
type Server struct {
	cfg Config
	reg *protocol.Registry
	// sim is the default (legacy) room's simulation — the world the
	// pre-rooms server ran. It is the same pointer the runner maps under
	// DefaultRoom; P7's character→room routing resolves every other room.
	sim  *game.Simulation
	auth session.Authenticator

	// templates / characters / spawn are the character-management
	// repositories the session layer needs for the selecting phase
	// (design D1/D4). They are injected from Config; when nil the session
	// answers the selecting handlers with a not-ok response.
	templates  template.TemplateRepository
	characters character.CharacterRepository
	spawn      session.SpawnResolver

	mu      sync.Mutex
	players map[string]*player
	// charRooms is the character -> room ownership index (design:
	// Routing). Guarded by mu together with players, so a character can
	// never be claimed by two rooms.
	charRooms map[string]rooms.RoomID

	// runner owns every per-room simulation (sim above is the DefaultRoom
	// entry) and its room-owner goroutine — one ticker stepping all rooms.
	runner *roomRunner

	wsOrigins []string

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New constructs the server (registry, dev auth, simulation wired to
// this server as its sink) without opening any sockets. Run opens them.
func New(cfg Config) (*Server, error) {
	if cfg.WSSAddr == "" {
		return nil, errors.New("server: wss listen address is required")
	}
	if err := validateWSOrigins(cfg.WSSOrigins); err != nil {
		return nil, err
	}
	if cfg.TickRate != game.TickRate {
		return nil, fmt.Errorf("server: tick rate %d not supported: v1 simulation cadence is fixed at %d Hz by internal/game",
			cfg.TickRate, game.TickRate)
	}
	if cfg.MinProtoVer <= 0 || cfg.MaxProtoVer < cfg.MinProtoVer {
		return nil, errors.New("server: invalid protocol version range")
	}
	srv := &Server{
		cfg:        cfg,
		reg:        protocol.NewWorldRegistry(),
		auth:       devAuthenticator{enabled: cfg.DevAuth},
		templates:  cfg.Templates,
		characters: cfg.Characters,
		players:    make(map[string]*player),
		charRooms:  make(map[string]rooms.RoomID),
		wsOrigins:  cfg.WSSOrigins,
	}
	if cfg.Spawn == nil {
		// Default resolver: every selected character spawns at the
		// configured spawn point, with the terrain height in Y (design D4,
		// spec CTH-3). Tests inject a custom resolver.
		srv.spawn = &defaultSpawnResolver{spawnX: cfg.SpawnX, spawnZ: cfg.SpawnZ, heights: cfg.Heights}
	} else {
		srv.spawn = cfg.Spawn
	}
	sim, err := game.NewSimulation(game.SimulationConfig{Sink: srv, Heights: cfg.Heights})
	if err != nil {
		return nil, err
	}
	srv.sim = sim
	// The legacy world becomes the default room; new rooms reuse srv as the
	// snapshot sink and the configured heights.
	srv.runner = newRoomRunner(sim, srv, cfg.Heights)
	return srv, nil
}

// Run starts the WSS upstream and the sim loop and blocks until ctx is
// cancelled (SIGINT/SIGTERM via signal.NotifyContext in main), then shuts
// down gracefully: the listener closes, sessions and sinks are torn down,
// and every goroutine is joined.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel

	wsLn, err := net.Listen("tcp", s.cfg.WSSAddr)
	if err != nil {
		return fmt.Errorf("server: wss listen %s: %w", s.cfg.WSSAddr, err)
	}
	httpSrv := &http.Server{Handler: s.wsHandler(ctx), ReadHeaderTimeout: wsReadHeaderTimeout}
	wsClose := func() { _ = httpSrv.Close() }
	defer wsClose()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := httpSrv.Serve(wsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: wss server failed: %v", err)
			s.cancel()
		}
	}()

	ticker := time.NewTicker(time.Second / time.Duration(s.cfg.TickRate))
	defer ticker.Stop()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.runSim(ctx, ticker.C); err != nil {
			log.Printf("server: simulation loop failed: %v", err)
			s.cancel()
		}
	}()

	<-ctx.Done()
	wsClose()

	s.mu.Lock()
	for _, p := range s.players {
		_ = p.sess.Close()
		if p.sink != nil {
			_ = p.sink.Close()
		}
	}
	s.mu.Unlock()

	s.wg.Wait()
	return nil
}

// serveConn drives one session over an inbound FrameSource and its outbound
// Sink: it reads frames via src.Next(), feeds lifecycle frames to the
// session and routes inbound MoveInputs to the room simulation, then when
// the session reaches in-world registers the player and sends the REAL
// WorldSnapshot. On any read error (io.EOF at a clean boundary, oversize
// frame, network failure) or session protocol error the connection is
// cleaned up.
func (s *Server) serveConn(ctx context.Context, src network.FrameSource, tr network.Transport, sink network.Sink) {
	sess, err := session.NewSession(s.reg, tr, session.Config{
		MinProtoVer:      s.cfg.MinProtoVer,
		MaxProtoVer:      s.cfg.MaxProtoVer,
		TickRate:         s.cfg.TickRate,
		Auth:             s.auth,
		Now:              time.Now,
		HandshakeTimeout: s.cfg.HandshakeTimeout,
		Templates:        s.templates,
		Characters:       s.characters,
		Spawn:            s.spawn,
	})
	if err != nil {
		if sink != nil {
			_ = sink.Close()
		}
		return
	}
	defer s.cleanupConn(ctx, sess)

	for {
		frame, err := src.Next()
		if err != nil {
			return
		}
		// WSS carries lifecycle and realtime on one socket (spec WSS
		// único): a MoveInput belongs to the room sim, not the lifecycle
		// state machine, which would reject it as out-of-order. Only a
		// frame with a supported envelope version may take this shortcut;
		// anything else falls through so the session applies the same
		// VersionMismatch + close rule as a lifecycle frame
		// (docs/PROTOCOL.md §9).
		if mi, version, ok := s.decodeMoveInput(frame); ok && s.versionSupported(version) {
			s.queueMoveInput(sess, mi)
			continue
		}
		if err := sess.HandleTCP(frame); err != nil {
			return // protocol violation — the session already closed itself
		}
		cid := sess.ActiveCharacterID()
		if sess.State() == session.StateInWorld && cid != "" && !s.isRegistered(cid) {
			if err := s.enterWorld(ctx, sess, sink); err != nil {
				// Join failed (e.g. duplicate character id): drop the
				// connection so the client can retry cleanly.
				log.Printf("server: character %q join failed: %v", cid, err)
				return
			}
		}
	}
}

// versionSupported reports whether an envelope version is inside the
// configured protocol range (docs/PROTOCOL.md §9).
func (s *Server) versionSupported(v uint16) bool {
	return int32(v) >= s.cfg.MinProtoVer && int32(v) <= s.cfg.MaxProtoVer
}

// cleanupConn tears down a session: the session closes its transport
// (which closes the socket), the world entity (the selected character id,
// design D3) is removed from the simulation (emitting its despawn event)
// and dropped from the server map.
func (s *Server) cleanupConn(ctx context.Context, sess *session.Session) {
	cid := sess.ActiveCharacterID()
	_ = sess.Close()
	if cid != "" {
		// Only the session that actually owns the character may tear it
		// down. A rejected duplicate tab is cleaned up too, and must not
		// evict the first tab (design: one character, one room).
		s.mu.Lock()
		p := s.players[cid]
		owned := p != nil && p.sess == sess
		if owned {
			delete(s.players, cid)
			delete(s.charRooms, cid)
		}
		s.mu.Unlock()
		if owned {
			// RemovePlayer is a no-op for entities that never joined and
			// returns ctx.Err during shutdown — both are benign. The leave
			// is addressed to the character's owning room.
			_ = s.simDo(ctx, p.roomID, -1, func(sim *game.Simulation) error { return sim.RemovePlayer(cid) })
			// Stop the player's sink writer goroutine (design P2);
			// idempotent with the session close above.
			if p.sink != nil {
				_ = p.sink.Close()
			}
		}
	}
}

// isRegistered reports whether the player is in the server map.
func (s *Server) isRegistered(pid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.players[pid]
	return ok
}

// roomForSession resolves the room a session enters. Room intent on the
// wire arrives with the lobby slice (P10); until then every session enters
// the legacy default room. The seam exists so P10 only changes this
// resolver, not the routing/ownership code.
func (s *Server) roomForSession(*session.Session) rooms.RoomID {
	return DefaultRoom
}

// enterWorld registers the authenticated player in its room and sends the
// REAL WorldSnapshot through the player's sink.
func (s *Server) enterWorld(ctx context.Context, sess *session.Session, sink network.Sink) error {
	return s.enterWorldIn(ctx, sess, sink, s.roomForSession(sess))
}

// enterWorldIn registers the authenticated player in roomID. Ownership is
// claimed atomically before anything is registered, so a character can
// never appear in two rooms (duplicate tab), and every rollback path
// releases both the player map and the character index.
//
// v1 behavior: the session's handshake already delivered an EMPTY
// WorldSnapshot as the EnterWorld ack (the session layer has no world
// state); this real one follows and carries the current room state, so the
// client's first authoritative view is non-empty.
func (s *Server) enterWorldIn(ctx context.Context, sess *session.Session, sink network.Sink, roomID rooms.RoomID) error {
	// World entity identity = the selected character id (design D3), not
	// the account id. The session only reaches in-world via a valid
	// SelectCharacter, so the active character + its spawn are resolved.
	cid := sess.ActiveCharacterID()
	if cid == "" {
		return fmt.Errorf("server: no active character selected")
	}
	char := sess.ActiveCharacter()
	if char == nil {
		return fmt.Errorf("server: no active character selected")
	}
	spawn := sess.CharacterSpawn()
	if spawn == nil {
		return fmt.Errorf("server: no character spawn resolved")
	}
	if sink == nil {
		return fmt.Errorf("server: no outbound sink for character %q", cid)
	}
	// Claim character -> room ownership first: the duplicate tab loses
	// here, before any room is created or any entity registered.
	if err := s.bindCharacter(cid, roomID); err != nil {
		return err
	}
	if _, err := s.runner.ensureRoom(roomID); err != nil {
		s.unbindCharacter(cid)
		return err
	}
	s.mu.Lock()
	if _, dup := s.players[cid]; dup {
		s.mu.Unlock()
		s.unbindCharacter(cid)
		return fmt.Errorf("server: character %q already connected", cid)
	}
	p := &player{sess: sess, sink: sink, roomID: roomID}
	s.players[cid] = p
	s.mu.Unlock()

	// Design D4/D5: the entity registers at the character spawn with the
	// character's template id + frozen stats snapshot. The sim re-resolves
	// the terrain height authoritatively.
	frame, err := s.registerAndSnapshot(ctx, roomID, cid, spawnFromProto(spawn), char.TemplateID, char.Stats, sess.WireVersion())
	if err != nil {
		s.mu.Lock()
		delete(s.players, cid)
		s.mu.Unlock()
		s.unbindCharacter(cid)
		_ = p.sink.Close() // release the sink built above (design P3)
		return err
	}
	// Route the REAL WorldSnapshot through the bounded sink's reliable
	// queue (design P3): a broken client tears down only its own conn,
	// and the enqueue never blocks the conn goroutine.
	return p.sink.Reliable(frame)
}
