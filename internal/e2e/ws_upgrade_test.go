package e2e

// P9 proxy-path proof: a browser-like client terminates TLS at a front
// proxy, and the proxy forwards the WebSocket Upgrade to the private,
// plain-HTTP Go upstream. TLS and certificates live only in the proxy;
// the game process never loads them. The test asserts the 101 upgrade,
// the full lifecycle through the proxy, binary envelope passthrough, the
// Origin allowlist surviving the hop, and the 64 KiB (1009) read limit.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/server"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

const (
	proxyWSOrigin         = "https://game.example.com"
	proxyDisallowedOrigin = "https://evil.example"
	proxyAllowedHost      = "game.example.com"
	proxyWSPath           = "/ws"
	proxyReadLimit        = 64 << 10
	proxyDialTimeout      = 15 * time.Second
)

// startProxyUpstream boots the real server with a WSS-only private listener on
// loopback (the deployment's plain upstream) and returns the WSS URL
// browsers dial plus a shutdown that waits for Run to return. Transport
// legacy listeners are not started.
func startProxyUpstream(t *testing.T) (wssURL string, shutdown func()) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		addr := freeLoopbackAddr(t)
		srv, err := server.New(server.Config{
			WSSAddr: addr, WSSOrigins: []string{proxyAllowedHost},
			TickRate: game.TickRate, DevAuth: true,
			MinProtoVer: protoVersion, MaxProtoVer: protoVersion,
			SpawnX: testSpawnX, SpawnZ: testSpawnZ,
			Heights:    fixtureHeightfield(t),
			Templates:  e2eTemplateRepo(t),
			Characters: e2eCharacterRepo(t),
		})
		if err != nil {
			t.Fatalf("server.New: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Run(ctx) }()
		select {
		case err := <-done:
			cancel()
			if attempt < 5 {
				continue
			}
			t.Fatalf("server.Run exited before serving: %v", err)
		case <-time.After(100 * time.Millisecond):
			return "ws://" + addr + proxyWSPath, func() { cancel(); <-done }
		}
	}
}

// startTLSProxy terminates TLS in front of the private upstream,
// mirroring the operational nginx block: HTTP/1.1, Upgrade/Connection
// forwarded, and the public Host + Origin preserved. The upstream is the
// plain-HTTP origin backing the WSS URL (TLS never reaches the server).
func startTLSProxy(t *testing.T, wssURL string) *httptest.Server {
	t.Helper()
	u, err := url.Parse(wssURL)
	if err != nil {
		t.Fatalf("parse WSS URL %q: %v", wssURL, err)
	}
	target := &url.URL{Scheme: "http", Host: u.Host}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		host := r.Host
		origin := r.Header.Get("Origin")
		upgrade := r.Header.Get("Upgrade")
		connection := r.Header.Get("Connection")
		base(r)
		// The operational contract: forward the Upgrade/Connection
		// pair and keep the public Host + Origin the browser sent.
		if upgrade != "" {
			r.Header.Set("Upgrade", upgrade)
		}
		if connection != "" {
			r.Header.Set("Connection", connection)
		}
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
	}
	front := httptest.NewTLSServer(proxy)
	t.Cleanup(front.Close)
	return front
}

func proxyWSSURL(front *httptest.Server) string {
	return "wss" + strings.TrimPrefix(front.URL, "https") + proxyWSPath
}

