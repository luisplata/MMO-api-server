# MMO API Server

Game server for a **Massively Multiplayer Online** world, written in Go. Authoritative simulation at 20 Hz, hybrid TCP+UDP transport, Protobuf wire protocol. Built to pair with a Unity client (separate repo).

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
- **Hybrid transport** — TCP for reliable lifecycle (handshake, auth, spawn/despawn); UDP for fast, fresh movement and world snapshots.
- **Deterministic tick loop** — fixed 50 ms steps (20 Hz), accumulator-based, no `time.Sleep`, reproducible in tests.
- **Interest management (chunk grid)** — 64-unit cells, 5×5 view radius, spawn/despawn on cell change. v1 uses flat broadcast; the seam is ready for spatial partitioning without protocol changes.
- **Protobuf wire contract** — single source of truth (`proto/v1/world.proto`), committed Go + C# codegen, AOT-safe for Unity/IL2CPP.
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
| **Network** | TCP `:8000` + UDP `:8001` open |
| **Go** | 1.25.x (only for native builds) |
| **Docker** | 24.0+ (for containerized runs) |
| **Disk** | 100 MiB (source + binary) |

### Recommended (production / many players)

| Component | Requirement |
|-----------|-------------|
| **CPU** | 4+ cores (the sim loop is single-threaded; extra cores help with networking goroutines) |
| **RAM** | 2 GiB (in-memory world state; scales with entity count) |
| **Network** | Low-latency, low-jitter connection; UDP must not be rate-limited |
| **Go** | 1.25.x |

### Notes

- The binary is **statically linked** (`CGO_ENABLED=0`) — no libc or external dependencies at runtime.
- State is **in-memory**. No database required (or supported) in v1.
- No TLS/DTLS in v1 — traffic is plaintext. Place behind a reverse proxy or VPN if encryption is needed.

---

## Quick Start

### Option A: Docker (recommended)

```bash
git clone https://github.com/luisplata/MMO-api-server.git
cd MMO-api-server
docker compose up
```

Server listens on `localhost:8000` (TCP) and `localhost:8001` (UDP).

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
| `-tcp` | `:8000` | TCP listen address (handshake, auth, spawn/despawn, reliable commands) |
| `-udp` | `:8001` | UDP listen address (MoveInput client→server, snapshots server→client) |
| `-tick` | `20` | Simulation tick rate in Hz (v1 fixed at 20) |
| `-dev-auth` | `true` | Accept any credentials (username = player ID). Set `false` to reject all (placeholder for real auth). |
| `-spawn-x` | `0` | Default spawn X coordinate |
| `-spawn-z` | `0` | Default spawn Z coordinate |

### Examples

```bash
# Custom ports, dev auth on
./mmo-server -tcp :9000 -udp :9001 -dev-auth true

# Production-like: auth rejected (no real auth backend yet)
./mmo-server -dev-auth false
```

```bash
# Docker: custom ports
docker compose run -p 9000:9000 -p 9001:9001/udp mmo-server -tcp :9000 -udp :9001
```

---

## Connecting a Client

The server expects a Unity client (C#) speaking the protobuf wire protocol. The client repo is separate — this server provides:

- **Committed C# bindings**: `proto/v1/gen/csharp/World.cs` — drop into your Unity project, no protoc needed.
- **Wire contract**: see [`docs/PROTOCOL.md`](docs/PROTOCOL.md) for the full byte-level spec.
- **5-step connection flow** (TL;DR):

```
1. TCP connect → send Hello{ protoVer: 1 }
2. Receive ServerInfo → send AuthRequest{ username, password }
3. Receive AuthResponse (carries udpToken + spawnPos) → send EnterWorld
4. Receive two WorldSnapshots (first = ack, second = world state)
5. UDP: send udpToken as the FIRST raw datagram (no envelope) → then send/receive MoveInput/Snapshot
```

### Connection Parameters

| Setting | Value |
|---------|-------|
| **TCP host** | `localhost` (or your server IP) |
| **TCP port** | `8000` |
| **UDP host** | same as TCP |
| **UDP port** | `8001` |
| **Protocol version** | `1` (range `[1,1]`) |
| **Auth** | any username/password accepted in dev mode |
| **Tick rate** | 20 Hz |
| **Snapshot rate** | 10 Hz (per player, staggered) |
| **Max speed** | 10 units/sec (server-clamped) |
| **Frame limit** | TCP: 64 KiB; UDP: 1200 bytes |
| **Handshake timeout** | 10 seconds |

---

## Architecture Overview

```
Client (Unity/C#)                              Server (Go)
─────────────────────                          ──────────
TCP :8000  ◄── handshake, auth, spawn ──►  internal/session
                                               internal/protocol
UDP :8001  ◄── MoveInput / Snapshot ────►  internal/network
                                               internal/game (20 Hz sim)
                                               internal/world (interest grid)
```

### Package Layout

| Path | Responsibility |
|------|----------------|
| `proto/v1/world.proto` | **The wire contract** — all messages, type IDs 1–12 |
| `proto/v1/gen/` | Committed Go + C# codegen |
| `internal/protocol` | Envelope (11-byte header) + type registry |
| `internal/network` | TCP framing (length-prefix) + UDP (datagram) |
| `internal/session` | State machine: connecting → handshaking → authenticating → entering → in-world |
| `internal/game` | Deterministic tick loop, kinematic movement, snapshots |
| `internal/world` | Chunk grid (64u, 5×5), interest resolver (v1 flat) |
| `internal/server` | Wiring: accept loop, UDP loop, sim-owner goroutine |
| `cmd/server/main.go` | Entry point, flag parsing, signal handling |
| `docs/PROTOCOL.md` | Human-readable wire spec |
| `docs/ACADEMIC.md` | Deep-dive: why every decision was made |

### The Three Goroutines

1. **acceptLoop** — accepts TCP connections, spawns a goroutine per client.
2. **udpLoop** — reads datagrams, binds tokens, queues inputs (mutex-safe).
3. **runSim (sim-owner)** — single goroutine that owns the simulation: steps at 20 Hz, registers/removes players, assembles and sinks snapshots.

---

## Documentation

- **[`docs/PROTOCOL.md`](docs/PROTOCOL.md)** — the wire contract. Read this to build a client.
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

```bash
make proto    # regenerate Go + C#
make all      # proto → tidy → fmt → vet → build → test
```

---

## License

MIT
