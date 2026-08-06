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
)

func main() {
	tcpAddr := flag.String("tcp", ":8000", "TCP listen address (handshake, auth, spawn/despawn, reliable commands)")
	udpAddr := flag.String("udp", ":8001", "UDP listen address (MoveInput client->server, snapshots server->client)")
	tickRate := flag.Int("tick", game.TickRate, "simulation tick rate; v1 is fixed at 20 Hz by internal/game")
	devAuth := flag.Bool("dev-auth", true, "accept any credentials in dev mode (false rejects all — placeholder for real auth)")
	spawnX := flag.Float64("spawn-x", 0, "default spawn position X (issued by dev auth)")
	spawnZ := flag.Float64("spawn-z", 0, "default spawn position Z (issued by dev auth)")
	flag.Parse()

	srv, err := server.New(server.Config{
		TCPAddr:          *tcpAddr,
		UDPAddr:          *udpAddr,
		TickRate:         int32(*tickRate),
		DevAuth:          *devAuth,
		SpawnX:           float32(*spawnX),
		SpawnZ:           float32(*spawnZ),
		MinProtoVer:      1,
		MaxProtoVer:      1,
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
