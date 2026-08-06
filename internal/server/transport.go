package server

// The TCP transport seam (spec R9): adapts one net.Conn to the
// session's network.Transport. SendTCP writes length-prefixed frames;
// SendUDP is a no-op because the wiring reads/writes UDP directly on the
// socket, not through the session's Transport; Close tears down the
// conn.

import (
	"net"

	"github.com/luisplata/mmo-api-server/internal/network"
)

// tcpConnTransport is the production Transport for one TCP conn.
type tcpConnTransport struct {
	conn net.Conn
}

// SendTCP writes one length-prefixed frame on the reliable channel.
func (t *tcpConnTransport) SendTCP(frame []byte) error { return network.WriteFrame(t.conn, frame) }

// SendUDP is a no-op: the server writes UDP datagrams directly through
// its socket, not through the session's Transport.
func (t *tcpConnTransport) SendUDP([]byte) error { return nil }

// Close tears down the conn.
func (t *tcpConnTransport) Close() error { return t.conn.Close() }

var _ network.Transport = (*tcpConnTransport)(nil)
