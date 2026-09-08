package session

// Character-management flow over the selecting phase (spec
// character-management, design D3/D4): after a successful AuthRequest the
// session enters the `selecting` phase where ListCharacters,
// CreateCharacter and SelectCharacter are the only valid messages.
//
// Coverage: List empty/non-empty; Create valid / invalid-name /
// template-not-found / duplicate-name; Select valid / wrong-owner /
// not-found; EnterWorld rejected before a character is selected; the
// character messages rejected outside `selecting`.
//
// The fakes (fakeTemplateRepo / fakeCharacterRepo / fakeSpawnResolver)
// are in-memory stand-ins for the repository interfaces the server layer
// injects in the wiring (PR5). The shared helpers newTestSession,
// sendHello, sendAuth, sendEnterWorld, sendSelect and clientFrame live in
// session_test.go / this file and are all in package session.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/stats"
	"github.com/luisplata/mmo-api-server/internal/template"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// testCharID is the character seeded in newTestSession, owned by the
// fakeAuth player id ("p1"), so the inWorld helper can select it.
const testCharID = "char-1"

// fakeTemplateRepo is an in-memory TemplateRepository for session tests.
type fakeTemplateRepo struct {
	templates map[string]*template.Template
}

func (f *fakeTemplateRepo) Get(id string) (*template.Template, error) {
	t, ok := f.templates[id]
	if !ok {
		return nil, template.ErrNotFound
	}
	return t, nil
}

func (f *fakeTemplateRepo) All() ([]*template.Template, error) {
	out := []*template.Template{}
	for _, t := range f.templates {
		out = append(out, t)
	}
	return out, nil
}

// fakeCharacterRepo is an in-memory CharacterRepository mirroring the
// file impl's semantics: per-account case-insensitive name uniqueness
// (design D7), generated id when omitted, default CreatedAt when zero.
type fakeCharacterRepo struct {
	chars []*character.Character
}

func (f *fakeCharacterRepo) Create(c *character.Character) error {
	if c.ID == "" {
		c.ID = "gen-" + c.AccountID + "-" + c.Name
	}
	for _, e := range f.chars {
		if e.ID == c.ID {
			return character.ErrDuplicateID
		}
		if e.AccountID == c.AccountID && strings.EqualFold(e.Name, c.Name) {
			return character.ErrDuplicateName
		}
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	f.chars = append(f.chars, c)
	return nil
}

func (f *fakeCharacterRepo) ListByAccount(accountID string) ([]*character.Character, error) {
	out := []*character.Character{}
	for _, c := range f.chars {
		if c.AccountID == accountID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeCharacterRepo) Get(id string) (*character.Character, error) {
	for _, c := range f.chars {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, character.ErrNotFound
}

func (f *fakeCharacterRepo) FindByName(accountID, name string) (*character.Character, error) {
	for _, c := range f.chars {
		if c.AccountID == accountID && strings.EqualFold(c.Name, name) {
			return c, nil
		}
	}
	return nil, character.ErrNotFound
}

// fakeSpawnResolver returns a fixed spawn for any selected character.
type fakeSpawnResolver struct{ spawn mmov1.Vec3 }

func (f *fakeSpawnResolver) SpawnFor(characterID string) *mmov1.Vec3 {
	return &mmov1.Vec3{X: f.spawn.X, Y: f.spawn.Y, Z: f.spawn.Z}
}

// selectingSession drives a fresh session to the selecting phase (auth
// done, no character chosen yet) and returns it with its transport and
// registry.
func selectingSession(t *testing.T, mut func(*Config)) (*Session, *mockTransport, *protocol.Registry) {
	t.Helper()
	s, tr, _ := newTestSession(t, mut)
	reg := protocol.NewWorldRegistry()
	sendHello(t, s, reg)
	sendAuth(t, s, reg)
	if s.State() != StateSelecting {
		t.Fatalf("session state after auth = %s, want selecting", s.State())
	}
	return s, tr, reg
}

// sendSelect drives a SelectCharacter for charID and expects no error.
func sendSelect(t *testing.T, s *Session, reg *protocol.Registry, charID string) {
	t.Helper()
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.SelectCharacter{CharacterId: charID}, 0, 0)); err != nil {
		t.Fatalf("SelectCharacter(%s): %v", charID, err)
	}
}

// --- ListCharacters -----------------------------------------------------

func TestListCharactersEmpty(t *testing.T) {
	s, tr, reg := selectingSession(t, func(c *Config) {
		c.Characters = &fakeCharacterRepo{} // account has no characters
	})
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.ListCharacters{}, 0, 0)); err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	_, msg := sentFrame(t, reg, tr, 2)
	cl, ok := msg.(*mmov1.CharacterList)
	if !ok {
		t.Fatalf("frame 2 = %T, want CharacterList", msg)
	}
	if len(cl.Characters) != 0 {
		t.Errorf("CharacterList for an empty account = %d chars, want 0", len(cl.Characters))
	}
}

