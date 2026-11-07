package server

// P18 wiring test: a served connection emits redacted JSON telemetry for
// every reliable message (in and out), with decoded payloads only when the
// logger opts in. Credentials never reach the output.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/observability"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestServeConnEmitsTelemetry(t *testing.T) {
	var buf bytes.Buffer
	srv, err := New(Config{
		WSSAddr: "127.0.0.1:0", TickRate: game.TickRate, DevAuth: true,
		MinProtoVer: 1, MaxProtoVer: 9,
		Templates:  testTemplateRepo(),
		Characters: testCharacterRepo("alice"),
		Logger:     observability.NewLogger(&buf, true, time.Now),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src := &replaySource{frames: [][]byte{
		clientFrame(t, srv.reg, &mmov1.Hello{ProtoVer: testVersion}, 0, 0),
		clientFrame(t, srv.reg, &mmov1.AuthRequest{Username: "alice", Password: "hunter2"}, 0, 0),
		clientFrame(t, srv.reg, &mmov1.JoinRoom{Mode: protocol.JoinRoomNextAvailable}, 0, 0),
	}}
	srv.serveConn(context.Background(), src, &mockTransport{}, nil)

	byType := map[float64]map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("decode telemetry %q: %v", line, err)
		}
		byType[ev["typeID"].(float64)] = ev
	}
	if got := byType[21]; got["dir"] != "in" || got["accountID"] != "alice" ||
		got["sessionID"] == "" || got["roomID"] != "default" || got["joinMode"] != float64(1) {
		t.Errorf("JoinRoom event = %v, want inbound alice/default with joinMode 1", got)
	}
	if got := byType[22]; got["dir"] != "out" || got["roomID"] != "default" {
		t.Errorf("JoinRoomResponse event = %v, want outbound roomID default", got)
	}
	if strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), `"password"`) {
		t.Errorf("credential leaked in telemetry: %s", buf.String())
	}
}
