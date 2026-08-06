package protocol

// Registry tests (design D2, spec R6).
//
// Coverage: S6.1 registry completeness vs the type-id table documented
// in the proto/v1/world.proto header, and S6.2 unknown dispatch in both
// directions (encode and decode).

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/protocol/testfixture"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// typeIDRow is one parsed row of the .proto header type-id table.
type typeIDRow struct {
	id   uint16
	name string
}

// typeTablePattern matches the header table rows of world.proto:
//
//	//	 1  Hello
//	//	 2  ServerInfo
//	...
var typeTablePattern = regexp.MustCompile(`(?m)^\s*//\s*(\d+)\s+([A-Za-z]\w*)\s*$`)

// protoTypeIDTable reads the type-id table documented in the header of
// proto/v1/world.proto — the source of truth the registry must mirror.
func protoTypeIDTable(t *testing.T) []typeIDRow {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "proto", "v1", "world.proto"))
	if err != nil {
		t.Fatalf("read world.proto: %v", err)
	}
	var rows []typeIDRow
	for _, line := range strings.Split(string(src), "\n") {
		m := typeTablePattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("parse id %q: %v", m[1], err)
		}
		rows = append(rows, typeIDRow{id: uint16(id), name: m[2]})
	}
	if len(rows) < 10 {
		t.Fatalf("expected at least the 10 contract types in world.proto header, parsed %d", len(rows))
	}
	return rows
}

// TestWorldRegistryCompleteness covers S6.1: every message defined in
// the .proto contract has a unique registered id, and the registry has
// no extra types the .proto does not declare.
func TestWorldRegistryCompleteness(t *testing.T) {
	rows := protoTypeIDTable(t)
	reg := NewWorldRegistry()

	seen := map[uint16]bool{}
	for _, row := range rows {
		if seen[row.id] {
			t.Errorf("duplicate type id in .proto table: %d", row.id)
		}
		seen[row.id] = true

		msg, err := reg.NewMessage(row.id)
		if err != nil {
			t.Errorf("type id %d (%s) not registered: %v", row.id, row.name, err)
			continue
		}
		got := string(msg.ProtoReflect().Descriptor().Name())
		if got != row.name {
			t.Errorf("type id %d maps to %q, want %q", row.id, got, row.name)
		}
	}

	// Registry must contain exactly the table ids (no extra, none missing).
	if got := reg.Len(); got != len(rows) {
		t.Errorf("registry has %d types, want %d", got, len(rows))
	}
}

// TestRegistryMessageRoundTripDispatch exercises the registry encode and
// decode directions for every contract type: EncodeMessage produces a
// frame whose envelope type matches the message, and DecodeMessage
// rebuilds a message equal to the original. This is the round-trip glue
// between the envelope codec (S2.1) and the registry (S6.1).
func TestRegistryMessageRoundTripDispatch(t *testing.T) {
	reg := NewWorldRegistry()
	cases := []struct {
		name string
		id   uint16
		msg  proto.Message
	}{
		{"Hello", 1, &mmov1.Hello{ProtoVer: 7}},
		{"ServerInfo", 2, &mmov1.ServerInfo{ProtoVer: 7, TickRate: 20, ServerTime: 123}},
		{"AuthRequest", 3, &mmov1.AuthRequest{Username: "alice", Password: "pw"}},
		{"AuthResponse", 4, &mmov1.AuthResponse{Ok: true, PlayerId: "p1", SpawnPos: &mmov1.Vec2{X: 1, Z: 2}, UdpToken: []byte{1, 2, 3}}},
		{"EnterWorld", 5, &mmov1.EnterWorld{}},
		{"WorldSnapshot", 6, &mmov1.WorldSnapshot{Entities: []*mmov1.EntityState{{Id: "p1"}}}},
		{"MoveInput", 7, &mmov1.MoveInput{Seq: 1, Dir: &mmov1.Vec2{X: 0.5, Z: 0.5}, Speed: 3, Yaw: 1.2}},
		{"Snapshot", 8, &mmov1.Snapshot{Seq: 2, Entities: []*mmov1.EntityState{{Id: "p1", Pos: &mmov1.Vec2{X: 4, Z: 5}}}}},
		{"VersionMismatch", 9, &mmov1.VersionMismatch{MinVer: 1, MaxVer: 9}},
		{"Ack", 10, &mmov1.Ack{Seq: 42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := reg.EncodeMessage(tc.msg, Envelope{Version: 1, Flags: 0, Seq: 1})
			if err != nil {
				t.Fatalf("EncodeMessage: %v", err)
			}
			env, err := DecodeEnvelope(frame)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}
			if env.Type != tc.id {
				t.Errorf("frame type = %d, want %d", env.Type, tc.id)
			}
			got, err := reg.DecodeMessage(*env)
			if err != nil {
				t.Fatalf("DecodeMessage: %v", err)
			}
			if !proto.Equal(got, tc.msg) {
				t.Errorf("decoded %v, want %v", got, tc.msg)
			}
		})
	}
}

// TestRegistryUnknownDispatchBothDirections covers S6.2: an unregistered
// type id fails in the encode direction (message not in registry) and in
// the decode direction (envelope type not registered).
func TestRegistryUnknownDispatchBothDirections(t *testing.T) {
	reg := NewWorldRegistry()

	// Decode direction: unregistered type id.
	env := Envelope{Version: 1, Type: 99, Flags: 0, Seq: 1, Payload: nil}
	if _, err := reg.DecodeMessage(env); !errors.Is(err, ErrUnknownType) {
		t.Errorf("DecodeMessage(unknown type) err = %v, want ErrUnknownType", err)
	}

	// Encode direction: a well-formed proto message that is NOT part of
	// the world contract (the test fixture MiniServerInfo) must be
	// rejected as unknown.
	unknown := &testfixture.MiniServerInfo{ProtoVer: 7}
	if _, err := reg.EncodeMessage(unknown, Envelope{Version: 1, Flags: 0, Seq: 1}); !errors.Is(err, ErrUnknownType) {
		t.Errorf("EncodeMessage(unknown msg) err = %v, want ErrUnknownType", err)
	}
}

// TestWorldRegistryIDsContiguous pins the ids 1..10 (the table rows are
// contiguous by design) so a dropped row cannot hide behind a count-only
// assertion.
func TestWorldRegistryIDsContiguous(t *testing.T) {
	reg := NewWorldRegistry()
	rows := protoTypeIDTable(t)
	want := uint16(len(rows))
	for id := uint16(1); id <= want; id++ {
		if _, err := reg.NewMessage(id); err != nil {
			t.Errorf("type id %d must be registered (contiguous table), err = %v", id, err)
		}
	}
}
