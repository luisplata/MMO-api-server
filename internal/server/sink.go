package server

// The snapshot sink (design PR4b point 6, spec R17): the simulation's
// SnapshotSink seam delivers per-player snapshots (10 Hz, staggered) and
// the server routes each one over the player's bound UDP socket, encoded
// with the player's negotiated wire version.

import (
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// SendSnapshot implements game.SnapshotSink. The player is looked up by
// id; unknown players and sessions without a UDP peer are skipped (their
// snapshots are dropped — the peer binds shortly after). Called from the
// sim-owner goroutine during Step.
func (s *Server) SendSnapshot(playerID string, snap *mmov1.Snapshot) error {
	s.mu.Lock()
	p := s.players[playerID]
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	sess := p.sess
	peer := sess.UDPPeer()
	if !sess.UDPBound() || peer == nil {
		return nil
	}
	frame, err := s.reg.EncodeMessage(snap, protocol.Envelope{Version: sess.WireVersion()})
	if err != nil {
		return err
	}
	return network.SendDatagram(s.udp, peer, frame)
}
