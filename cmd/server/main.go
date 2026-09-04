// Command server is the MMO server entry point (task 4.9, design PR4b):
// it parses flags and hands control to internal/server, which owns the
// TCP accept loop, the UDP input loop, the deterministic 20 Hz
// simulation and graceful shutdown.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luisplata/mmo-api-server/internal/game"
	"github.com/luisplata/mmo-api-server/internal/server"
	"github.com/luisplata/mmo-api-server/internal/world"
)

func main() {
	tcpAddr := flag.String("tcp", ":8000", "TCP listen address (handshake, auth, spawn/despawn, reliable commands)")
	udpAddr := flag.String("udp", ":8001", "UDP listen address (MoveInput client->server, snapshots server->client)")
	tickRate := flag.Int("tick", game.TickRate, "simulation tick rate; v1 is fixed at 20 Hz by internal/game")
	devAuth := flag.Bool("dev-auth", true, "accept any credentials in dev mode (false rejects all — placeholder for real auth)")
	spawnX := flag.Float64("spawn-x", 0, "default spawn position X (issued by dev auth)")
	spawnZ := flag.Float64("spawn-z", 0, "default spawn position Z (issued by dev auth)")
	mapFile := flag.String("map", "", "canonical <name>.heightmap to load (with its <name>.manifest sidecar); empty = embedded hills fixture")
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

	srv, err := server.New(server.Config{
		TCPAddr:          *tcpAddr,
		UDPAddr:          *udpAddr,
		TickRate:         int32(*tickRate),
		DevAuth:          *devAuth,
		SpawnX:           float32(*spawnX),
		SpawnZ:           float32(*spawnZ),
		MinProtoVer:      2,
		MaxProtoVer:      2,
		Heights:          heights,
		HandshakeTimeout: 10 * time.Second,
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
