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

	"github.com/luisplata/mmo-api-server/internal/content"
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
	mapFile := flag.String("map", "data/canonical/carnaval-solar-v1.heightmap", "canonical <name>.heightmap to load (with its <name>.manifest sidecar); empty = embedded hills fixture")
	arenaFile := flag.String("arena", "data/canonical/map.json", "canonical arena JSON (mapId, bounds, pillars, spawns, heightmap identity) validated at boot")
	contentFile := flag.String("content", "data/canonical/content.json", "canonical class/ability/type content JSON validated at boot")
	templatesFile := flag.String("templates", "data/templates.json", "character-template catalog JSON loaded at boot")
	charactersFile := flag.String("characters", "data/characters.json", "character store JSON loaded at boot")
	logPayloads := flag.Bool("log-payloads", false, "log decoded allowlisted message payloads (dev only; credentials are always redacted)")
	flag.Parse()

	// Boot the canonical artifacts before any listener opens (fail-fast, no
	// partial boot). content.json and map.json are validated now and will
	// feed the simulation in the combat slice; they are NOT yet wired into
	// gameplay. The -map heightmap is loaded through the shared world loader
	// (checksum + redundant metadata verified); an empty -map selects the
	// embedded hills fixture.
	canonicalContent, err := content.LoadContent(*contentFile)
	if err != nil {
		log.Fatalf("server: content: %v", err)
	}
	arena, err := content.LoadArena(*arenaFile)
	if err != nil {
		log.Fatalf("server: arena: %v", err)
	}
	heights, err := loadMap(*mapFile)
	if err != nil {
		log.Fatalf("server: map: %v", err)
	}
	log.Printf("server: map active: %s", mapIdentity(*mapFile))

	// M1 alignment gate: with a canonical -map, the arena's declared
	// heightmap identity must match the real .heightmap + .manifest pair.
	// With the embedded fixture there is no canonical arena to align.
	if *mapFile != "" {
		if err := content.VerifyHeightmapFile(arena, *mapFile); err != nil {
			log.Fatalf("server: arena alignment: %v", err)
		}
	} else {
		log.Printf("server: canonical arena verification skipped (embedded hills fixture)")
	}
	log.Printf("server: arena loaded: mapId=%s mapHash=%s heightmap_sha256=%s",
		arena.MapID, arena.MapHash, shortSHA(arena.Heightmap.SHA256))
	log.Printf("server: content loaded: contentVersion=%s classes=%d abilities=%d",
		canonicalContent.ContentVersion, len(canonicalContent.Classes), len(canonicalContent.Abilities))

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

// shortSHA abbreviates a hex digest for boot logs (first 8 chars). Short
// inputs are returned unchanged so the log never panics.
func shortSHA(sum string) string {
	const n = 8
	if len(sum) <= n {
		return sum
	}
	return sum[:n]
}
