package server

// Boot-wiring tests (P17): the deployed binary loads the committed
// template catalog and character store through LoadRepositories, injects
// both into server.Config, and uses the configured spawn resolver. A
// character can then be listed, created and selected with ok:true. Missing
// or corrupt data files abort the boot instead of exposing a partial
// character service.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/character"
	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/rooms"
	"github.com/luisplata/mmo-api-server/internal/session"
	"github.com/luisplata/mmo-api-server/internal/template"
	"github.com/luisplata/mmo-api-server/internal/world"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// bootRepoRoot locates the module root from this file's location so the
// test reads the same committed data/ files the deployed flags default to.
func bootRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// seedDataFile copies one committed data file into dir, returning the new
// path. Writable stores need a temp copy so a Create never pollutes the
// committed seed file.
func seedDataFile(t *testing.T, root, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "data", name))
	if err != nil {
		t.Fatalf("read data/%s: %v", name, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// bootTransport records every reliable frame a session sends so the test
// can assert the List/Create/Select responses without a socket.
type bootTransport struct{ tcpFrames [][]byte }

func (b *bootTransport) SendTCP(frame []byte) error {
	b.tcpFrames = append(b.tcpFrames, append([]byte(nil), frame...))
	return nil
}

func (b *bootTransport) SendUDP([]byte) error { return nil }
func (b *bootTransport) Close() error         { return nil }

// bootSend drives one lifecycle message through the session.
func bootSend(t *testing.T, sess *session.Session, reg *protocol.Registry, msg proto.Message) {
	t.Helper()
	frame, err := reg.EncodeMessage(msg, protocol.Envelope{Version: 2})
	if err != nil {
		t.Fatalf("encode %T: %v", msg, err)
	}
	if err := sess.HandleTCP(frame); err != nil {
		t.Fatalf("HandleTCP(%T): %v", msg, err)
	}
}

// bootReply decodes the last reliable frame the session wrote.
func bootReply[T proto.Message](t *testing.T, tr *bootTransport, reg *protocol.Registry) T {
	t.Helper()
	if len(tr.tcpFrames) == 0 {
		t.Fatal("session wrote no frame")
	}
	env, err := protocol.DecodeEnvelope(tr.tcpFrames[len(tr.tcpFrames)-1])
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	msg, err := reg.DecodeMessage(*env)
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	out, ok := msg.(T)
	if !ok {
		t.Fatalf("frame carries %T, want %T", msg, out)
	}
	return out
}

// bootServer wires the loaded repositories into a server exactly as
// cmd/server does, using the embedded heightfield and configured spawn.
func bootServer(t *testing.T, repos Repositories) *Server {
	t.Helper()
	heights, err := world.DefaultHeightfield()
	if err != nil {
		t.Fatalf("DefaultHeightfield: %v", err)
	}
	srv, err := New(Config{
		WSSAddr:     "127.0.0.1:0",
		TickRate:    game.TickRate,
		DevAuth:     true,
		SpawnX:      100,
		SpawnZ:      200,
		MinProtoVer: 2,
		MaxProtoVer: 2,
		Heights:     heights,
		Templates:   repos.Templates,
		Characters:  repos.Characters,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// TestLoadRepositoriesDrivesCharacterFlow is the P17 acceptance gate: the
// repositories loaded by the deployed boot path answer List (empty),
// Create (ok:true) and Select (ok:true with the fixed spawn) against the
// committed catalog, and the second List reflects the created character.
func TestLoadRepositoriesDrivesCharacterFlow(t *testing.T) {
	root := bootRepoRoot(t)
	dir := t.TempDir()
	repos, err := LoadRepositories(
		seedDataFile(t, root, dir, "templates.json"),
		seedDataFile(t, root, dir, "characters.json"),
	)
	if err != nil {
		t.Fatalf("LoadRepositories: %v", err)
	}
	srv := bootServer(t, repos)

	tr := &bootTransport{}
	sess, err := session.NewSession(srv.reg, tr, session.Config{
		MinProtoVer: 2,
		MaxProtoVer: 2,
		TickRate:    game.TickRate,
		Auth:        srv.auth,
		Now:         time.Now,
		Templates:   srv.templates,
		Characters:  srv.characters,
		Spawn:       srv.spawn,
		Rooms:       rooms.NewManager(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	bootSend(t, sess, srv.reg, &mmov1.Hello{ProtoVer: 2})
	bootSend(t, sess, srv.reg, &mmov1.AuthRequest{Username: "alice", Password: "pw"})

	bootSend(t, sess, srv.reg, &mmov1.ListCharacters{})
	if list := bootReply[*mmov1.CharacterList](t, tr, srv.reg); len(list.Characters) != 0 {
		t.Fatalf("fresh account list = %v, want empty", list.Characters)
	}

	bootSend(t, sess, srv.reg, &mmov1.CreateCharacter{Name: "Hero", TemplateId: "warrior"})
	cc := bootReply[*mmov1.CreateCharacterResponse](t, tr, srv.reg)
	if !cc.GetOk() || cc.GetCharacter().GetId() == "" {
		t.Fatalf("CreateCharacter = %v, want ok with an id", cc)
	}
	if cc.GetCharacter().GetTemplateId() != "warrior" || cc.GetCharacter().GetStats().GetHp() != 120 {
		t.Fatalf("created character = %v, want warrior template snapshot", cc.GetCharacter())
	}

	bootSend(t, sess, srv.reg, &mmov1.SelectCharacter{CharacterId: cc.GetCharacter().GetId()})
	sc := bootReply[*mmov1.SelectCharacterResponse](t, tr, srv.reg)
	if !sc.GetOk() {
		t.Fatalf("SelectCharacter = %v, want ok", sc)
	}
	if got := sc.GetSpawnPos(); got.GetX() != 100 || got.GetY() != 25 || got.GetZ() != 200 {
		t.Fatalf("spawn = %v, want (100, 25, 200) from the flags and heightfield", got)
	}

	bootSend(t, sess, srv.reg, &mmov1.ListCharacters{})
	if list := bootReply[*mmov1.CharacterList](t, tr, srv.reg); len(list.Characters) != 1 || list.Characters[0].GetId() != cc.GetCharacter().GetId() {
		t.Fatalf("list after create = %v, want the created character", list.Characters)
	}
}

// TestLoadRepositoriesFailsFast pins the boot failure contract: a missing
// or corrupt catalog/store is a wrapped error (os.ErrNotExist /
// ErrInvalidJSON) and never a usable partial repository.
func TestLoadRepositoriesFailsFast(t *testing.T) {
	root := bootRepoRoot(t)
	dir := t.TempDir()
	goodTemplates := seedDataFile(t, root, dir, "templates.json")
	goodCharacters := seedDataFile(t, root, dir, "characters.json")

	corruptTemplates := filepath.Join(dir, "bad-templates.json")
	if err := os.WriteFile(corruptTemplates, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt templates: %v", err)
	}
	corruptCharacters := filepath.Join(dir, "bad-characters.json")
	if err := os.WriteFile(corruptCharacters, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt characters: %v", err)
	}
	missing := filepath.Join(dir, "missing.json")

	cases := []struct {
		name       string
		templates  string
		characters string
		want       error
	}{
		{"missing templates", missing, goodCharacters, os.ErrNotExist},
		{"missing characters", goodTemplates, missing, os.ErrNotExist},
		{"corrupt templates", corruptTemplates, goodCharacters, template.ErrInvalidJSON},
		{"corrupt characters", goodTemplates, corruptCharacters, character.ErrInvalidJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadRepositories(tc.templates, tc.characters); !errors.Is(err, tc.want) {
				t.Fatalf("LoadRepositories() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestDefaultSpawnIsFixedForEverySlot pins 17.4: when Config.Spawn is
// absent, the server builds the default resolver from the spawn flags and
// the heightfield, and every character (first or second slot) gets the
// same fixed spawn with the terrain-derived Y.
func TestDefaultSpawnIsFixedForEverySlot(t *testing.T) {
	srv := bootServer(t, Repositories{
		Templates:  testTemplateRepo(),
		Characters: testCharacterRepo("alice"),
	})
	for _, characterID := range []string{"first", "second"} {
		got := srv.spawn.SpawnFor(characterID)
		if got.GetX() != 100 || got.GetY() != 25 || got.GetZ() != 200 {
			t.Errorf("SpawnFor(%q) = %v, want (100, 25, 200)", characterID, got)
		}
	}
}

// TestNewRequiresCharacterService pins the construction contract: the
// deployed server cannot be built without the character repositories, so a
// wiring regression fails at boot instead of silently answering every
// List/Create/Select with a not-ok response.
func TestNewRequiresCharacterService(t *testing.T) {
	valid := Config{
		WSSAddr:     "127.0.0.1:0",
		TickRate:    game.TickRate,
		DevAuth:     true,
		MinProtoVer: 2,
		MaxProtoVer: 2,
		Templates:   testTemplateRepo(),
		Characters:  testCharacterRepo("alice"),
	}
	if _, err := New(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"missing templates", func(c *Config) { c.Templates = nil }},
		{"missing characters", func(c *Config) { c.Characters = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatalf("New(%+v) must fail without the character service", cfg)
			}
		})
	}
}
