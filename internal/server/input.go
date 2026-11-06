package server

// Inbound realtime frames (spec WSS único, docs/PROTOCOL.md §9): on the
// WSS transport a MoveInput shares the frame stream with the lifecycle
// messages. The read loop decodes it here — before the session's lifecycle
// state machine — and queues it into the room that owns the character.

import (
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/session"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// queueMoveInput queues one client MoveInput for the session's active
// character in the room that owns it. An unknown or unbound character is
// dropped, never defaulted to the legacy room.
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
// the read loop rejects an out-of-range version through the session's
// standard VersionMismatch + close path (docs/PROTOCOL.md §9).
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
