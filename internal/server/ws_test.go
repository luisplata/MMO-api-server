package server

// P8 WSS: one binary message per envelope, two players in one room, the
// 64 KiB read limit (1009), and the run context close (1001).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

const testWSOrigin = "https://allowed.example"

func newWSHarness(t *testing.T) (*Server, context.CancelFunc, context.Context, string, chan time.Time) {
	t.Helper()
	srv, err := New(Config{
		WSSAddr: "127.0.0.1:0", WSSOrigins: []string{"allowed.example"},
		TickRate: game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 9,
		Templates: testTemplateRepo(), Characters: &fakeCharacterRepo{},
		Spawn: &fakeSpawnResolver{spawn: mmov1.Vec3{X: 1, Z: 2}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ticks := make(chan time.Time)
	go srv.runSim(runCtx, ticks)
	ts := httptest.NewServer(srv.wsHandler(runCtx))
	t.Cleanup(ts.Close)
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(ctxCancel)
	return srv, cancel, ctx, "ws" + strings.TrimPrefix(ts.URL, "http") + wsPath, ticks
}

func dialWS(t *testing.T, ctx context.Context, url, origin string) *websocket.Conn {
	t.Helper()
	opts := &websocket.DialOptions{}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": {origin}}
	}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
	return c
}

func wsSend(t *testing.T, ctx context.Context, c *websocket.Conn, srv *Server, msg proto.Message) {
	t.Helper()
	frame, err := srv.reg.EncodeMessage(msg, protocol.Envelope{Version: testVersion})
	if err != nil {
		t.Fatalf("encode %T: %v", msg, err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write %T: %v", msg, err)
	}
}

func wsRead(t *testing.T, ctx context.Context, c *websocket.Conn, srv *Server) proto.Message {
	t.Helper()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type = %v, want binary", typ)
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	msg, err := srv.reg.DecodeMessage(*env)
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return msg
}

func wsRound(t *testing.T, ctx context.Context, c *websocket.Conn, srv *Server, msg proto.Message) proto.Message {
	t.Helper()
	wsSend(t, ctx, c, srv, msg)
	return wsRead(t, ctx, c, srv)
}

func enterWS(t *testing.T, ctx context.Context, srv *Server, c *websocket.Conn, user, name string) (*mmov1.WorldSnapshot, string) {
	t.Helper()
	if _, ok := wsRound(t, ctx, c, srv, &mmov1.Hello{ProtoVer: testVersion}).(*mmov1.ServerInfo); !ok {
		t.Fatal("want ServerInfo")
	}
	if _, ok := wsRound(t, ctx, c, srv, &mmov1.AuthRequest{Username: user, Password: "pw"}).(*mmov1.AuthResponse); !ok {
		t.Fatal("want AuthResponse")
	}
	if _, ok := wsRound(t, ctx, c, srv, &mmov1.ListCharacters{}).(*mmov1.CharacterList); !ok {
		t.Fatal("want CharacterList")
	}
	created, ok := wsRound(t, ctx, c, srv, &mmov1.CreateCharacter{Name: name, TemplateId: "warrior"}).(*mmov1.CreateCharacterResponse)
	if !ok || !created.GetOk() {
		t.Fatalf("create = %v", created)
	}
	id := created.GetCharacter().GetId()
	selected, ok := wsRound(t, ctx, c, srv, &mmov1.SelectCharacter{CharacterId: id}).(*mmov1.SelectCharacterResponse)
	if !ok || !selected.GetOk() {
		t.Fatalf("select = %v", selected)
	}
	wsSend(t, ctx, c, srv, &mmov1.EnterWorld{})
	if _, ok := wsRead(t, ctx, c, srv).(*mmov1.WorldSnapshot); !ok {
		t.Fatal("missing empty handshake snapshot")
	}
	snap, ok := wsRead(t, ctx, c, srv).(*mmov1.WorldSnapshot)
	if !ok {
		t.Fatal("missing real WorldSnapshot")
	}
	return snap, id
}

func TestWSSessionLifecycle(t *testing.T) {
	srv, _, ctx, url, _ := newWSHarness(t)
	c := dialWS(t, ctx, url, testWSOrigin)
	snap, id := enterWS(t, ctx, srv, c, "alice", "Hero")
	if len(snap.GetEntities()) != 1 || snap.GetEntities()[0].GetId() != id || !srv.isRegistered(id) {
		t.Fatalf("snapshot/registry = %v / %v, want only %q", snap.GetEntities(), srv.isRegistered(id), id)
	}
}

func TestWSTwoPlayersSeeEachOther(t *testing.T) {
	srv, _, ctx, url, ticks := newWSHarness(t)
	alice := dialWS(t, ctx, url, testWSOrigin)
	_, aliceID := enterWS(t, ctx, srv, alice, "alice", "Hero")
	bob := dialWS(t, ctx, url, testWSOrigin)
	bobSnap, bobID := enterWS(t, ctx, srv, bob, "bob", "Rival")

	seen := map[string]bool{}
	for _, e := range bobSnap.GetEntities() {
		seen[e.GetId()] = true
	}
	if !seen[aliceID] || !seen[bobID] {
		t.Fatalf("bob sees %v, want %q and %q", seen, aliceID, bobID)
	}
	spawn, ok := wsRead(t, ctx, alice, srv).(*mmov1.SpawnEntity)
	if !ok || spawn.GetEntityId() != bobID {
		t.Fatalf("alice fanout = %T %v, want SpawnEntity %q", spawn, spawn, bobID)
	}
	// A tick drives realtime snapshots through SendUDP→Realtime.
	now := time.Now()
	ticks <- now
	ticks <- now.Add(game.TickInterval)
	if _, ok := wsRead(t, ctx, alice, srv).(*mmov1.Snapshot); !ok {
		t.Fatal("alice did not receive a realtime snapshot")
	}
}

func TestWSReadLimit(t *testing.T) {
	_, _, ctx, url, _ := newWSHarness(t)
	cases := []struct {
		name string
		size int
		too  bool
	}{
		{"over the 64 KiB limit closes 1009", wsReadLimit + 1, true},
		{"exactly 64 KiB reaches the protocol layer", wsReadLimit, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dialWS(t, ctx, url, testWSOrigin)
			if err := c.Write(ctx, websocket.MessageBinary, make([]byte, tc.size)); err != nil {
				t.Fatalf("write %d bytes: %v", tc.size, err)
			}
			_, _, err := c.Read(ctx)
			got := websocket.CloseStatus(err)
			if tc.too && got != websocket.StatusMessageTooBig {
				t.Fatalf("close status = %d, want 1009 (%v)", got, err)
			}
			if !tc.too && got == websocket.StatusMessageTooBig {
				t.Fatalf("at-limit message rejected as too big: %v", err)
			}
		})
	}
}

func TestWSServerContextClosesWithGoingAway(t *testing.T) {
	srv, cancel, ctx, url, _ := newWSHarness(t)
	c := dialWS(t, ctx, url, testWSOrigin)
	wsRound(t, ctx, c, srv, &mmov1.Hello{ProtoVer: testVersion})
	cancel()
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("close status = %d, want 1001 (%v)", websocket.CloseStatus(err), err)
	}
}