func TestListCharacters(t *testing.T) {
	s, tr, reg := selectingSession(t, nil) // default repo seeds char-1 for p1
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.ListCharacters{}, 0, 0)); err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	_, msg := sentFrame(t, reg, tr, 2)
	cl, ok := msg.(*mmov1.CharacterList)
	if !ok {
		t.Fatalf("frame 2 = %T, want CharacterList", msg)
	}
	if len(cl.Characters) != 1 {
		t.Fatalf("CharacterList = %d chars, want 1", len(cl.Characters))
	}
	c := cl.Characters[0]
	if c.Id != testCharID || c.AccountId != "p1" || c.Name != "Hero" || c.TemplateId != "warrior" {
		t.Errorf("CharacterList[0] = %+v, want id=%s account=p1 name=Hero template=warrior", c, testCharID)
	}
	if c.Stats == nil || c.Stats.Hp != 120 || c.Stats.Speed != 3 || c.Stats.Atk != 10 || c.Stats.Def != 8 {
		t.Errorf("CharacterList[0].Stats = %+v, want frozen warrior stats (120/3/10/8)", c.Stats)
	}
}

// --- CreateCharacter ----------------------------------------------------

func TestCreateCharacterValid(t *testing.T) {
	s, tr, reg := selectingSession(t, nil)
	err := s.HandleTCP(clientFrame(t, reg, &mmov1.CreateCharacter{TemplateId: "mage", Name: "Nuevo"}, 0, 0))
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	_, msg := sentFrame(t, reg, tr, 2)
	cc, ok := msg.(*mmov1.CreateCharacterResponse)
	if !ok {
		t.Fatalf("frame 2 = %T, want CreateCharacterResponse", msg)
	}
	if !cc.Ok {
		t.Fatalf("CreateCharacterResponse.Ok = false, want true (err=%q)", cc.ErrorMessage)
	}
	if cc.Character == nil {
		t.Fatalf("CreateCharacterResponse.Character is nil")
	}
	if cc.Character.Name != "Nuevo" || cc.Character.TemplateId != "mage" || cc.Character.AccountId != "p1" {
		t.Errorf("created Character = %+v, want name=Nuevo template=mage account=p1", cc.Character)
	}
	// Stats are a frozen snapshot of the template base stats (design D2).
	if cc.Character.Stats == nil || cc.Character.Stats.Hp != 80 || cc.Character.Stats.Speed != 4 ||
		cc.Character.Stats.Atk != 14 || cc.Character.Stats.Def != 4 {
		t.Errorf("created Character.Stats = %+v, want mage snapshot (80/4/14/4)", cc.Character.Stats)
	}
}

func TestCreateCharacterInvalidName(t *testing.T) {
	cases := []struct {
		name string
		nm   string
	}{
		{"empty", ""},
		{"too short", "ab"},
		{"off charset", "bad-name!"},
		{"whitespace only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tr, reg := selectingSession(t, nil)
			if err := s.HandleTCP(clientFrame(t, reg, &mmov1.CreateCharacter{TemplateId: "warrior", Name: tc.nm}, 0, 0)); err != nil {
				t.Fatalf("CreateCharacter(%q): %v", tc.nm, err)
			}
			_, msg := sentFrame(t, reg, tr, 2)
			cc, ok := msg.(*mmov1.CreateCharacterResponse)
			if !ok {
				t.Fatalf("frame 2 = %T, want CreateCharacterResponse", msg)
			}
			if cc.Ok {
				t.Errorf("CreateCharacter(%q).Ok = true, want false", tc.nm)
			}
			if cc.ErrorMessage == "" {
				t.Errorf("CreateCharacter(%q).ErrorMessage is empty, want a reason", tc.nm)
			}
			if cc.Character != nil {
				t.Errorf("CreateCharacter(%q).Character = %+v, want nil (no state mutated)", tc.nm, cc.Character)
			}
		})
	}
}

