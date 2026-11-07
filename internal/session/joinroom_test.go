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
	// Seed room-7 as a known room with one committed member. ByID must join
	// existing rooms only; the seed is the fixture, not a ByID side effect.
	seed, err := alloc.Reserve("room-7")
	if err != nil {
		t.Fatalf("seed room-7: %v", err)
	}
	if err := alloc.Commit(seed); err != nil {
		t.Fatalf("commit seed member: %v", err)
	}
	t.Cleanup(func() { alloc.Release(seed) })

	join := func(t *testing.T) (*mmov1.JoinRoomResponse, *Session) {
		s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
		sendJoin(t, s, reg, protocol.JoinRoomByID, "room-7")
		_, msg := sentFrame(t, reg, tr, 2)
		return msg.(*mmov1.JoinRoomResponse), s
	}
	// One seed member + one reservation fill the room: the first ByID takes
	// the last slot (occupancy 2) and the next is rejected as full.
	jr, first := join(t)
	if jr.RoomId != "room-7" || jr.Occupancy != rooms.Capacity {
		t.Errorf("ByID known room = %+v, want room-7 occ=%d", jr, rooms.Capacity)
	}
	if first.State() != StateSelecting || first.ActiveRoomID() != "room-7" {
		t.Errorf("first ByID state=%s room=%q, want selecting/room-7", first.State(), first.ActiveRoomID())
	}
	jr, third := join(t)
	if jr.RoomId != "" || third.State() != StateSelecting || third.ActiveRoomID() != "" || alloc.Occupancy("room-7")+alloc.Reserved("room-7") != rooms.Capacity {
		t.Errorf("full ByID = %+v state=%s room=%q slots=%d, want rejected", jr, third.State(), third.ActiveRoomID(), alloc.Occupancy("room-7")+alloc.Reserved("room-7"))
	}
	s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.SelectCharacter{CharacterId: testCharID}, 0, 0)); err != nil {
		t.Fatalf("SelectCharacter: %v", err)
	}
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.EnterWorld{}, 0, 0)); !errors.Is(err, ErrIllegalMessage) || !tr.closed {
		t.Errorf("EnterWorld without room err=%v closed=%v, want illegal/true", err, tr.closed)
	}
}

// TestLobbyByIDUnknownRoomIsRejected pins not-found semantics: ByID never
// mints an unknown room, reserves no slot and leaves the session selecting.
func TestLobbyByIDUnknownRoomIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		roomID string
	}{
		{"empty id", ""},
		{"unknown id", "ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alloc := rooms.NewManager()
			s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
			sendJoin(t, s, reg, protocol.JoinRoomByID, tc.roomID)
			_, msg := sentFrame(t, reg, tr, 2)
			jr := msg.(*mmov1.JoinRoomResponse)
			if jr.RoomId != "" || jr.Capacity != 0 || jr.Occupancy != 0 {
				t.Fatalf("ByID %q = %+v, want zero not-found response", tc.roomID, jr)
			}
			if s.State() != StateSelecting || s.ActiveRoomID() != "" {
				t.Fatalf("after unknown ByID state=%s room=%q, want selecting/empty", s.State(), s.ActiveRoomID())
			}
			if got := alloc.RoomIDs(); len(got) != 0 {
				t.Fatalf("RoomIDs after unknown ByID = %v, want none (no room created)", got)
			}
			if got := alloc.Reserved(rooms.RoomID(tc.roomID)); got != 0 {
				t.Fatalf("Reserved(%q) = %d, want 0", tc.roomID, got)
			}
		})
	}
}

// TestLobbyRejectsUnknownModes pins the two-mode contract: mode 0, the old
// Create value 3 and an arbitrary 99 return the zero response, create no
// room and consume no reservation.
func TestLobbyRejectsUnknownModes(t *testing.T) {
	cases := []struct {
		name   string
		mode   int32
		roomID string
	}{
		{"zero", 0, ""},
		{"old create", 3, "create-room"},
		{"unknown", 99, "ghost-99"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alloc := rooms.NewManager()
			s, tr, reg := selectingSession(t, func(c *Config) { c.Rooms = alloc })
			sendJoin(t, s, reg, tc.mode, tc.roomID)
			_, msg := sentFrame(t, reg, tr, 2)
			jr := msg.(*mmov1.JoinRoomResponse)
			if jr.RoomId != "" || jr.Capacity != 0 || jr.Occupancy != 0 {
				t.Fatalf("mode %d = %+v, want zero response", tc.mode, jr)
			}
			if s.State() != StateSelecting || s.ActiveRoomID() != "" {
				t.Fatalf("after mode %d state=%s room=%q, want selecting/empty", tc.mode, s.State(), s.ActiveRoomID())
			}
			if got := alloc.RoomIDs(); len(got) != 0 {
				t.Fatalf("mode %d created rooms = %v, want none", tc.mode, got)
			}
			if got := alloc.Reserved(rooms.RoomID(tc.roomID)); got != 0 {
				t.Fatalf("mode %d reserved = %d, want 0", tc.mode, got)
			}
		})
	}
}
