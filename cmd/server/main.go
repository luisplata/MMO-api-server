// Command server is the MMO server entry point: it parses flags and hands
// control to internal/server, which owns the private WSS upstream, the
// deterministic 20 Hz simulation and graceful shutdown.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/observability"
	"github.com/luisplata/mmo-api-server/internal/server"
	"github.com/luisplata/mmo-api-server/internal/world"
)

func main() {
	wssAddr := flag.String("wss", ":8080", "private upstream WebSocket listen address, reachable from the proxy (TLS terminates there; no cert/key flags)")
	wssOrigins := flag.String("wss-origins", "", "comma-separated explicit Origin host patterns allowed to upgrade (never '*')")
	tickRate := flag.Int("tick", game.TickRate, "simulation tick rate; v1 is fixed at 20 Hz by internal/game")
	devAuth := flag.Bool("dev-auth", true, "accept any credentials in dev mode (false rejects all — placeholder for real auth)")
	spawnX := flag.Float64("spawn-x", 0, "default spawn position X (issued by dev auth)")
	spawnZ := flag.Float64("spawn-z", 0, "default spawn position Z (issued by dev auth)")
	mapFile := flag.String("map", "", "canonical <name>.heightmap to load (with its <name>.manifest sidecar); empty = embedded hills fixture")
	templatesFile := flag.String("templates", "data/templates.json", "character-template catalog JSON loaded at boot")
	charactersFile := flag.String("characters", "data/characters.json", "character store JSON loaded at boot")
	logPayloads := flag.Bool("log-payloads", false, "log decoded allowlisted message payloads (dev only; credentials are always redacted)")
	flag.Parse()

	// Boot the map (design D7, spec WTM-4/WTM-5): a -map override loads
	// the canonical pair through the shared loader (checksum + redundant
	// metadata verified), otherwise the embedded hills fixture. ANY
	// failure is fatal — the server never boots on a partial or corrupt
	// map (fail-fast, no partial map).
	heights, err := loadMap(*mapFile)
	if err != nil {
		log.Fatalf("server: map: %v", err)
	}
	log.Printf("server: map active: %s", mapIdentity(*mapFile))

	// Load the character service before opening the listener: missing or
	// corrupt data is fatal, so the deployed server never starts with
	// List/Create/Select unconfigured (P17).
	repos, err := server.LoadRepositories(*templatesFile, *charactersFile)
	if err != nil {
		log.Fatalf("server: repositories: %v", err)
	}
	log.Printf("server: character service loaded: templates=%s characters=%s", *templatesFile, *charactersFile)

	srv, err := server.New(server.Config{
		WSSAddr:          *wssAddr,
		WSSOrigins:       splitOrigins(*wssOrigins),
		TickRate:         int32(*tickRate),
		DevAuth:          *devAuth,
		SpawnX:           float32(*spawnX),
		SpawnZ:           float32(*spawnZ),
		MinProtoVer:      2,
		MaxProtoVer:      2,
		Heights:          heights,
		HandshakeTimeout: 10 * time.Second,
		Templates:        repos.Templates,
		Characters:       repos.Characters,
		Logger:           observability.NewLogger(os.Stdout, *logPayloads, time.Now),
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		log.Printf("server stopped with error: %v", err)
		os.Exit(1)
	}
	log.Printf("server exited cleanly")
}

// splitOrigins parses the -wss-origins comma-separated allowlist,
// trimming whitespace and dropping empty entries. Wildcards are kept so
// server.New can reject them at boot.
func splitOrigins(raw string) []string {
	var origins []string
	for _, part := range strings.Split(raw, ",") {
		if origin := strings.TrimSpace(part); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}

// loadMap resolves the boot heightfield (design D7, task 2.6): a
// non-empty mapPath loads the canonical .heightmap + .manifest pair
// through the shared loader (fail-fast %w on any corruption); an empty
// path selects the embedded hills fixture. The result is always a
// real resolver — flat/nil is never a boot outcome.
func loadMap(mapPath string) (world.HeightResolver, error) {
	if mapPath != "" {
		return world.LoadHeightmap(mapPath)
	}
	return world.DefaultHeightfield()
}

// mapIdentity names the active map for the boot log: the -map path's
// file, or the embedded fixture for the default.
func mapIdentity(mapPath string) string {
	if mapPath != "" {
		return mapPath
	}
	return "embedded hills fixture (internal/world/testdata/hills.*)"
}