func TestCreateCharacterTemplateNotFound(t *testing.T) {
	s, tr, reg := selectingSession(t, nil)
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.CreateCharacter{TemplateId: "nope", Name: "ValidName"}, 0, 0)); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	_, msg := sentFrame(t, reg, tr, 2)
	cc, ok := msg.(*mmov1.CreateCharacterResponse)
	if !ok {
		t.Fatalf("frame 2 = %T, want CreateCharacterResponse", msg)
	}
	if cc.Ok {
		t.Errorf("CreateCharacter with unknown template .Ok = true, want false")
	}
	if cc.ErrorMessage == "" {
		t.Errorf("CreateCharacter with unknown template .ErrorMessage is empty")
	}
}

func TestCreateCharacterDuplicateName(t *testing.T) {
	// The default repo seeds a character named "Hero" for account "p1"
	// (design D7 uniqueness is case-insensitive per account).
	cases := []struct {
		name string
		nm   string
	}{
		{"exact duplicate", "Hero"},
		{"case-insensitive duplicate", "hero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tr, reg := selectingSession(t, nil)
			if err := s.HandleTCP(clientFrame(t, reg, &mmov1.CreateCharacter{TemplateId: "warrior", Name: tc.nm}, 0, 0)); err != nil {
				t.Fatalf("CreateCharacter(%q): %v", tc.nm, err)
			}
			_, msg := sentFrame(t, reg, tr, 2)
			cc, ok := msg.(*mmov1.CreateCharacterResponse)
			if !ok {
				t.Fatalf("frame 2 = %T, want CreateCharacterResponse", msg)
			}
			if cc.Ok {
				t.Errorf("CreateCharacter(%q).Ok = true, want false (duplicate name)", tc.nm)
			}
			if cc.ErrorMessage == "" {
				t.Errorf("CreateCharacter(%q).ErrorMessage is empty, want duplicate-name reason", tc.nm)
			}
		})
	}
}

// --- SelectCharacter ----------------------------------------------------

func TestSelectCharacterValid(t *testing.T) {
	s, tr, reg := selectingSession(t, nil)
	sendSelect(t, s, reg, testCharID)
	if s.State() != StateEntering {
		t.Errorf("after valid select state = %s, want entering", s.State())
	}
	if got := s.ActiveCharacterID(); got != testCharID {
		t.Errorf("ActiveCharacterID() = %q, want %q", got, testCharID)
	}
	assertSpawn(t, s.CharacterSpawn(), &mmov1.Vec3{X: 1.5, Y: 0, Z: -2.5})

	_, msg := sentFrame(t, reg, tr, 2)
	sc, ok := msg.(*mmov1.SelectCharacterResponse)
	if !ok {
		t.Fatalf("frame 2 = %T, want SelectCharacterResponse", msg)
	}
	if !sc.Ok {
		t.Fatalf("SelectCharacterResponse.Ok = false, want true (err=%q)", sc.ErrorMessage)
	}
	if sc.Character == nil || sc.Character.Id != testCharID {
		t.Errorf("SelectCharacterResponse.Character = %+v, want id %q", sc.Character, testCharID)
	}
	assertSpawn(t, sc.SpawnPos, &mmov1.Vec3{X: 1.5, Y: 0, Z: -2.5})
}

func TestSelectCharacterWrongOwner(t *testing.T) {
	other := &character.Character{
		ID: "char-x", AccountID: "someone-else", Name: "Other", TemplateID: "mage",
		Stats: stats.Stats{HP: 80, Speed: 4, Atk: 14, Def: 4},
	}
	s, tr, reg := selectingSession(t, func(c *Config) {
		c.Characters = &fakeCharacterRepo{chars: []*character.Character{other}}
	})
	sendSelectErr := s.HandleTCP(clientFrame(t, reg, &mmov1.SelectCharacter{CharacterId: "char-x"}, 0, 0))
	if sendSelectErr != nil {
		t.Fatalf("SelectCharacter wrong-owner: %v", sendSelectErr)
	}
	if s.State() != StateSelecting {
		t.Errorf("after wrong-owner select state = %s, want selecting (unchanged)", s.State())
	}
	if got := s.ActiveCharacterID(); got != "" {
		t.Errorf("ActiveCharacterID() = %q, want empty (no character activated)", got)
	}
	_, msg := sentFrame(t, reg, tr, 2)
	sc, ok := msg.(*mmov1.SelectCharacterResponse)
	if !ok {
		t.Fatalf("frame 2 = %T, want SelectCharacterResponse", msg)
	}
	if sc.Ok {
		t.Errorf("SelectCharacterResponse.Ok = true, want false (foreign character)")
	}
	if sc.ErrorMessage == "" {
		t.Errorf("SelectCharacterResponse.ErrorMessage is empty, want ownership reason")
	}
}

