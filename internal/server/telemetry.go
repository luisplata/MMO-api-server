package server

// Structured telemetry wiring (P18): every reliable frame emits one JSON
// metadata event and snapshots feed a one-second aggregate. Payloads are
// opt-in and allowlisted; proto structs and raw frames are never logged.

import (
	"google.golang.org/protobuf/proto"

	"github.com/luisplata/mmo-api-server/internal/network"
	"github.com/luisplata/mmo-api-server/internal/observability"
	"github.com/luisplata/mmo-api-server/internal/protocol"
	"github.com/luisplata/mmo-api-server/internal/session"
	mmov1 "github.com/luisplata/mmo-api-server/proto/v1/gen/go/v1"
)

// connLogger is both the reliable Transport and Sink wrapper for one
// connection, so every reliable frame shares one identity. Realtime
// snapshot frames bypass it and are counted by the aggregate instead.
type connLogger struct {
	tr   network.Transport
	sink network.Sink
	srv  *Server
	id   string
	sess *session.Session
}

// logFrame emits the metadata event for one reliable frame. Identity is
// read after the session handled it, so Auth/JoinRoom/Select events carry
// the account/room/character they just established.
func (c *connLogger) logFrame(frame []byte, dir observability.Direction) {
	if c.srv.log == nil {
		return
	}
	ev := observability.MessageEvent{SessionID: c.id, Direction: dir, Size: len(frame)}
	if c.sess != nil {
		ev.AccountID = c.sess.PlayerID()
		ev.RoomID = c.sess.ActiveRoomID()
		ev.CharacterID = c.sess.ActiveCharacterID()
	}
	env, err := protocol.DecodeEnvelope(frame)
	if err != nil {
		c.srv.log.Message(ev)
		return
	}
	ev.TypeID, ev.Seq = env.Type, env.Seq
	if c.srv.log.PayloadsEnabled() {
		if msg, err := c.srv.reg.DecodeMessage(*env); err == nil {
			ev.Payload = decodedPayload(msg)
		}
	}
	c.srv.log.Message(ev)
}

// decodedPayload maps allowlisted scalars; AuthRequest is absent, so
// credentials can never reach the logger.
func decodedPayload(msg proto.Message) map[string]any {
	switch m := msg.(type) {
	case *mmov1.JoinRoom:
		return map[string]any{"joinMode": m.Mode, "roomId": m.RoomId}
	case *mmov1.SelectCharacter:
		return map[string]any{"characterId": m.CharacterId}
	case *mmov1.Ping:
		return map[string]any{"nonce": m.Nonce, "clientTime": m.ClientTime}
	default:
		return nil
	}
}

func (c *connLogger) SendTCP(frame []byte) error {
	c.logFrame(frame, observability.Outbound)
	return c.tr.SendTCP(frame)
}
func (c *connLogger) SendUDP(frame []byte) error { return c.tr.SendUDP(frame) }
func (c *connLogger) Reliable(frame []byte) error {
	c.logFrame(frame, observability.Outbound)
	return c.sink.Reliable(frame)
}
func (c *connLogger) Realtime(frame []byte) { c.sink.Realtime(frame) }

func (c *connLogger) Close() error {
	if c.sink != nil {
		return c.sink.Close()
	}
	return c.tr.Close()
}
