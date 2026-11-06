package session

// Room lobby over selecting (spec room-lobby, session-entry, P15).

import (
	"errors"
	"testing"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func sendJoin(t *testing.T, s *Session, reg *protocol.Registry, mode int32, roomID string) {
	t.Helper()
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.JoinRoom{Mode: mode, RoomId: roomID}, 0, 0)); err != nil {
		t.Fatalf("JoinRoom: %v", err)
	}
}

func TestLobbyNextAvailableSelectEnterRelease(t *testing.T) {
	alloc := rooms.NewManager()
	s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
	sendJoin(t, s, reg, protocol.JoinRoomNextAvailable, "")
	_, msg := sentFrame(t, reg, tr, 2)
	jr := msg.(*mmov1.JoinRoomResponse)
	if jr.RoomId != string(rooms.DefaultRoom) || jr.Capacity != rooms.Capacity || jr.Occupancy != 1 {
		t.Fatalf("JoinRoomResponse = %+v, want default cap=2 occ=1", jr)
	}
	if s.State() != StateSelecting {
		t.Errorf("after join state = %s, want selecting", s.State())
	}
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.SelectCharacter{CharacterId: testCharID}, 0, 0)); err != nil {
		t.Fatalf("SelectCharacter: %v", err)
	}
	if s.State() != StateEntering {
		t.Errorf("after room+character state = %s, want entering", s.State())
	}
	sendEnterWorld(t, s, reg)
	if s.State() != StateInWorld || alloc.Occupancy(rooms.DefaultRoom) != 1 || alloc.Reserved(rooms.DefaultRoom) != 0 {
		t.Errorf("after EnterWorld state=%s occ=%d reserved=%d, want in-world/1/0", s.State(), alloc.Occupancy(rooms.DefaultRoom), alloc.Reserved(rooms.DefaultRoom))
	}
	_ = s.Close()
	if alloc.Occupancy(rooms.DefaultRoom) != 0 || !tr.closed {
		t.Errorf("after close occupancy=%d closed=%v, want 0/true", alloc.Occupancy(rooms.DefaultRoom), tr.closed)
	}
	// Triangulation: a full default opens room-1.
	alloc.Reserve(rooms.DefaultRoom)
	alloc.Reserve(rooms.DefaultRoom)
	s2, _, reg2 := selectingSession(t, func(c *Config) { c.Rooms = alloc })
	sendJoin(t, s2, reg2, protocol.JoinRoomNextAvailable, "")
	if s2.ActiveRoomID() != "room-1" {
		t.Errorf("NextAvailable with full default = %q, want room-1", s2.ActiveRoomID())
	}
}

func TestLobbyByIDFullModesAndGates(t *testing.T) {
	alloc := rooms.NewManager()
	join := func(t *testing.T) (*mmov1.JoinRoomResponse, *Session) {
		s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
		sendJoin(t, s, reg, protocol.JoinRoomByID, "room-7")
		_, msg := sentFrame(t, reg, tr, 2)
		return msg.(*mmov1.JoinRoomResponse), s
	}
	for i, want := range []int32{1, 2} {
		if jr, _ := join(t); jr.RoomId != "room-7" || jr.Occupancy != want {
			t.Errorf("ByID #%d = %+v, want room-7 occ=%d", i+1, jr, want)
		}
	}
	jr, third := join(t)
	if jr.RoomId != "" || third.State() != StateSelecting || third.ActiveRoomID() != "" || alloc.Reserved("room-7") != rooms.Capacity {
		t.Errorf("third ByID = %+v state=%s room=%q reserved=%d, want rejected", jr, third.State(), third.ActiveRoomID(), alloc.Reserved("room-7"))
	}
	s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.SelectCharacter{CharacterId: testCharID}, 0, 0)); err != nil {
		t.Fatalf("SelectCharacter: %v", err)
	}
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.EnterWorld{}, 0, 0)); !errors.Is(err, ErrIllegalMessage) || !tr.closed {
		t.Errorf("EnterWorld without room err=%v closed=%v, want illegal/true", err, tr.closed)
	}
}
