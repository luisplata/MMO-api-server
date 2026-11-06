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
