package server

// P8 origin policy: explicit allowlist only, missing Origin rejected,
// wildcard boot rejected.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/luisplata/mmo-api-server/internal/game"
)

func TestWSOriginAllowlist(t *testing.T) {
	_, _, _, url, _ := newWSHarness(t)
	cases := []struct {
		name   string
		origin string
		want   int
	}{
		{"allowlisted origin upgrades", testWSOrigin, http.StatusSwitchingProtocols},
		{"disallowed origin is forbidden", "https://evil.example", http.StatusForbidden},
		{"absent origin is forbidden", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			opts := &websocket.DialOptions{}
			if tc.origin != "" {
				opts.HTTPHeader = http.Header{"Origin": {tc.origin}}
			}
			c, resp, err := websocket.Dial(ctx, url, opts)
			if c != nil {
				defer c.Close(websocket.StatusNormalClosure, "")
			}
			if err == nil {
				if tc.want != http.StatusSwitchingProtocols {
					t.Fatalf("dial succeeded, want %d", tc.want)
				}
				return
			}
			if resp == nil || resp.StatusCode != tc.want {
				t.Fatalf("status = %v, want %d (%v)", resp, tc.want, err)
			}
		})
	}
}

func TestWSSBootRejectsWildcardOrigin(t *testing.T) {
	base := Config{
		WSSAddr:  "127.0.0.1:0",
		TickRate: game.TickRate, DevAuth: true, MinProtoVer: 1, MaxProtoVer: 1,
	}
	for _, origin := range []string{"*", "https://*.example.com"} {
		cfg := base
		cfg.WSSOrigins = []string{origin}
		if _, err := New(cfg); err == nil {
			t.Fatalf("New with wildcard origin %q must fail", origin)
		}
	}
	cfg := base
	cfg.WSSOrigins = []string{"allowed.example"}
	if _, err := New(cfg); err != nil {
		t.Fatalf("New with an explicit origin rejected: %v", err)
	}
}
