package server

// The WSS endpoint (design P8): one binary WebSocket message carries one
// protocol envelope. TLS terminates at the proxy, so this file never
// loads certificates. The connection lifetime is bound to the server run
// context, never r.Context(), which net/http cancels on handler return.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/luisplata/mmo-api-server/internal/network"
)

const (
	wsPath              = "/ws"
	wsReadLimit         = 64 << 10
	wsWriteTimeout      = 10 * time.Second
	wsReadHeaderTimeout = 10 * time.Second
)

// validateWSOrigins rejects wildcard patterns at boot.
func validateWSOrigins(origins []string) error {
	for _, origin := range origins {
		if strings.Contains(origin, "*") {
			return fmt.Errorf("server: wildcard websocket origin %q is not allowed", origin)
		}
	}
	return nil
}

// wsHandler upgrades one connection and serves its session until the
// socket closes or runCtx is cancelled.
func (s *Server) wsHandler(runCtx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(wsPath, func(w http.ResponseWriter, r *http.Request) {
		// The library's same-host fallback accepts a missing Origin;
		// require an explicit one instead.
		if r.Header.Get("Origin") == "" {
			http.Error(w, "websocket: Origin header required", http.StatusForbidden)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns:  s.wsOrigins,
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		c.SetReadLimit(wsReadLimit)
		s.serveWS(runCtx, c)
	})
	return mux
}

// serveWS builds the bounded sink before the session so SendTCP→Reliable
// and SendUDP→Realtime share one writer; a failed write closes only this
// connection.
func (s *Server) serveWS(runCtx context.Context, c *websocket.Conn) {
	// The watcher sends 1001 before the read context is cancelled, so the
	// client observes a going-away close on shutdown.
	ctx, cancel := context.WithCancel(context.WithoutCancel(runCtx))
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-runCtx.Done():
			_ = c.Close(websocket.StatusGoingAway, "server shutting down")
			cancel()
		case <-done:
		}
	}()

	sink := network.NewSink(
		func(frame []byte) error { return wsWrite(ctx, c, frame) },
		func(frame []byte) error { return wsWrite(ctx, c, frame) },
		func() error {
			if runCtx.Err() != nil {
				return c.Close(websocket.StatusGoingAway, "server shutting down")
			}
			return c.Close(websocket.StatusNormalClosure, "")
		},
	)
	defer sink.Close()
	s.serveFrames(ctx, &wsFrameSource{ctx: ctx, conn: c}, nil, &wsTransport{sink: sink}, sink)
}

func wsWrite(ctx context.Context, c *websocket.Conn, frame []byte) error {
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return c.Write(wctx, websocket.MessageBinary, frame)
}
