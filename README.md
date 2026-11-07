# MMO API Server

Game server for a **Massively Multiplayer Online** world, written in Go. Authoritative simulation at 20 Hz, **WSS-only** browser transport behind a TLS proxy, Protobuf wire protocol, and public two-player rooms. Ships a Unity/C# client contract and a committed TypeScript (protobuf-es) contract for web clients.

---

## Table of Contents

- [Features](#features)
- [System Requirements](#system-requirements)
- [Quick Start](#quick-start)
- [Running the Server](#running-the-server)
- [Connecting a Client](#connecting-a-client)
- [Configuration](#configuration)
- [Architecture Overview](#architecture-overview)
- [Documentation](#documentation)
- [Regenerating Code from the Proto](#regenerating-code-from-the-proto)
- [License](#license)

---

## Features

- **Authoritative simulation** — the server is the single source of truth. Clients send *intentions* (`MoveInput`); the server validates, integrates, and corrects.
- **Public two-player rooms** — `NextAvailable` reuses capacity or creates on miss; `ByID` joins a known public room. Capacity 2, no spectators, no private/by-code rooms.
- **WSS-only transport** — one binary WebSocket message = one 11-byte envelope frame. TLS terminates at the reverse proxy; the Go process binds a private plaintext upstream and exposes no public game port.
- **Deterministic tick loop** — fixed 50 ms steps (20 Hz), accumulator-based, no `time.Sleep`, reproducible in tests.
- **Interest management (chunk grid)** — 64-unit cells, 5×5 view radius, spawn/despawn on cell change. v1 uses flat broadcast; the seam is ready for spatial partitioning without protocol changes.
- **Protobuf wire contract** — single source of truth (`proto/v1/world.proto`), committed Go + C# + TypeScript codegen, AOT-safe for Unity/IL2CPP and directly consumable by browsers.
- **Graceful shutdown** — SIGTERM/SIGINT aware, drains cleanly.
- **Dockerized** — multi-stage build, static binary, minimal Alpine runtime.
- **E2E tested** — two players see each other move end-to-end.

---

## System Requirements

### Minimum (development / few players)

| Component | Requirement |
|-----------|-------------|
| **OS** | Linux, macOS, Windows 10/11 |
| **CPU** | 2 cores (any modern x64 or ARM) |
| **RAM** | 512 MiB |
| **Network** | Private `:8080` upstream; public TLS `:443` at the proxy |
| **Go** | 1.25.x (only for native builds) |
| **Docker** | 24.0+ (for containerized runs) |
| **Disk** | 100 MiB (source + binary) |

### Recommended (production / many players)

| Component | Requirement |
|-----------|-------------|
| **CPU** | 4+ cores (the sim loop is single-threaded; extra cores help with networking goroutines) |
| **RAM** | 2 GiB (in-memory world state; scales with entity count) |
| **Network** | Low-latency, low-jitter connection; the proxy must forward WebSocket upgrades |
| **Go** | 1.25.x |

### Notes

- The binary is **statically linked** (`CGO_ENABLED=0`) — no libc or external dependencies at runtime.
- State is **in-memory**. No database required (or supported) in v1.
- TLS lives at the reverse proxy; the Go process speaks plaintext `ws://` only on a private network and must never be published directly.

---

## Quick Start

### Option A: Docker (recommended)

```bash
git clone https://github.com/luisplata/MMO-api-server.git
cd MMO-api-server
docker compose up
```

The game server listens privately on `:8080`; browsers connect only through the TLS proxy (see `docs/DEPLOYMENT.md` §10). `docker compose up` starts the game server alone — bring your own TLS proxy for public traffic.

### Option B: Native (Go required)

```bash
git clone https://github.com/luisplata/MMO-api-server.git
cd MMO-api-server
go run ./cmd/server
```

Build a binary:

```bash
go build -o mmo-server ./cmd/server
./mmo-server
```

---

## Running the Server

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-wss` | `:8080` | Private upstream WebSocket listen address; reachable from the proxy, never published. TLS terminates at the proxy; there are no cert/key flags. |
| `-wss-origins` | _(empty)_ | Comma-separated explicit Origin host patterns allowed to upgrade. `*` aborts boot. |
| `-tick` | `20` | Simulation tick rate in Hz (v1 fixed at 20) |
| `-dev-auth` | `true` | Accept any credentials (username = player ID). **Non-production only** — proves no identity. `false` rejects all until real auth exists. |
| `-map` | _(empty)_ | Canonical `<name>.heightmap` to load with its `<name>.manifest` sidecar (verified sha256 + redundant metadata). Empty = embedded `hills` fixture (32×32). See `docs/HEIGHTMAP.md` + `docs/DEPLOYMENT.md`. |
| `-spawn-x` / `-spawn-z` | `0` | Fixed spawn coordinates used for every selected character. |
| `-templates` | `data/templates.json` | Character-template catalog loaded at boot; missing/corrupt is fatal. |
| `-characters` | `data/characters.json` | Character store loaded at boot; missing/corrupt is fatal. |
| `-log-payloads` | `false` | Log decoded, allowlisted payload fields (dev only). Metadata logs are always on; credentials and `udpToken` are always redacted. |

### Examples

```bash
# Private upstream reachable by a local proxy
./mmo-server -wss 127.0.0.1:8080 -wss-origins game.example.com

# Explicit character data (defaults are data/templates.json + data/characters.json)
./mmo-server -wss :8080 -wss-origins game.example.com \
  -templates data/templates.json -characters data/characters.json

# Dev-only payload logging (never in production)
./mmo-server -log-payloads=true

# With a terrain map (fail-fast if the pair is corrupt)
./mmo-server -map ./internal/world/testdata/combate_map.heightmap -dev-auth=true
```

```bash
# Docker: the game container only exposes 8080 on the private network.
# Run the proxy separately; do not publish the game port.
docker compose up
```

---

## Connecting a Client

Browsers connect to `wss://<proxy-host>/ws`; the TLS-terminating reverse proxy (see `deploy/proxy/nginx.conf`) forwards the upgrade to the private Go upstream on `:8080`. The Go process owns no certificates and must never be published directly.

### Public Matchmaking (MVP)

The MVP exposes **two modes only**:

- **`NextAvailable` (mode 1)** — reuses any public room with free capacity; if every room is full it creates a new public room and reserves a slot atomically.
- **`ByID` (mode 2)** — joins a known public room by `roomId`.

Rooms hold **two active players**, with no spectators, no private/by-code rooms, and no `Create` join mode. `roomId` is an identifier, not a credential: admission depends only on capacity, state and ownership. Private rooms are deferred to a separate SDD that requires real authentication. A reservation holds its slot for a **30-second lease**; a disconnect releases it immediately.

### Connection Flow (TL;DR)

1. Open `wss://<proxy-host>/ws` and send `Hello{ protoVer: 2 }`.
2. Receive `ServerInfo` → send `AuthRequest{ username, password }`.
3. Receive `AuthResponse` → in `selecting`, send `ListCharacters` / `CreateCharacter` / `SelectCharacter`.
4. Send `JoinRoom{ mode: 1 }` (or `{ mode: 2, roomId }`) → receive `JoinRoomResponse{ roomId, capacity, occupancy }`. `EnterWorld` requires both a selected character and a room.
5. Send `EnterWorld` → receive the empty ack snapshot and the real world state.
6. Stable state: send `MoveInput`, receive `Snapshot` at 10 Hz, and use application `Ping`/`Pong` to measure RTT/EMA/jitter. The second player joins live at the server's fixed spawn point, with no ready gate.

| Setting | Value |
|---------|-------|
| **WSS endpoint** | `wss://<proxy-host>/ws` |
| **Go upstream** | private `:8080` (never public) |
| **Protocol version** | `2` (range `[2,2]`) |
| **Room capacity** | 2 active players, no spectators |
| **Tick / snapshot** | 20 Hz / 10 Hz |
| **Frame limit** | 64 KiB per binary WebSocket message |
| **Handshake timeout** | 10 seconds |

> **Dev auth is not production auth.** `-dev-auth=true` accepts any credentials and proves no real identity. Use `-dev-auth=false` to reject all until a real auth backend exists.

---

## Architecture Overview

```
Browser ──wss(443)──► TLS proxy (nginx) ──ws(private)──► mmo-server
                                                        internal/server  (WSS handler)
                                                        internal/session (state machine)
                                                        internal/rooms   (public lobby)
                                                        internal/game    (20 Hz sim)
                                                        internal/world   (interest grid)
```

### Package Layout

| Path | Responsibility |
|------|----------------|
| `proto/v1/world.proto` | **The wire contract** — all messages, type IDs 1–22 |
| `proto/v1/gen/` | Committed Go + C# + TypeScript codegen |
| `internal/protocol` | Envelope (11-byte header) + type registry |
| `internal/network` | WebSocket-backed `FrameSource` / `Sink` adapters |
| `internal/session` | State machine: handshaking → authenticating → selecting → entering → in-world |
| `internal/rooms` | Public room manager, 30 s reservation lease, capacity 2 |
| `internal/game` | Deterministic tick loop, kinematic movement, snapshots |
| `internal/world` | Chunk grid (64u, 5×5), interest resolver (v1 flat) |
| `internal/server` | WSS handler, origin/read limits, room runner, wiring |
| `cmd/server/main.go` | Entry point, flag parsing, signal handling |
| `docs/PROTOCOL.md` | Human-readable wire spec |
| `docs/ACADEMIC.md` | Deep-dive: why every decision was made |

### The Three Goroutines

1. **WSS handler** — upgrades HTTP, validates Origin, enforces the 64 KiB read limit, and owns each session's read loop.
2. **roomRunner** — one 20 Hz ticker steps every room's simulation; a slow peer never blocks the tick.
3. **Sink writers** — per-connection depth-1 realtime and bounded reliable queues write off-tick.

---

## Documentation

- **[`docs/PROTOCOL.md`](docs/PROTOCOL.md)** — the wire contract. Read this to build a client.
- **[`docs/HEIGHTMAP.md`](docs/HEIGHTMAP.md)** — canonical binary terrain format (`.heightmap` + `.manifest` contract).
- **[`docs/UNITY_HEIGHTMAP_HANDOFF.md`](docs/UNITY_HEIGHTMAP_HANDOFF.md)** — what the Unity dev must deliver (exporter checklist + `heightmap-validate` gate).
- **[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)** — runbook: gate → staging → atomic activation → ` -map` boot → smoke → rollback N−1.
- **[`docs/ACADEMIC.md`](docs/ACADEMIC.md)** — first-principles explanation of every architectural decision, the decision log, industry context, and how the code maps to the theory.

---

## Regenerating Code from the Proto

The generated code is committed — you only need this if you modify `world.proto`.

### Toolchain (pinned)

| Tool | Version | Install |
|------|---------|---------|
| `protoc` | v35.1 | Download from protobuf releases |
| `protoc-gen-go` | v1.36.11 | `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11` |
| `buf` | v1.72.0 | `go install github.com/bufbuild/buf/cmd/buf@v1.72.0` |
| `@bufbuild/protobuf` / `@bufbuild/protoc-gen-es` | 2.16.0 (exact) | `npm ci` |

```bash
npm ci && make proto-es   # TypeScript contract -> proto/v1/gen/es/v1/world_pb.ts
make proto-es-check       # regenerate and fail on drift
make proto                # Go + C# + TypeScript
make all                  # proto → tidy → fmt → vet → build → test
```

The browser contract is generated with protobuf-es v2 (`buf.gen.es.yaml`, `target=ts`) and imports from `@bufbuild/protobuf` must stay extensionless. There are **no C# web-client bindings**: the committed C# bindings are for the Unity client.

---

## License

MIT
