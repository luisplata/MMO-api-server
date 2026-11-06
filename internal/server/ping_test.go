package server

import (
	"context"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func waitTick(t *testing.T, sim *game.Simulation, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for sim.Tick() != want {
		if time.Now().After(deadline) {
			t.Fatalf("sim tick = %d, want %d", sim.Tick(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPingFloodDoesNotPerturbTickCadence(t *testing.T) {
	srv, err := New(Config{
		WSSAddr: "127.0.0.1:0", TickRate: game.TickRate, DevAuth: true,
		MinProtoVer: 1, MaxProtoVer: 9,
		Templates: testTemplateRepo(), Characters: testCharacterRepo("alice"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan time.Time)
	go srv.runSim(ctx, wake)
	base := time.UnixMilli(1_700_000_000_000)
	wake <- base
	wake <- base.Add(game.TickInterval)
	waitTick(t, srv.sim, 1)

	probe := clientFrame(t, srv.reg, &mmov1.Ping{Nonce: 1, ClientTime: 1}, 0, 0)
	frames := make([][]byte, 50)
	for i := range frames {
		frames[i] = probe
	}
	hold := make(chan struct{})
	src := &replaySource{frames: frames, hold: hold, drained: make(chan struct{})}
	sink := newTestSink(8)
	defer sink.sink.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.serveConn(ctx, src, &mockTransport{}, sink.sink)
	}()

	<-src.drained
	if got := srv.sim.Tick(); got != 1 {
		t.Fatalf("ping flood advanced the sim tick to %d, want 1", got)
	}
	wake <- base.Add(2 * game.TickInterval)
	waitTick(t, srv.sim, 2)

	close(hold)
	<-done
}
