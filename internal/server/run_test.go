package server

// P13e runtime contract: Run exposes exactly one listener — the private
// WSS upstream. The legacy TCP/UDP defaults the pre-WSS server bound must
// stay unbound and refuse connections.

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
	// legacyTCPAddr is the pre-WSS reliable default (cmd/server used
	// ":8000"); P13d removes that listener and Run must never bind it.
	legacyTCPAddr = "127.0.0.1:8000"
	// legacyUDPAddr is the pre-WSS datagram default (":8001"); P13e
	// removes the UDP runtime, so Run must never bind it.
	legacyUDPAddr = "127.0.0.1:8001"
)

// requireFreeLegacyPorts skips when another process owns a legacy port.
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

// startRun reserves a WSS port and starts Run in the background.
func startRun(t *testing.T, cfg Config) (string, *Server, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cfg.WSSAddr = addr
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	return addr, srv, cancel, done
}

// dialUntilReady polls the WSS URL until Run is accepting.
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

// waitRun cancels the run context and requires a clean, bounded exit.
func waitRun(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// handshake dials the WSS upstream and completes Hello→ServerInfo, proving
// the WSS lifecycle still serves.
func handshake(t *testing.T, addr string, srv *Server) {
	t.Helper()
	c := dialUntilReady(t, "ws://"+addr+wsPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsSend(t, ctx, c, srv, &mmov1.Hello{ProtoVer: testVersion})
	if _, ok := wsRead(t, ctx, c, srv).(*mmov1.ServerInfo); !ok {
		t.Fatal("WSS upstream did not complete the handshake")
	}
}

// TestRunBindsOnlyWSSUpstream pins 13e.1: Run serves WSS and leaves both
// legacy TCP/UDP defaults unbindable.
func TestRunBindsOnlyWSSUpstream(t *testing.T) {
	requireFreeLegacyPorts(t)
	addr, srv, cancel, done := startRun(t, Config{
		WSSOrigins: []string{"allowed.example"},
		TickRate:   game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 9,
	})
	handshake(t, addr, srv)

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

	waitRun(t, cancel, done)
	// Shutdown closed the WSS listener.
	if conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("WSS listener %s still accepts after shutdown", addr)
	}
}
