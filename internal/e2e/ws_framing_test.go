package e2e

// P12 framing proof (spec realtime-web-transport: WSS único, 1 WS = 1
// envelope): the WebSocket message boundary IS the envelope boundary.
// This is the WSS-only replacement for the retired TCP/UDP
// channel-separation test: a binary message carries exactly one raw
// envelope (no TCP length prefix, no batching), and the server never
// scans one message for a second envelope.

import (
	"context"
	"encoding/binary"
	"net/http"
	"testing"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/protocol"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// readOneEnvelope reads one binary message and proves it carries exactly
// one raw envelope whose payload is one complete protobuf message. A TCP
// length prefix would break the magic check; a second concatenated
// envelope would make the payload longer than the decoded message.
func readOneEnvelope(t *testing.T, ctx context.Context, c *websocket.Conn, reg *protocol.Registry) proto.Message {
	t.Helper()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read through proxy: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type = %v, want binary", typ)
	}
	if len(data) < protocol.HeaderSize {
		t.Fatalf("message = %d bytes, want at least the %d-byte envelope header", len(data), protocol.HeaderSize)
	}
	if magic := binary.BigEndian.Uint16(data); magic != protocol.Magic {
		t.Fatalf("message starts %#x, want envelope magic %#x (no length prefix)", magic, protocol.Magic)
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	msg, err := reg.DecodeMessage(*env)
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if got, want := len(env.Payload), proto.Size(msg); got != want {
		t.Fatalf("%T payload = %d bytes, want exactly one message (%d bytes)", msg, got, want)
	}
	return msg
}

// TestWSSFramingOneMessageOneEnvelope pins both directions of the framing
// contract: one outbound message holds one envelope, and two envelopes
// concatenated into one inbound message are NOT processed as two requests.
func TestWSSFramingOneMessageOneEnvelope(t *testing.T) {
	wssURL, shutdown := startServer(t)
	t.Cleanup(shutdown)
	front := startTLSProxy(t, wssURL)
	ctx, cancel := context.WithTimeout(context.Background(), proxyDialTimeout)
	t.Cleanup(cancel)
	reg := protocol.NewWorldRegistry()

	c, resp := dialProxiedWS(t, ctx, front, proxyWSOrigin)
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %v, want 101", resp)
	}

	// Outbound: each reply is one envelope in one message. Two different
	// response sizes (ServerInfo, AuthResponse) triangulate the payload
	// check instead of matching one fixed message.
	proxySend(t, ctx, c, reg, &mmov1.Hello{ProtoVer: protoVersion})
	if _, ok := readOneEnvelope(t, ctx, c, reg).(*mmov1.ServerInfo); !ok {
		t.Fatal("want ServerInfo")
	}
	proxySend(t, ctx, c, reg, &mmov1.AuthRequest{Username: "framing", Password: "pw"})
	if _, ok := readOneEnvelope(t, ctx, c, reg).(*mmov1.AuthResponse); !ok {
		t.Fatal("want AuthResponse")
	}

	hello := mustEncode(t, reg, &mmov1.Hello{ProtoVer: protoVersion})
	auth := mustEncode(t, reg, &mmov1.AuthRequest{Username: "framing", Password: "pw"})
	cases := []struct {
		name  string
		frame []byte
	}{
		{"two Hello envelopes", append(append([]byte{}, hello...), hello...)},
		{"Hello plus AuthRequest envelopes", append(append([]byte{}, hello...), auth...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subCtx, subCancel := context.WithTimeout(context.Background(), waitTimeout)
			defer subCancel()
			c, resp := dialProxiedWS(t, subCtx, front, proxyWSOrigin)
			if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade status = %v, want 101", resp)
			}
			if err := c.Write(subCtx, websocket.MessageBinary, tc.frame); err != nil {
				t.Fatalf("write two-envelope message: %v", err)
			}
			// A message carrying two envelopes is malformed. The server
			// must never split it into two requests: it answers neither
			// envelope and closes. Any ServerInfo/AuthResponse here means
			// the message boundary was ignored.
			for {
				typ, data, err := c.Read(subCtx)
				if err != nil {
					if subCtx.Err() != nil {
						t.Fatalf("server neither answered nor closed within %v", waitTimeout)
					}
					return // closed without splitting the message: correct
				}
				if typ != websocket.MessageBinary {
					t.Fatalf("message type = %v, want binary", typ)
				}
				env, err := protocol.DecodeEnvelope(data)
				if err != nil {
					t.Fatalf("decode envelope: %v", err)
				}
				msg, _ := reg.DecodeMessage(*env)
				switch msg.(type) {
				case *mmov1.ServerInfo, *mmov1.AuthResponse:
					t.Fatalf("server split one message into multiple envelopes: answered %T", msg)
				}
			}
		})
	}
}

// mustEncode serializes msg as one envelope frame for the framing cases.
func mustEncode(t *testing.T, reg *protocol.Registry, msg proto.Message) []byte {
	t.Helper()
	frame, err := reg.EncodeMessage(msg, protocol.Envelope{Version: protoVersion})
	if err != nil {
		t.Fatalf("encode %T: %v", msg, err)
	}
	return frame
}
