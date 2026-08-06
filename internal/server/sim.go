package server

// The sim-owner goroutine: the single owner of the simulation's
// non-threadsafe surface (RegisterPlayer, RemovePlayer, Step,
// TakeInterestEvents, AssembleWorldSnapshot). The conn goroutines
// communicate with it through simOps; the UDP loop feeds the sim's
// concurrency-safe QueueInput directly.

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// simOp is one simulation mutation (join/leave) executed by the
// sim-owner goroutine. done is buffered so the owner never blocks on the
// sender even when the caller stopped waiting.
type simOp struct {
	apply func() error
	done  chan error
}

// simDo queues an op onto the sim-owner goroutine and waits for its
// result. During shutdown (ctx cancelled) it returns ctx.Err instead of
// blocking forever.
func (s *Server) simDo(ctx context.Context, apply func() error) error {
	op := simOp{apply: apply, done: make(chan error, 1)}
	select {
	case s.simOps <- op:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-op.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runSim owns the simulation. It mirrors game.Simulation.Run (wake
// channel timestamps drive the fixed-dt accumulator) but additionally
// services simOps — Run would block and starve joins/leaves — and drains
// interest events after every step and op, so spawn/despawn fanout never
// races the tick. Identical input sequences produce identical states
// (spec S14.2): the accumulator + Step are pure, exactly as in
// game.Simulation.Run.
func (s *Server) runSim(ctx context.Context, wake <-chan time.Time) error {
	acc := game.NewAccumulator(game.TickInterval)
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case op := <-s.simOps:
			if op.done != nil {
				op.done <- op.apply()
			} else {
				_ = op.apply()
			}
			s.fanoutInterest()
		case t := <-wake:
			if !last.IsZero() {
				steps := acc.StepCount(t.Sub(last))
				for i := 0; i < steps; i++ {
					if err := s.sim.Step(); err != nil {
						return err
					}
				}
			}
			last = t
			s.fanoutInterest()
		}
	}
}

// registerAndSnapshot runs on the sim-owner goroutine (via simDo) so
// RegisterPlayer and AssembleWorldSnapshot never race Step. It returns
// the encoded REAL WorldSnapshot the caller writes over the TCP conn.
func (s *Server) registerAndSnapshot(ctx context.Context, pid string, spawn game.Vec2, version uint16) ([]byte, error) {
	var frame []byte
	err := s.simDo(ctx, func() error {
		if err := s.sim.RegisterPlayer(pid, spawn); err != nil {
			return err
		}
		ws := s.sim.AssembleWorldSnapshot()
		f, err := s.reg.EncodeMessage(ws, protocol.Envelope{Version: version})
		if err != nil {
			return err
		}
		frame = f
		return nil
	})
	return frame, err
}

// fanoutInterest drains the sim's pending spawn/despawn events and turns
// them into TCP frames for each target (design D4, spec S18.2). The
// target set comes from the InterestResolver (v1 flat: everyone else).
func (s *Server) fanoutInterest() {
	events := s.sim.TakeInterestEvents()
	for _, ev := range events {
		for _, target := range ev.Targets {
			s.fanoutEvent(ev, target)
		}
	}
}

// fanoutEvent encodes one interest event for one target and writes it
// over the target's TCP conn, encoded with the TARGET's negotiated wire
// version. Missing targets/subjects are skipped without writing.
func (s *Server) fanoutEvent(ev world.InterestEvent, target string) {
	var msg proto.Message
	switch ev.Kind {
	case world.SpawnEvent:
		e, ok := s.sim.Entity(ev.Subject)
		if !ok {
			return // subject no longer exists — nothing to spawn
		}
		msg = &mmov1.SpawnEntity{EntityId: ev.Subject, State: entityStateFromGame(e)}
	case world.DespawnEvent:
		msg = &mmov1.DespawnEntity{EntityId: ev.Subject}
	default:
		return
	}
	s.mu.Lock()
	p := s.players[target]
	s.mu.Unlock()
	if p == nil || p.tcp == nil {
		return // target gone or not yet connected
	}
	frame, err := s.reg.EncodeMessage(msg, protocol.Envelope{Version: p.sess.WireVersion()})
	if err != nil {
		return
	}
	_ = network.WriteFrame(p.tcp, frame)
}
