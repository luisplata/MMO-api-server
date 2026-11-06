package session

// Application-level latency probe (spec latency-probe, design D2). It runs
// on the session's connection read loop, outside the 20 Hz simulation tick:
// a Ping never enqueues a sim input, mutates world state, or blocks the
// ticker. The server echoes nonce/clientTime; the client owns RTT smoothing.

import (
	"time"

	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// pingsPerSecond caps the Pong rate per session. Excess probes are dropped
// silently — a client that floods pings loses samples, not its connection.
const pingsPerSecond = 5

// handlePing echoes the probe's nonce/clientTime. Ping is accepted in any
// live state (like Ack) so the client can measure RTT during selecting as
// well as in-world; a ping never changes the lifecycle state.
func (s *Session) handlePing(p *mmov1.Ping) error {
	now := s.cfg.Now()
	if s.pingWindow.IsZero() || now.Sub(s.pingWindow) >= time.Second {
		s.pingWindow = now
		s.pingCount = 0
	}
	if s.pingCount >= pingsPerSecond {
		return nil
	}
	s.pingCount++
	return s.sendTCP(&mmov1.Pong{Nonce: p.Nonce, ClientTime: p.ClientTime}, false)
}
