package server

// P13d runtime contract: Run no longer exposes the legacy reliable TCP
// lifecycle listener; WSS still serves and UDP survives until P13e.

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
	// removes it, so P13d still binds it when configured.
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

// TestRunRefusesLegacyTCPAndRetainsUDP pins 13d.1: Run serves WSS,
// refuses legacy TCP, and still binds legacy UDP (P13e removes it).
func TestRunRefusesLegacyTCPAndRetainsUDP(t *testing.T) {
	requireFreeLegacyPorts(t)
	addr, srv, cancel, done := startRun(t, Config{
		UDPAddr: legacyUDPAddr, WSSOrigins: []string{"allowed.example"},
		TickRate: game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 9,
	})
	handshake(t, addr, srv)

	if conn, err := net.DialTimeout("tcp", legacyTCPAddr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("legacy TCP %s accepted a connection; Run must bind only the WSS upstream", legacyTCPAddr)
	}
	// The legacy datagram socket is still held by Run until P13e.
	if pc, err := net.ListenPacket("udp", legacyUDPAddr); err == nil {
		_ = pc.Close()
		t.Fatalf("legacy UDP %s is free; P13d still runs the UDP loop", legacyUDPAddr)
	}

	waitRun(t, cancel, done)
	// Shutdown released both listeners.
	if pc, err := net.ListenPacket("udp", legacyUDPAddr); err != nil {
		t.Fatalf("legacy UDP %s still held after shutdown: %v", legacyUDPAddr, err)
	} else {
		_ = pc.Close()
	}
	if conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("WSS listener %s still accepts after shutdown", addr)
	}
}

// TestRunWithoutUDPLeavesLegacyPortsFree triangulates the same contract
// with UDPAddr empty: Run binds neither legacy default while the WSS
// upstream serves.
func TestRunWithoutUDPLeavesLegacyPortsFree(t *testing.T) {
	requireFreeLegacyPorts(t)
	addr, srv, cancel, done := startRun(t, Config{
		WSSOrigins: []string{"allowed.example"},
		TickRate:   game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 9,
	})
	handshake(t, addr, srv)

	if srv.udp != nil {
		t.Fatalf("udp socket = %v, want nil with UDPAddr empty", srv.udp)
	}
	if conn, err := net.DialTimeout("tcp", legacyTCPAddr, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("legacy TCP %s accepted a connection with UDPAddr empty", legacyTCPAddr)
	}
	pc, err := net.ListenPacket("udp", legacyUDPAddr)
	if err != nil {
		t.Fatalf("legacy UDP %s is held although UDPAddr is empty: %v", legacyUDPAddr, err)
	}
	_ = pc.Close()

	waitRun(t, cancel, done)
}
