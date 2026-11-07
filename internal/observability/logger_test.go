package observability

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func jsonEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("decode telemetry line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}
func fixedLogger(now time.Time) (*Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return NewLogger(buf, false, func() time.Time { return now }), buf
}

func TestMessageEventMetadata(t *testing.T) {
	now := time.Unix(1000, 0)
	l, buf := fixedLogger(now)
	l.Message(MessageEvent{
		SessionID: "sess-1", AccountID: "alice", RoomID: "default",
		CharacterID: "char-1", Direction: Inbound, TypeID: 7,
		Size: 42, Seq: 9, RTTms: 12,
	})
	events := jsonEvents(t, buf)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	want := map[string]any{
		"event": "message", "ts": float64(now.UnixMilli()),
		"sessionID": "sess-1", "accountID": "alice", "roomID": "default",
		"characterID": "char-1", "dir": "in", "typeID": float64(7),
		"size": float64(42), "seq": float64(9), "rttMs": float64(12),
	}
	for k, v := range want {
		if got := events[0][k]; got != v {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
}

func TestSnapshotAggregatePerSecond(t *testing.T) {
	base := time.Unix(3000, 0)
	l, buf := fixedLogger(base)
	l.SnapshotTick(base)
	for i := 0; i < 20; i++ {
		l.Snapshot()
	}
	l.SnapshotTick(base.Add(500 * time.Millisecond))
	if events := jsonEvents(t, buf); len(events) != 0 {
		t.Fatalf("mid-window events = %v, want none", events)
	}
	l.SnapshotTick(base.Add(time.Second))
	events := jsonEvents(t, buf)
	if len(events) != 1 || events[0]["event"] != "snapshot_aggregate" ||
		events[0]["count"] != float64(20) || events[0]["windowMs"] != float64(1000) {
		t.Fatalf("aggregate = %v, want count 20 windowMs 1000", events)
	}
	l.Snapshot()
	l.Snapshot()
	l.SnapshotTick(base.Add(1500 * time.Millisecond))
	l.SnapshotTick(base.Add(2 * time.Second))
	events = jsonEvents(t, buf)
	if len(events) != 2 || events[1]["count"] != float64(2) {
		t.Fatalf("events = %v, want a second aggregate with count 2", events)
	}
}

func TestPayloadPolicy(t *testing.T) {
	ev := MessageEvent{SessionID: "sess-1", Direction: Inbound, TypeID: 21, Payload: map[string]any{
		"joinMode": 1, "roomId": "default", "characterId": "char-1",
		"password": "hunter2", "udpToken": "SEKRET-TOKEN", "debug": "internal",
	}}
	offBuf := &bytes.Buffer{}
	NewLogger(offBuf, false, time.Now).Message(ev)
	onBuf := &bytes.Buffer{}
	NewLogger(onBuf, true, time.Now).Message(ev)
	on := jsonEvents(t, onBuf)[0]
	if bytes.Contains(offBuf.Bytes(), []byte(`"joinMode"`)) || bytes.Contains(offBuf.Bytes(), []byte(`"roomId"`)) {
		t.Errorf("payload emitted without -log-payloads: %s", offBuf.String())
	}
	if on["joinMode"] != float64(1) || on["roomId"] != "default" || on["characterId"] != "char-1" {
		t.Errorf("allowlisted payload = %v, want joinMode/roomId/characterId", on)
	}
	for _, key := range []string{"password", "udpToken", "debug"} {
		if _, ok := on[key]; ok {
			t.Errorf("non-allowlisted key %q was emitted", key)
		}
	}
	for _, buf := range []*bytes.Buffer{offBuf, onBuf} {
		if bytes.Contains(buf.Bytes(), []byte("hunter2")) || bytes.Contains(buf.Bytes(), []byte("SEKRET-TOKEN")) {
			t.Errorf("credential leaked: %s", buf.String())
		}
	}
}
