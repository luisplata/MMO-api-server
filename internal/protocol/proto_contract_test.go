package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/luisplata/mmo-api-server/internal/protocol/testfixture"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// roundTripCase describes a fully-populated message and the exact field
// values that must survive a marshal -> unmarshal round-trip.
type roundTripCase struct {
	name   string
	msg    proto.Message
	verify func(t *testing.T, got proto.Message)
}

// testRoundTrip marshals msg, unmarshals into a fresh instance of the
// same type, and runs the case-specific verifier against the decoded
// value. The verifier asserts concrete field values (not identity), so
// the test would fail if any serialization detail regressed.
func testRoundTrip(t *testing.T, tc roundTripCase) {
	t.Helper()
	wire, err := proto.Marshal(tc.msg)
	if err != nil {
		t.Fatalf("marshal %s: %v", tc.name, err)
	}
	decoded := tc.msg.ProtoReflect().New().Interface()
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", tc.name, err)
	}
	tc.verify(t, decoded)
}

// TestMessageRoundTrip covers spec R5 S5.3 (encode/decode identity) for
// every envelope message type in proto/v1/world.proto. Each case asserts
// the SPECIFIC values that were set, so trivial passes are impossible.
func TestMessageRoundTrip(t *testing.T) {
	cases := []roundTripCase{
		{
			name: "Hello",
			msg:  &mmov1.Hello{ProtoVer: 7},
			verify: func(t *testing.T, got proto.Message) {
				if g := got.(*mmov1.Hello).ProtoVer; g != 7 {
					t.Errorf("Hello.protoVer = %d, want 7", g)
				}
			},
		},
		{
			name: "ServerInfo",
			msg:  &mmov1.ServerInfo{ProtoVer: 7, TickRate: 20, ServerTime: 1_782_912_345_678},
			verify: func(t *testing.T, got proto.Message) {
				s := got.(*mmov1.ServerInfo)
				if s.ProtoVer != 7 || s.TickRate != 20 || s.ServerTime != 1_782_912_345_678 {
					t.Errorf("ServerInfo round-trip mismatch: %+v", s)
				}
			},
		},
		{
			name: "VersionMismatch",
			msg:  &mmov1.VersionMismatch{MinVer: 1, MaxVer: 9},
			verify: func(t *testing.T, got proto.Message) {
				v := got.(*mmov1.VersionMismatch)
				if v.MinVer != 1 || v.MaxVer != 9 {
					t.Errorf("VersionMismatch round-trip mismatch: %+v", v)
				}
			},
		},
		{
			name: "AuthRequest",
			msg:  &mmov1.AuthRequest{Username: "alice", Password: "s3cret"},
			verify: func(t *testing.T, got proto.Message) {
				a := got.(*mmov1.AuthRequest)
				if a.Username != "alice" || a.Password != "s3cret" {
					t.Errorf("AuthRequest round-trip mismatch: %+v", a)
				}
			},
		},
		{
			name: "AuthResponse",
			msg: &mmov1.AuthResponse{
				Ok:           true,
				PlayerId:     "plr-001",
				SpawnPos:     &mmov1.Vec2{X: 12.5, Z: -8.25},
				UdpToken:     []byte{0xDE, 0xAD, 0xBE, 0xEF},
				ErrorMessage: "",
			},
			verify: func(t *testing.T, got proto.Message) {
				a := got.(*mmov1.AuthResponse)
				if !a.Ok || a.PlayerId != "plr-001" || string(a.UdpToken) != string([]byte{0xDE, 0xAD, 0xBE, 0xEF}) {
					t.Errorf("AuthResponse round-trip mismatch: %+v", a)
				}
				if a.SpawnPos == nil || a.SpawnPos.X != 12.5 || a.SpawnPos.Z != -8.25 {
					t.Errorf("AuthResponse.spawnPos round-trip mismatch: %+v", a.SpawnPos)
				}
			},
		},
		{
			name: "EnterWorld",
			msg:  &mmov1.EnterWorld{},
			verify: func(t *testing.T, got proto.Message) {
				// Empty message must still round-trip to a zero-length
				// payload that decodes cleanly.
				if g := got.(*mmov1.EnterWorld); g == nil {
					t.Errorf("EnterWorld decoded to nil")
				}
			},
		},
		{
			name: "WorldSnapshot",
			msg: &mmov1.WorldSnapshot{
				Entities: []*mmov1.EntityState{
					{Id: "plr-001", Pos: &mmov1.Vec2{X: 10, Z: 20}, Velocity: &mmov1.Vec2{X: 5, Z: 0}, Yaw: 1.5707964},
					{Id: "plr-002", Pos: &mmov1.Vec2{X: -3, Z: 4}, Velocity: &mmov1.Vec2{X: 0, Z: 2.5}, Yaw: 0},
				},
			},
			verify: func(t *testing.T, got proto.Message) {
				ws := got.(*mmov1.WorldSnapshot)
				if len(ws.Entities) != 2 {
					t.Fatalf("WorldSnapshot has %d entities, want 2", len(ws.Entities))
				}
				e0 := ws.Entities[0]
				if e0.Id != "plr-001" || e0.Pos.X != 10 || e0.Pos.Z != 20 ||
					e0.Velocity.X != 5 || e0.Velocity.Z != 0 || e0.Yaw != 1.5707964 {
					t.Errorf("WorldSnapshot entity[0] round-trip mismatch: %+v", e0)
				}
				e1 := ws.Entities[1]
				if e1.Id != "plr-002" || e1.Pos.X != -3 || e1.Pos.Z != 4 ||
					e1.Velocity.X != 0 || e1.Velocity.Z != 2.5 || e1.Yaw != 0 {
					t.Errorf("WorldSnapshot entity[1] round-trip mismatch: %+v", e1)
				}
			},
		},
		{
			name: "MoveInput",
			msg: &mmov1.MoveInput{
				Seq:   42,
				Dir:   &mmov1.Vec2{X: 0.70710677, Z: 0.70710677},
				Speed: 5,
				Yaw:   -0.7853982,
			},
			verify: func(t *testing.T, got proto.Message) {
				m := got.(*mmov1.MoveInput)
				if m.Seq != 42 || m.Speed != 5 || m.Yaw != -0.7853982 {
					t.Errorf("MoveInput round-trip mismatch: %+v", m)
				}
				if m.Dir == nil || m.Dir.X != 0.70710677 || m.Dir.Z != 0.70710677 {
					t.Errorf("MoveInput.dir round-trip mismatch: %+v", m.Dir)
				}
			},
		},
		{
			name: "Snapshot",
			msg: &mmov1.Snapshot{
				Seq: 9001,
				Entities: []*mmov1.EntityState{
					{Id: "plr-001", Pos: &mmov1.Vec2{X: 100.5, Z: 200.25}, Velocity: &mmov1.Vec2{X: 1, Z: -1}, Yaw: 3.1415927},
					{Id: "plr-002", Pos: &mmov1.Vec2{X: -50, Z: -60}, Velocity: &mmov1.Vec2{X: 0, Z: 0}, Yaw: 4.712389},
					{Id: "plr-003", Pos: &mmov1.Vec2{X: 0, Z: 0}, Velocity: &mmov1.Vec2{X: 2, Z: 2}, Yaw: 0.5},
				},
			},
			verify: func(t *testing.T, got proto.Message) {
				s := got.(*mmov1.Snapshot)
				if s.Seq != 9001 {
					t.Errorf("Snapshot.seq = %d, want 9001", s.Seq)
				}
				if len(s.Entities) != 3 {
					t.Fatalf("Snapshot has %d entities, want 3", len(s.Entities))
				}
				want := []struct {
					id  string
					x   float32
					z   float32
					yaw float32
				}{
					{"plr-001", 100.5, 200.25, 3.1415927},
					{"plr-002", -50, -60, 4.712389},
					{"plr-003", 0, 0, 0.5},
				}
				for i, w := range want {
					e := s.Entities[i]
					if e.Id != w.id || e.Pos.X != w.x || e.Pos.Z != w.z || e.Yaw != w.yaw {
						t.Errorf("Snapshot entity[%d] mismatch: got %+v, want id=%s pos=(%v,%v) yaw=%v", i, e, w.id, w.x, w.z, w.yaw)
					}
				}
			},
		},
		{
			name: "Ack",
			msg:  &mmov1.Ack{Seq: 9001},
			verify: func(t *testing.T, got proto.Message) {
				if g := got.(*mmov1.Ack).Seq; g != 9001 {
					t.Errorf("Ack.seq = %d, want 9001", g)
				}
			},
		},
		{
			name: "SpawnEntity",
			msg: &mmov1.SpawnEntity{
				EntityId: "plr-001",
				State: &mmov1.EntityState{
					Id:       "plr-001",
					Pos:      &mmov1.Vec2{X: 12.5, Z: -8.25},
					Velocity: &mmov1.Vec2{X: 1.5, Z: 0},
					Yaw:      0.7853982,
				},
			},
			verify: func(t *testing.T, got proto.Message) {
				s := got.(*mmov1.SpawnEntity)
				if s.EntityId != "plr-001" {
					t.Errorf("SpawnEntity.entityId = %q, want plr-001", s.EntityId)
				}
				if s.State == nil {
					t.Fatalf("SpawnEntity.state is nil")
				}
				if s.State.Id != "plr-001" || s.State.Pos.X != 12.5 || s.State.Pos.Z != -8.25 ||
					s.State.Velocity.X != 1.5 || s.State.Velocity.Z != 0 || s.State.Yaw != 0.7853982 {
					t.Errorf("SpawnEntity.state round-trip mismatch: %+v", s.State)
				}
			},
		},
		{
			name: "DespawnEntity",
			msg:  &mmov1.DespawnEntity{EntityId: "plr-002"},
			verify: func(t *testing.T, got proto.Message) {
				if g := got.(*mmov1.DespawnEntity).EntityId; g != "plr-002" {
					t.Errorf("DespawnEntity.entityId = %q, want plr-002", g)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testRoundTrip(t, tc)
		})
	}
}

// TestUnknownFieldPreservation_AdditiveMinorBump implements spec R5 S5.3:
// a minor bump adds new fields; an OLD client schema that only knows
// field 1 of ServerInfo must preserve the new unknown fields (2, 3)
// byte-for-byte through a re-marshal, so behavior is unchanged when old
// and new versions interoperate.
func TestUnknownFieldPreservation_AdditiveMinorBump(t *testing.T) {
	full := &mmov1.ServerInfo{ProtoVer: 7, TickRate: 20, ServerTime: 123}
	wire, err := proto.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full ServerInfo: %v", err)
	}

	// Old client decodes with the MiniServerInfo schema (field 1 only).
	old := &testfixture.MiniServerInfo{}
	if err := proto.Unmarshal(wire, old); err != nil {
		t.Fatalf("old schema unmarshal: %v", err)
	}
	if old.ProtoVer != 7 {
		t.Errorf("old schema decoded protoVer = %d, want 7", old.ProtoVer)
	}

	// Old client re-marshals: fields 2 and 3 are unknown to it and must
	// be preserved verbatim in the output.
	rewire, err := proto.Marshal(old)
	if err != nil {
		t.Fatalf("old schema re-marshal: %v", err)
	}

	// New client decodes the old client's output: nothing may be lost.
	full2 := &mmov1.ServerInfo{}
	if err := proto.Unmarshal(rewire, full2); err != nil {
		t.Fatalf("new schema unmarshal of old output: %v", err)
	}
	if full2.ProtoVer != 7 {
		t.Errorf("protoVer lost through old schema: got %d, want 7", full2.ProtoVer)
	}
	if full2.TickRate != 20 {
		t.Errorf("unknown field 2 lost through old schema: got %d, want 20", full2.TickRate)
	}
	if full2.ServerTime != 123 {
		t.Errorf("unknown field 3 lost through old schema: got %d, want 123", full2.ServerTime)
	}
}

