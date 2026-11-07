package server

// The sim-owner goroutine: the single owner of every room's
// non-threadsafe surface (RegisterPlayer, RemovePlayer, Step,
// TakeInterestEvents, AssembleWorldSnapshot). The conn goroutines
// communicate with it through room-scoped ops; the UDP loop feeds the
// default room's concurrency-safe QueueInput directly.

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/stats"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// simDo queues a room-scoped op onto the room-owner goroutine and waits for
// its result. delta is the membership change (+1 join / -1 leave) applied on
// success. During shutdown (ctx cancelled) it returns ctx.Err instead of
// blocking forever.
func (s *Server) simDo(ctx context.Context, roomID rooms.RoomID, delta int, apply func(sim *game.Simulation) error) error {
	op := roomOp{roomID: roomID, delta: delta, apply: apply, done: make(chan error, 1)}
	select {
	case s.runner.ops <- op:
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

// runSim owns the rooms. It mirrors game.Simulation.Run (wake channel
// timestamps drive the fixed-dt accumulator) but additionally services room
// ops — Run would block and starve joins/leaves — and drains interest
// events after every step and op, so spawn/despawn fanout never races the
// tick. One goroutine, one ticker, every room stepped per wake (P6: the
// default room is the only populated one; P7 makes fanout per-room).
func (s *Server) runSim(ctx context.Context, wake <-chan time.Time) error {
	acc := game.NewAccumulator(game.TickInterval)
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case op := <-s.runner.ops:
			s.runner.apply(op)
			s.fanoutInterest()
		case t := <-wake:
			if !last.IsZero() {
				steps := acc.StepCount(t.Sub(last))
				for i := 0; i < steps; i++ {
					if err := s.runner.stepAll(); err != nil {
						return err
					}
				}
			}
			last = t
			s.fanoutInterest()
		}
	}
}

// registerAndSnapshot runs on the room-owner goroutine (via simDo) so
// RegisterPlayer and AssembleWorldSnapshot never race Step. It registers
// the player by its ENTITY id (the selected character id, design D3) with
// its template id + frozen stats in the addressed room, and returns the
// encoded REAL WorldSnapshot the caller writes over the TCP conn.
func (s *Server) registerAndSnapshot(ctx context.Context, roomID rooms.RoomID, id string, spawn game.Vec2, templateID string, st stats.Stats, version uint16) ([]byte, error) {
	var frame []byte
	err := s.simDo(ctx, roomID, +1, func(sim *game.Simulation) error {
		if err := sim.RegisterPlayer(id, spawn, templateID, st); err != nil {
			return err
		}
		ws := sim.AssembleWorldSnapshot()
		f, err := s.reg.EncodeMessage(ws, protocol.Envelope{Version: version})
		if err != nil {
			return err
		}
		frame = f
		return nil
	})
	return frame, err
}

func (s *Server) fanoutInterest() {
	for _, rs := range s.runner.allRooms() {
		for _, ev := range rs.sim.TakeInterestEvents() {
			for _, target := range ev.Targets {
				s.fanoutEvent(rs, ev, target)
			}
		}
	}
}

// fanoutEvent encodes one interest event for one target and hands it to
// the target's bounded outbound Sink (design P3), encoded with the
// TARGET's negotiated wire version. Missing targets/subjects are skipped
// without writing. The enqueue is non-blocking: a slow or broken target
// is bounded by the sink's queue and a failed write closes only that
// conn — never the sim-owner goroutine.
func (s *Server) fanoutEvent(rs *roomSim, ev world.InterestEvent, target string) {
	var msg proto.Message
	switch ev.Kind {
	case world.SpawnEvent:
		e, ok := rs.sim.Entity(ev.Subject)
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
	if p == nil || p.sink == nil {
		return // target gone or not yet connected
	}
	frame, err := s.reg.EncodeMessage(msg, protocol.Envelope{Version: p.sess.WireVersion()})
	if err != nil {
		return
	}
	_ = p.sink.Reliable(frame)
}