func dialProxiedWS(t *testing.T, ctx context.Context, front *httptest.Server, origin string) (*websocket.Conn, *http.Response) {
	t.Helper()
	c, resp, err := websocket.Dial(ctx, proxyWSSURL(front), &websocket.DialOptions{
		HTTPClient: front.Client(),
		HTTPHeader: http.Header{"Origin": {origin}},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial through TLS proxy: status=%d err=%v", status, err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
	return c, resp
}

func proxySend(t *testing.T, ctx context.Context, c *websocket.Conn, reg *protocol.Registry, msg proto.Message) {
	t.Helper()
	frame, err := reg.EncodeMessage(msg, protocol.Envelope{Version: protoVersion})
	if err != nil {
		t.Fatalf("encode %T: %v", msg, err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write %T: %v", msg, err)
	}
}

func proxyRead(t *testing.T, ctx context.Context, c *websocket.Conn, reg *protocol.Registry) proto.Message {
	t.Helper()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read through proxy: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type = %v, want binary", typ)
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	msg, err := reg.DecodeMessage(*env)
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return msg
}

func proxyRound(t *testing.T, ctx context.Context, c *websocket.Conn, reg *protocol.Registry, msg proto.Message) proto.Message {
	t.Helper()
	proxySend(t, ctx, c, reg, msg)
	return proxyRead(t, ctx, c, reg)
}

// enterWorldProxy drives the same Hello→Auth→List→Create→Select→
// EnterWorld sequence ws_test uses, but every frame crosses the TLS
// proxy. It returns the first real world snapshot and the character id.
func enterWorldProxy(t *testing.T, ctx context.Context, c *websocket.Conn, reg *protocol.Registry) (*mmov1.WorldSnapshot, string) {
	t.Helper()
	if _, ok := proxyRound(t, ctx, c, reg, &mmov1.Hello{ProtoVer: protoVersion}).(*mmov1.ServerInfo); !ok {
		t.Fatal("want ServerInfo")
	}
	if _, ok := proxyRound(t, ctx, c, reg, &mmov1.AuthRequest{Username: "proxy", Password: "pw"}).(*mmov1.AuthResponse); !ok {
		t.Fatal("want AuthResponse")
	}
	if _, ok := proxyRound(t, ctx, c, reg, &mmov1.ListCharacters{}).(*mmov1.CharacterList); !ok {
		t.Fatal("want CharacterList")
	}
	created, ok := proxyRound(t, ctx, c, reg, &mmov1.CreateCharacter{Name: "ProxyHero", TemplateId: "warrior"}).(*mmov1.CreateCharacterResponse)
	if !ok || !created.GetOk() {
		t.Fatalf("create = %v", created)
	}
	id := created.GetCharacter().GetId()
	selected, ok := proxyRound(t, ctx, c, reg, &mmov1.SelectCharacter{CharacterId: id}).(*mmov1.SelectCharacterResponse)
	if !ok || !selected.GetOk() {
		t.Fatalf("select = %v", selected)
	}
	proxySend(t, ctx, c, reg, &mmov1.EnterWorld{})
	if _, ok := proxyRead(t, ctx, c, reg).(*mmov1.WorldSnapshot); !ok {
		t.Fatal("missing empty handshake snapshot")
	}
	snap, ok := proxyRead(t, ctx, c, reg).(*mmov1.WorldSnapshot)
	if !ok {
		t.Fatal("missing real WorldSnapshot")
	}
	return snap, id
}

func TestWSSThroughTLSProxy(t *testing.T) {
	wssURL, shutdown := startProxyUpstream(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	ctx, cancel := context.WithTimeout(context.Background(), proxyDialTimeout)
	t.Cleanup(cancel)
	reg := protocol.NewWorldRegistry()

	c, resp := dialProxiedWS(t, ctx, front, proxyWSOrigin)
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %v, want 101", resp)
	}
	snap, id := enterWorldProxy(t, ctx, c, reg)
	if len(snap.GetEntities()) != 1 || snap.GetEntities()[0].GetId() != id {
		t.Fatalf("snapshot entities = %v, want only %q", snap.GetEntities(), id)
	}
	// Downstream binary passthrough: the live 20 Hz tick reaches the
	// browser through the proxy as a real Snapshot frame.
	if _, ok := proxyRead(t, ctx, c, reg).(*mmov1.Snapshot); !ok {
		t.Fatal("no realtime Snapshot through the proxy")
	}
}

func TestWSSThroughTLSProxyRejectsDisallowedOrigin(t *testing.T) {
	wssURL, shutdown := startProxyUpstream(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	ctx, cancel := context.WithTimeout(context.Background(), proxyDialTimeout)
	t.Cleanup(cancel)

	_, resp, err := websocket.Dial(ctx, proxyWSSURL(front), &websocket.DialOptions{
		HTTPClient: front.Client(),
		HTTPHeader: http.Header{"Origin": {proxyDisallowedOrigin}},
	})
	if err == nil {
		t.Fatal("disallowed Origin upgraded through the proxy")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestWSSThroughTLSProxyReadLimit(t *testing.T) {
	wssURL, shutdown := startProxyUpstream(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	ctx, cancel := context.WithTimeout(context.Background(), proxyDialTimeout)
	t.Cleanup(cancel)

	c, resp := dialProxiedWS(t, ctx, front, proxyWSOrigin)
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %v, want 101", resp)
	}
	if err := c.Write(ctx, websocket.MessageBinary, make([]byte, proxyReadLimit+1)); err != nil {
		t.Fatalf("write oversize frame: %v", err)
	}
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
				t.Fatalf("close status = %d, want 1009 (%v)", got, err)
			}
			return
		}
	}
}
