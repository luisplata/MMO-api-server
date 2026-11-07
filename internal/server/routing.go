package server

import (
	"errors"
	"fmt"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/rooms"
)

// ErrEmptyCharacterID rejects binding a character with no identity.
var ErrEmptyCharacterID = errors.New("server: empty character id")

// bindCharacter atomically claims cid for roomID (fails if already bound).
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

func (s *Server) unbindCharacter(cid string) {
	s.mu.Lock()
	delete(s.charRooms, cid)
	s.mu.Unlock()
}

func (s *Server) roomOfCharacter(cid string) (rooms.RoomID, bool) {
	if cid == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	roomID, ok := s.charRooms[cid]
	return roomID, ok
}

// simForCharacter resolves the owning room sim; unknown ids report false.
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
