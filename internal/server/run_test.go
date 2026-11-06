package server

// P13 runtime contract: Run exposes exactly one listener — the private WSS
// upstream. The legacy TCP/UDP ports the pre-WSS server bound by default
// must stay unbound and refuse connections, and cancelling the run context
// must shut every listener and goroutine down.

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/luisplata/mmo-api-server/internal/game"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

const (
	// legacyTCPAddr/legacyUDPAddr are the pre-WSS defaults (cmd/server main
	// used ":8000"/":8001"). P13 removes both listeners; nothing may bind
	// them again.
	legacyTCPAddr = "127.0.0.1:8000"
	legacyUDPAddr = "127.0.0.1:8001"
)

// requireFreeLegacyPorts skips when another process already owns a legacy
// default port, so the refusal assertions below are meaningful rather than
// masked by an unrelated listener.
func requireFreeLegacyPorts(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", legacyTCPAddr)
	if err != nil {
		t.Skipf("legacy TCP default %s is busy on this host: %v", legacyTCPAddr, err)
	}
	_ = ln.Close()
	pc, err := net.ListenPacket("udp", legacyUDPAddr)
	if err != nil {
		t.Skipf("legacy UDP default %s is busy on this host: %v", legacyUDPAddr, err)
	}
	_ = pc.Close()
}

// freeServerAddr reserves and releases a loopback port, returning the
// address Run should bind for the WSS upstream.
func freeServerAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// runServer starts Run in the background and returns its shutdown func and
// completion channel.
func runServer(t *testing.T, srv *Server) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	return cancel, done
}

// dialUntilReady polls the WSS URL until Run is accepting, returning the
// established connection.
func dialUntilReady(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		opts := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {testWSOrigin}}}
		c, _, err := websocket.Dial(ctx, url, opts)
		cancel()
		if err == nil {
			t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("WSS upstream %s never became ready: %v", url, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunBindsOnlyWSSUpstream pins 13.1: Run must serve the configured WSS
// upstream and leave the legacy TCP/UDP defaults unbindable.
func TestRunBindsOnlyWSSUpstream(t *testing.T) {
	requireFreeLegacyPorts(t)
	addr := freeServerAddr(t)
	srv, err := New(Config{
		WSSAddr: addr, WSSOrigins: []string{"allowed.example"},
		TickRate: game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 9,
		Templates: testTemplateRepo(), Characters: testCharacterRepo("alice"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, done := runServer(t, srv)

	c := dialUntilReady(t, "ws://"+addr+wsPath)
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	wsSend(t, ctx, c, srv, &mmov1.Hello{ProtoVer: testVersion})
	if _, ok := wsRead(t, ctx, c, srv).(*mmov1.ServerInfo); !ok {
		t.Fatal("WSS upstream did not complete the handshake")
	}

	// The legacy reliable port must refuse connections: Run is WSS-only.
	if conn, err := net.DialTimeout("tcp", legacyTCPAddr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("legacy TCP %s accepted a connection; Run must bind only the WSS upstream", legacyTCPAddr)
	}

	// The legacy datagram socket must be free: binding it proves no UDP
	// listener holds it.
	pc, err := net.ListenPacket("udp", legacyUDPAddr)
	if err != nil {
		t.Fatalf("legacy UDP %s is held by a listener: %v", legacyUDPAddr, err)
	}
	_ = pc.Close()

	// Graceful shutdown joins every goroutine and closes the listener.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
	if conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("WSS listener %s still accepts after shutdown", addr)
	}
}

// TestRunGracefulShutdownClosesWSS triangulates 13.1 with a live client:
// cancelling the run context closes the socket with 1001 instead of
// leaking the connection.
func TestRunGracefulShutdownClosesWSS(t *testing.T) {
	addr := freeServerAddr(t)
	srv, err := New(Config{
		WSSAddr: addr, WSSOrigins: []string{"allowed.example"},
		TickRate: game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 9,
		Templates: testTemplateRepo(), Characters: testCharacterRepo("alice"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, done := runServer(t, srv)

	c := dialUntilReady(t, "ws://"+addr+wsPath)
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	wsSend(t, ctx, c, srv, &mmov1.Hello{ProtoVer: testVersion})
	if _, ok := wsRead(t, ctx, c, srv).(*mmov1.ServerInfo); !ok {
		t.Fatal("WSS upstream did not complete the handshake")
	}

	cancel()
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("close status = %d, want 1001 (%v)", websocket.CloseStatus(err), err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
