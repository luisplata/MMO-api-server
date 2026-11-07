package server

// Character -> room ownership index (design: Routing). A character lives in
// at most one room; inputs, fanout and teardown resolve the owning room
// through this index instead of the legacy default room.

import (
	"errors"
	"fmt"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/rooms"
)

// ErrEmptyCharacterID rejects binding a character with no identity.
var ErrEmptyCharacterID = errors.New("server: empty character id")

// bindCharacter atomically claims cid for roomID. It fails when the
// character is already bound, which is how a duplicate tab (same character
// in two rooms) is rejected. The index is guarded by the server mutex,
// together with the player map, so ownership cannot be double-claimed.
func (s *Server) bindCharacter(cid string, roomID rooms.RoomID) error {
	if cid == "" {
		return ErrEmptyCharacterID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.charRooms[cid]; ok {
		return fmt.Errorf("server: character %q already bound to room %q", cid, existing)
	}
	s.charRooms[cid] = roomID
	return nil
}

// unbindCharacter releases the character's room ownership (idempotent).
func (s *Server) unbindCharacter(cid string) {
	s.mu.Lock()
	delete(s.charRooms, cid)
	s.mu.Unlock()
}

// roomOfCharacter returns the room that owns the character.
func (s *Server) roomOfCharacter(cid string) (rooms.RoomID, bool) {
	if cid == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	roomID, ok := s.charRooms[cid]
	return roomID, ok
}

// simForCharacter resolves the character's owning room simulation. Unknown
// characters and destroyed rooms report false, so callers drop the work
// instead of routing it to some other room.
func (s *Server) simForCharacter(cid string) (*game.Simulation, bool) {
	roomID, ok := s.roomOfCharacter(cid)
	if !ok {
		return nil, false
	}
	rs := s.runner.room(roomID)
	if rs == nil {
		return nil, false
	}
	return rs.sim, true
}
