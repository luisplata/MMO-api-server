package server

// The UDP input path (spec R13/R15, design PR4b point 5): one datagram
// is offered to every session (v1 flat — few players); each session
// accepts only its own bound peer (S13.2). The first packet carrying the
// bind token binds the session; subsequent MoveInputs from the bound
// peer are queued into the simulation. Datagrams that match nothing are
// ignored.

import (
	"context"
	"net"

	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/session"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// udpLoop reads datagrams and routes them until the conn closes or ctx
// is cancelled.
func (s *Server) udpLoop(ctx context.Context, pc net.PacketConn) error {
	for {
		payload, addr, err := network.ReadDatagram(pc)
		if err != nil {
			if ctx.Err() != nil {
				return nil // socket closed during shutdown
			}
			return err
		}
		s.routeUDP(payload, addr)
	}
}

// routeUDP classifies one datagram against every connected session.
func (s *Server) routeUDP(payload []byte, addr net.Addr) {
	s.mu.Lock()
	players := make([]*player, 0, len(s.players))
	for _, p := range s.players {
		players = append(players, p)
	}
	s.mu.Unlock()

	for _, p := range players {
		sess := p.sess
		if err := sess.HandleUDP(payload, addr); err != nil {
			continue // closed session
		}
		peer := sess.UDPPeer()
		if !sess.UDPBound() || peer == nil || peer.String() != addr.String() {
			continue // not bound yet, or datagram from a foreign peer
		}
		if isBindToken(payload, sess.UDPToken()) {
			continue // the bind datagram itself — not an input
		}
		if mi, _, ok := s.decodeMoveInput(payload); ok {
			// The input is queued for the session's world entity (the
			// selected character id, design D3) in the room that owns it.
			s.queueMoveInput(sess, mi)
		}
	}
}

// queueMoveInput queues one client MoveInput for the session's active
// character in the room that owns it. An unknown or unbound character is
// dropped, never defaulted to the legacy room. It moves with
// decodeMoveInput when P13 deletes the UDP loop.
func (s *Server) queueMoveInput(sess *session.Session, mi *mmov1.MoveInput) {
	cid := sess.ActiveCharacterID()
	if cid == "" {
		return
	}
	if sim, ok := s.simForCharacter(cid); ok {
		_ = sim.QueueInput(cid, mi)
	}
}

// decodeMoveInput decodes a frame into a MoveInput and its envelope
// version, or reports false for anything that is not one (garbage, other
// message types). The caller decides whether the version is acceptable:
// the WSS path must reject an out-of-range version (docs/PROTOCOL.md §9),
// while the legacy UDP path keeps its pre-existing behavior.
func (s *Server) decodeMoveInput(payload []byte) (*mmov1.MoveInput, uint16, bool) {
	env, err := protocol.DecodeEnvelope(payload)
	if err != nil {
		return nil, 0, false
	}
	msg, err := s.reg.DecodeMessage(*env)
	if err != nil {
		return nil, 0, false
	}
	mi, ok := msg.(*mmov1.MoveInput)
	return mi, env.Version, ok
}