func TestSelectCharacterNotFound(t *testing.T) {
	s, tr, reg := selectingSession(t, nil)
	if err := s.HandleTCP(clientFrame(t, reg, &mmov1.SelectCharacter{CharacterId: "nope"}, 0, 0)); err != nil {
		t.Fatalf("SelectCharacter not-found: %v", err)
	}
	if s.State() != StateSelecting {
		t.Errorf("after not-found select state = %s, want selecting (unchanged)", s.State())
	}
	if got := s.ActiveCharacterID(); got != "" {
		t.Errorf("ActiveCharacterID() = %q, want empty", got)
	}
	_, msg := sentFrame(t, reg, tr, 2)
	sc, ok := msg.(*mmov1.SelectCharacterResponse)
	if !ok {
		t.Fatalf("frame 2 = %T, want SelectCharacterResponse", msg)
	}
	if sc.Ok {
		t.Errorf("SelectCharacterResponse.Ok = true, want false (nonexistent character)")
	}
}

// --- EnterWorld-before-Select + wrong-state guards ------------------------

func TestEnterWorldBeforeSelectRejected(t *testing.T) {
	s, tr, reg := selectingSession(t, nil) // state = selecting, no character chosen
	err := s.HandleTCP(clientFrame(t, reg, &mmov1.EnterWorld{}, 0, 0))
	if !errors.Is(err, ErrIllegalMessage) {
		t.Errorf("EnterWorld before select err = %v, want ErrIllegalMessage", err)
	}
	if s.State() != StateClosed || !tr.closed {
		t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
	}
}

func TestSelectThenEnterWorldReachesInWorld(t *testing.T) {
	s, tr, reg := selectingSession(t, nil)
	sendSelect(t, s, reg, testCharID)
	sendEnterWorld(t, s, reg)
	if s.State() != StateInWorld {
		t.Errorf("after select + EnterWorld state = %s, want in-world", s.State())
	}
	if tr.closed {
		t.Errorf("happy selecting flow must not close the transport")
	}
}

func TestCharacterMessagesOutsideSelecting(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Session, reg *protocol.Registry)
		msg   *mmov1.ListCharacters
	}{
		{"ListCharacters in connecting", func(t *testing.T, s *Session, reg *protocol.Registry) {}, &mmov1.ListCharacters{}},
		{"ListCharacters in handshaking", sendHello, &mmov1.ListCharacters{}},
		{"ListCharacters in in-world", inWorld, &mmov1.ListCharacters{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tr, _ := newTestSession(t, nil)
			reg := protocol.NewWorldRegistry()
			tc.setup(t, s, reg)
			err := s.HandleTCP(clientFrame(t, reg, tc.msg, 0, 0))
			if !errors.Is(err, ErrIllegalMessage) {
				t.Errorf("err = %v, want ErrIllegalMessage", err)
			}
			if s.State() != StateClosed || !tr.closed {
				t.Errorf("session must be closed (state=%s closed=%v)", s.State(), tr.closed)
			}
		})
	}
}

// --- Accessors before/after select ---------------------------------------

func TestActiveCharacterAccessorsBeforeSelect(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	reg := protocol.NewWorldRegistry()
	sendHello(t, s, reg)
	sendAuth(t, s, reg)
	if got := s.ActiveCharacterID(); got != "" {
		t.Errorf("ActiveCharacterID() before select = %q, want empty", got)
	}
	if s.CharacterSpawn() != nil {
		t.Errorf("CharacterSpawn() before select = %v, want nil", s.CharacterSpawn())
	}
	if s.ActiveCharacter() != nil {
		t.Errorf("ActiveCharacter() before select = %+v, want nil", s.ActiveCharacter())
	}
}
