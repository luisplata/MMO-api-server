package server

// The snapshot sink (design PR4b, spec R17): each per-player snapshot is
// encoded with the player's wire version and handed to the player's
// bounded Sink (design P2): depth-1 last-wins, written off the tick.

import (
	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// SendSnapshot implements game.SnapshotSink. Unknown players, sessions
// without a sink and sessions without a UDP peer are skipped (their
// snapshots are dropped — the peer binds shortly after). Called from the
// sim-owner goroutine during Step.
func (s *Server) SendSnapshot(playerID string, snap *mmov1.Snapshot) error {
	s.mu.Lock()
	p := s.players[playerID]
	s.mu.Unlock()
	if p == nil || p.sink == nil {
		return nil
	}
	sess := p.sess
	if !sess.UDPBound() || sess.UDPPeer() == nil {
		return nil
	}
	frame, err := s.reg.EncodeMessage(snap, protocol.Envelope{Version: sess.WireVersion()})
	if err != nil {
		return err
	}
	// Non-blocking enqueue: the sink coalesces snapshots (last-wins).
	p.sink.Realtime(frame)
	return nil
}
