package server

// Snapshot sink tests: SendSnapshot must deliver a per-player snapshot
// datagram only when the player is registered AND its session has a UDP
// peer bound — never for unknown players or unbound sessions. All over a
// fake PacketConn, so no real sockets are opened.

import (
	"testing"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/session"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

func TestSendSnapshot(t *testing.T) {
	snap := &mmov1.Snapshot{
		Seq: 42,
		Entities: []*mmov1.EntityState{
			{Id: "alice", Pos: &mmov1.Vec2{X: 1, Z: 2}, Velocity: &mmov1.Vec2{X: 3, Z: 4}, Yaw: 0.5},
		},
	}

	cases := []struct {
		name  string
		setup func(t *testing.T, srv *Server, sess *session.Session)
		want  int // expected datagrams written
	}{
		{
			name: "unknown player skipped",
			setup: func(t *testing.T, srv *Server, sess *session.Session) {
				// "alice" never registered in the server map.
			},
			want: 0,
		},
		{
			name: "player without udp peer skipped",
			setup: func(t *testing.T, srv *Server, sess *session.Session) {
				addTestPlayer(t, srv, "alice", sess)
			},
			want: 0,
		},
		{
			name: "bound player receives datagram",
			setup: func(t *testing.T, srv *Server, sess *session.Session) {
				addTestPlayer(t, srv, "alice", sess)
				if err := sess.HandleUDP(sess.UDPToken(), fakeAddr("10.0.0.1:9000")); err != nil {
					t.Fatalf("bind: %v", err)
				}
			},
			want: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			sess := newInWorldSession(t, srv.reg, "alice")
			tc.setup(t, srv, sess)

			if err := srv.SendSnapshot("alice", snap); err != nil {
				t.Fatalf("SendSnapshot: %v", err)
			}
			pc := srv.udp.(*fakePacketConn)
			if got := len(pc.writes); got != tc.want {
				t.Fatalf("datagrams written = %d, want %d", got, tc.want)
			}
			if tc.want == 0 {
				return
			}

			w := pc.writes[0]
			if w.addr.String() != "10.0.0.1:9000" {
				t.Errorf("datagram addr = %v, want the bound peer", w.addr)
			}
			env, err := protocol.DecodeEnvelope(w.b)
			if err != nil {
				t.Fatalf("decode datagram: %v", err)
			}
			if env.Version != testVersion {
				t.Errorf("envelope version = %d, want negotiated %d", env.Version, testVersion)
			}
			msg, err := srv.reg.DecodeMessage(*env)
			if err != nil {
				t.Fatalf("registry decode: %v", err)
			}
			got, ok := msg.(*mmov1.Snapshot)
			if !ok {
				t.Fatalf("datagram carries %T, want *mmov1.Snapshot", msg)
			}
			if got.Seq != 42 {
				t.Errorf("snapshot seq = %d, want 42", got.Seq)
			}
			if len(got.Entities) != 1 || got.Entities[0].Id != "alice" || got.Entities[0].Pos.X != 1 {
				t.Errorf("snapshot entities = %v, want alice at x=1", got.Entities)
			}
		})
	}
}
