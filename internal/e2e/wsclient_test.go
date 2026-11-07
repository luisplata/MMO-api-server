package e2e

// P10 WSS wire client: one connection per player, one envelope per message.

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

type wsMsg struct {
	msg proto.Message
	err error
}

// wsClient is one player's real WSS connection.
type wsClient struct {
	reg  *protocol.Registry
	name string
	conn *websocket.Conn
	ch   chan wsMsg
	mu   sync.Mutex
	seen map[string]int
}

// newWSClient dials the WSS endpoint through the TLS front and starts the reader.
func newWSClient(t *testing.T, reg *protocol.Registry, front *httptest.Server, origin, name string) *wsClient {
	t.Helper()
	c, _, err := websocket.Dial(context.Background(), proxyWSSURL(front), &websocket.DialOptions{
		HTTPClient: front.Client(),
		HTTPHeader: http.Header{"Origin": {origin}},
	})
	if err != nil {
		t.Fatalf("%s: dial wss: %v", name, err)
	}
	w := &wsClient{
		reg: reg, name: name, conn: c,
		ch: make(chan wsMsg, 128), seen: make(map[string]int),
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
	go w.readLoop()
	return w
}

// readLoop decodes one envelope per binary message and closes on drop.
func (w *wsClient) readLoop() {
	defer close(w.ch)
	for {
		typ, data, err := w.conn.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			w.ch <- wsMsg{err: fmt.Errorf("message type %v, want binary", typ)}
			continue
		}
		env, err := protocol.DecodeEnvelope(data)
		if err != nil {
			w.ch <- wsMsg{err: err}
			continue
		}
		msg, err := w.reg.DecodeMessage(*env)
		if err != nil {
			w.ch <- wsMsg{err: err}
			continue
		}
		w.mu.Lock()
		w.seen[string(msg.ProtoReflect().Descriptor().Name())]++
		w.mu.Unlock()
		w.ch <- wsMsg{msg: msg}
	}
}

func (w *wsClient) seenTypes() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.seen)
}

// send writes msg as one binary message carrying one envelope.
func (w *wsClient) send(t *testing.T, msg proto.Message) {
	t.Helper()
	frame, err := w.reg.EncodeMessage(msg, protocol.Envelope{Version: protoVersion})
	if err != nil {
		t.Fatalf("%s: encode %T: %v", w.name, msg, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("%s: write %T over ws: %v", w.name, msg, err)
	}
}

func (w *wsClient) disconnect() {
	_ = w.conn.Close(websocket.StatusNormalClosure, "client leaving")
}

// requireWS waits for the next decoded message of type T, bounded by waitTimeout.
func requireWS[T proto.Message](t *testing.T, w *wsClient, what string, opt ...func(T) bool) T {
	t.Helper()
	want := func(T) bool { return true }
	if len(opt) > 0 {
		want = opt[0]
	}
	timeout := time.After(waitTimeout)
	for {
		select {
		case m, ok := <-w.ch:
			if !ok {
				t.Fatalf("%s: ws closed before %s; types seen: %v", w.name, what, w.seenTypes())
			}
			if m.err != nil {
				t.Fatalf("%s: bad ws message while waiting for %s: %v", w.name, what, m.err)
			}
			if msg, ok := m.msg.(T); ok && want(msg) {
				return msg
			}
		case <-timeout:
			t.Fatalf("%s: timed out waiting for %s; types seen: %v", w.name, what, w.seenTypes())
		}
	}
}

// connectAndAuth drives Hello→ServerInfo, AuthRequest→AuthResponse.
func (w *wsClient) connectAndAuth(t *testing.T, username string) *mmov1.AuthResponse {
	t.Helper()
	w.send(t, &mmov1.Hello{ProtoVer: protoVersion})
	if si := requireWS[*mmov1.ServerInfo](t, w, "ServerInfo"); si.ProtoVer != protoVersion {
		t.Fatalf("%s: ServerInfo.ProtoVer = %d, want %d", w.name, si.ProtoVer, protoVersion)
	}
	w.send(t, &mmov1.AuthRequest{Username: username, Password: "pw"})
	ar := requireWS[*mmov1.AuthResponse](t, w, "AuthResponse")
	if !ar.Ok {
		t.Fatalf("%s: auth rejected for %q: %s", w.name, username, ar.ErrorMessage)
	}
	return ar
}

func (w *wsClient) listCharacters(t *testing.T) *mmov1.CharacterList {
	w.send(t, &mmov1.ListCharacters{})
	return requireWS[*mmov1.CharacterList](t, w, "CharacterList")
}

func (w *wsClient) createCharacter(t *testing.T, templateID, name string) *mmov1.CreateCharacterResponse {
	w.send(t, &mmov1.CreateCharacter{TemplateId: templateID, Name: name})
	return requireWS[*mmov1.CreateCharacterResponse](t, w, "CreateCharacterResponse")
}

func (w *wsClient) selectCharacter(t *testing.T, charID string) *mmov1.SelectCharacterResponse {
	w.send(t, &mmov1.SelectCharacter{CharacterId: charID})
	return requireWS[*mmov1.SelectCharacterResponse](t, w, "SelectCharacterResponse")
}

// enterWorldNow sends EnterWorld and returns the REAL WorldSnapshot.
func (w *wsClient) enterWorldNow(t *testing.T, charID string) *mmov1.WorldSnapshot {
	t.Helper()
	w.send(t, &mmov1.EnterWorld{})
	requireWS[*mmov1.WorldSnapshot](t, w, "empty WorldSnapshot", func(ws *mmov1.WorldSnapshot) bool {
		return len(ws.Entities) == 0
	})
	ws := requireWS[*mmov1.WorldSnapshot](t, w, "real WorldSnapshot", func(ws *mmov1.WorldSnapshot) bool {
		return len(ws.Entities) > 0
	})
	if findEntity(ws.Entities, charID) == nil {
		t.Fatalf("%s: real WorldSnapshot lacks character %q: %v", w.name, charID, entityIDs(ws.Entities))
	}
	return ws
}

// enterWorld creates and selects a character, then enters the world.
func (w *wsClient) enterWorld(t *testing.T, username, templateID, name string) (*mmov1.Vec3, string, *mmov1.WorldSnapshot) {
	t.Helper()
	w.connectAndAuth(t, username)
	if cl := w.listCharacters(t); len(cl.Characters) != 0 {
		t.Fatalf("%s: fresh account CharacterList = %d chars, want 0", w.name, len(cl.Characters))
	}
	cc := w.createCharacter(t, templateID, name)
	if !cc.GetOk() || cc.GetCharacter().GetId() == "" {
		t.Fatalf("%s: CreateCharacter = %v, want a character", w.name, cc)
	}
	sc := w.selectCharacter(t, cc.Character.Id)
	if !sc.GetOk() || sc.SpawnPos == nil {
		t.Fatalf("%s: SelectCharacter = %v, want a spawn", w.name, sc)
	}
	if sc.SpawnPos.Y != 25 {
		t.Errorf("%s: SpawnPos.Y = %v, want 25 (hill peak at spawn)", w.name, sc.SpawnPos.Y)
	}
	return sc.SpawnPos, cc.Character.Id, w.enterWorldNow(t, cc.Character.Id)
}
