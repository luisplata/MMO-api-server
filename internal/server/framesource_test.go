package server

// P1 inbound seam test: the per-connection read loop consumes a
// FrameSource instead of reading net.Conn directly. A fake source
// replays the handshake→world frame sequence, the session reaches
// in-world (the real WorldSnapshot is written), and a clean close on EOF
// tears the player back down.

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/network"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// replaySource is a fake FrameSource that replays a pre-encoded frame
// sequence in order, then reports io.EOF. When hold is non-nil the source
// blocks on it before returning EOF, so a test can keep the connection
// alive (letting the async sink writer deliver its frames) and release
// the teardown deterministically.
type replaySource struct {
	frames [][]byte
	i      int
	hold   chan struct{}
}

func (r *replaySource) Next() ([]byte, error) {
	if r.i >= len(r.frames) {
		if r.hold != nil {
			<-r.hold
		}
		return nil, io.EOF
	}
	f := r.frames[r.i]
	r.i++
	return f, nil
}

var _ network.FrameSource = (*replaySource)(nil)

// TestServeConnConsumesFrameSource replays Hello→Auth→Select→EnterWorld
// through a FrameSource, asserts the session reaches in-world and the
// real WorldSnapshot reaches the client, then that EOF cleans up.
func TestServeConnConsumesFrameSource(t *testing.T) {
	srv, err := New(Config{
		TCPAddr: ":8000", UDPAddr: ":8001",
		TickRate:    game.TickRate,
		DevAuth:     true,
		MinProtoVer: 1, MaxProtoVer: 9,
		Templates:  testTemplateRepo(),
		Characters: testCharacterRepo("alice"),
		Spawn:      &fakeSpawnResolver{spawn: mmov1.Vec3{X: 1, Y: 0, Z: 2}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	frames := startFrameReader(t, clientConn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.runSim(ctx, make(chan time.Time))

	hold := make(chan struct{})
	src := &replaySource{hold: hold, frames: [][]byte{
		clientFrame(t, srv.reg, &mmov1.Hello{ProtoVer: testVersion}, 0, 0),
		clientFrame(t, srv.reg, &mmov1.AuthRequest{Username: "alice", Password: "pw"}, 0, 0),
		clientFrame(t, srv.reg, &mmov1.SelectCharacter{CharacterId: testCharID}, 0, 0),
		clientFrame(t, srv.reg, &mmov1.EnterWorld{}, 0, 0),
	}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.serveConn(ctx, src, serverConn)
	}()

	// ServerInfo, AuthResponse, SelectCharacterResponse, empty handshake
	// WorldSnapshot, then the REAL WorldSnapshot (non-empty). The real one
	// is delivered asynchronously by the player's sink (design P3), so the
	// source blocks here — keeping the conn alive — until it is observed.
	for i := 0; i < 4; i++ {
		nextFrame(t, frames)
	}
	env, msg := decodeFrame(t, srv, nextFrame(t, frames))
	if env.Version != testVersion {
		t.Errorf("WorldSnapshot envelope version = %d, want %d", env.Version, testVersion)
	}
	ws, ok := msg.(*mmov1.WorldSnapshot)
	if !ok {
		t.Fatalf("frame carries %T, want *mmov1.WorldSnapshot", msg)
	}
	if len(ws.Entities) != 1 || ws.Entities[0].Id != testCharID {
		t.Errorf("real WorldSnapshot = %v, want exactly %q", ws.Entities, testCharID)
	}

	// EOF: release the source, serveConn cleans up (session closed, player
	// removed) and returns.
	close(hold)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("serveConn did not return after EOF")
	}
	if srv.isRegistered(testCharID) {
		t.Errorf("%q still registered after clean close on EOF", testCharID)
	}
}