// TestEntityStateShapeNoPitchRoll pins design D3 / spec R16 S16.1 at the
// contract level: EntityState carries id, pos, velocity and yaw — and
// pitch/roll fields must NOT exist (structurally impossible to network
// the camera rotation). The field set is pinned exactly, so accidental
// additions also fail this test.
func TestEntityStateShapeNoPitchRoll(t *testing.T) {
	fields := (&mmov1.EntityState{}).ProtoReflect().Descriptor().Fields()
	want := []string{"id", "pos", "velocity", "yaw"}
	if fields.Len() != len(want) {
		t.Fatalf("EntityState has %d fields, want exactly %d (%v)", fields.Len(), len(want), want)
	}
	for i, w := range want {
		fd := fields.ByNumber(protoreflect.FieldNumber(i + 1))
		if fd == nil {
			t.Fatalf("EntityState field number %d missing", i+1)
		}
		got := string(fd.Name())
		if got != w {
			t.Errorf("EntityState field %d = %q, want %q", i+1, got, w)
		}
	}
	for i := 0; i < fields.Len(); i++ {
		fd := fields.ByNumber(protoreflect.FieldNumber(i + 1))
		name := strings.ToLower(string(fd.Name()))
		if strings.Contains(name, "pitch") || strings.Contains(name, "roll") {
			t.Errorf("EntityState must not contain pitch/roll fields, found %q", name)
		}
	}
}
