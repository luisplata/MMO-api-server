// Package observability writes redacted structured telemetry: one JSON
// event per reliable message plus a per-second snapshot aggregate.
package observability

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Direction is the wire direction of a logged frame.
type Direction string

const Inbound Direction = "in"

const Outbound Direction = "out"

// MessageEvent carries typed scalars only: a proto.Message or raw frame is
// never stored, so unknown wire fields cannot leak into logs.
type MessageEvent struct {
	SessionID, AccountID, RoomID, CharacterID string
	Direction                                 Direction
	TypeID                                    uint16
	Size                                      int
	Seq                                       uint32
	RTTms                                     int64
	Payload                                   map[string]any // -log-payloads only
}

// payloadAllowlist is the explicit order -log-payloads may emit.
var payloadAllowlist = []string{"joinMode", "roomId", "characterId", "nonce", "clientTime"}

// credentialKeys are dropped before the allowlist, even if mislabelled.
var credentialKeys = map[string]struct{}{
	"password": {}, "udptoken": {}, "udp_token": {}, "token": {},
	"secret": {}, "authorization": {}, "credential": {}, "credentials": {},
}

// Logger is safe for concurrent Message calls; snapshot state is owned by
// the room-owner goroutine.
type Logger struct {
	log      *slog.Logger
	payloads bool
	now      func() time.Time
	mu       sync.Mutex
	snaps    int
	last     time.Time
}

// NewLogger builds a JSON logger; now is injectable for deterministic ts.
func NewLogger(w io.Writer, logPayloads bool, now func() time.Time) *Logger {
	if now == nil {
		now = time.Now
	}
	return &Logger{log: slog.New(slog.NewJSONHandler(w, nil)), payloads: logPayloads, now: now}
}

// Message emits one reliable-message event with the fixed metadata set.
func (l *Logger) Message(ev MessageEvent) {
	if l == nil {
		return
	}
	args := []any{
		"event", "message", "ts", l.now().UnixMilli(),
		"sessionID", ev.SessionID, "accountID", ev.AccountID,
		"roomID", ev.RoomID, "characterID", ev.CharacterID,
		"dir", string(ev.Direction), "typeID", ev.TypeID,
		"size", ev.Size, "seq", ev.Seq, "rttMs", ev.RTTms,
	}
	if l.payloads {
		for _, key := range payloadAllowlist {
			if _, secret := credentialKeys[strings.ToLower(key)]; secret {
				continue
			}
			if v, ok := ev.Payload[key]; ok {
				args = append(args, key, v)
			}
		}
	}
	l.log.Info("message", args...)
}

// PayloadsEnabled reports whether the wiring should decode payloads at all.
func (l *Logger) PayloadsEnabled() bool { return l != nil && l.payloads }

// Snapshot records one delivered realtime snapshot for the aggregate.
func (l *Logger) Snapshot() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.snaps++
	l.mu.Unlock()
}

// SnapshotTick emits one aggregate per elapsed second with snapshots; the
// first call starts the window and empty windows stay silent.
func (l *Logger) SnapshotTick(now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.last = now
		return
	}
	if now.Sub(l.last) < time.Second || l.snaps == 0 {
		return
	}
	l.log.Info("snapshot", "event", "snapshot_aggregate", "ts", now.UnixMilli(),
		"count", l.snaps, "windowMs", now.Sub(l.last).Milliseconds())
	l.snaps, l.last = 0, now
}
