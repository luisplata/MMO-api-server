package server

// WebSocket transport adapters (design P8): one binary message per
// envelope inbound; reliable outbound queues and realtime outbound
// coalesces through the sink.

import (
	"context"
	"fmt"

	"github.com/coder/websocket"

	"github.com/luisplata/mmo-api-server/internal/network"
)

type wsFrameSource struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (s *wsFrameSource) Next() ([]byte, error) {
	typ, frame, err := s.conn.Read(s.ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		_ = s.conn.Close(websocket.StatusUnsupportedData, "binary messages only")
		return nil, fmt.Errorf("server: websocket: got %v message, want binary", typ)
	}
	return frame, nil
}

type wsTransport struct {
	sink network.Sink
}

func (t *wsTransport) SendTCP(frame []byte) error { return t.sink.Reliable(frame) }
func (t *wsTransport) SendUDP(frame []byte) error { t.sink.Realtime(frame); return nil }
func (t *wsTransport) Close() error               { return t.sink.Close() }

var (
	_ network.FrameSource = (*wsFrameSource)(nil)
	_ network.Transport   = (*wsTransport)(nil)
)
