# Protocolo World Simulation v1 — contrato de wire

> **Para**: desarrollador del cliente Unity.
> **Qué es**: el contrato binario entre `mmo-api-server` (Go) y el cliente (C#/Unity).
> **Fuente de verdad**: `proto/v1/world.proto`. El código generado (Go y C#) está **commiteado** en `proto/v1/gen/` — no necesitás protoc.
> **Versión del contrato**: v1 (rango negociado `[1, 1]`).

---

## 1. TL;DR — conectarse en 5 pasos

1. **Conectá TCP** a `<host>:8000` y mandá `Hello{ protoVer: 1 }`.
2. Recibís `ServerInfo` (versión negociada, `tickRate`, `serverTime`) → mandá `AuthRequest{ username, password }`.
3. Recibís `AuthResponse{ ok, playerId, spawnPos, udpToken }` → mandá `EnterWorld`.
4. Recibís dos `WorldSnapshot` por TCP (el primero vacío = ack de `EnterWorld`; el segundo = estado real del mundo). **Bindeá UDP**: el **primer datagrama** a `<host>:8001` es el `udpToken` **crudo** (sin envelope).
5. **Estado estable**: mandá `MoveInput` por UDP, recibís `Snapshot` a 10 Hz, y escuchá `SpawnEntity`/`DespawnEntity` por TCP.

```csharp
// Pseudo-C# (Unity). Los helpers Send/Recv codifican el envelope por vos.
var tcp = ConnectTcp(host, 8000);
Send(new Hello { ProtoVer = 1 });                    // paso 1
var info = Recv<ServerInfo>();                       // paso 2
Send(new AuthRequest { Username = user, Password = pass });
var auth = Recv<AuthResponse>();                     // paso 3 — guardá UdpToken y SpawnPos
Send(new EnterWorld());                              // paso 3
Recv<WorldSnapshot>();                               // paso 4 — vacío (ack)
Recv<WorldSnapshot>();                               // paso 4 — real (estado del mundo)

var udp = new UdpClient(host, 8001);
udp.Send(auth.UdpToken);                             // paso 4 — token crudo, primer datagrama

udp.Send(MoveInput { Seq = ++clientSeq, Dir = ..., Speed = 5f, Yaw = ... }); // paso 5
var snap = RecvUdp<Snapshot>();                      // paso 5 — 10 Hz
```

---

## 2. Split de transporte

| Canal | Mensajes | Fiabilidad | Uso |
|-------|----------|-----------|-----|
| **TCP** (`:8000`) | `Hello`, `ServerInfo`, `VersionMismatch`, `AuthRequest`, `AuthResponse`, `EnterWorld`, `WorldSnapshot`, `SpawnEntity`, `DespawnEntity`, `Ack` | Fiable — stream reensamblado por prefijo de longitud | Lifecycle, handshake, auth, spawn/despawn, comandos fiables |
| **UDP** (`:8001`) | `MoveInput` (cliente→servidor), `Snapshot` (servidor→cliente) | No fiable — 1 datagrama = 1 frame | Movimiento y sync del mundo |

- **NO hay KCP en v1.** UDP es crudo, sin capa de fiabilidad encima. `Ack` existe (v1 sin retransmisión), pero no hay reenvío.
- Un mensaje que no quepa en UDP (ver sección 3) **debe** ir por TCP.
- Los puertos son los defaults del server (`-tcp :8000`, `-udp :8001`); en Docker se exponen igual.

---

## 3. Framing + envelope

### Envelope (11 bytes, big-endian)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|        magic (u16 = 0x4D4D, "MM")   |       version (u16)     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|        type (u16, id del mensaje)   |  flags (u8) |            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    seq (u32)        |        payload ...
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+                       |
```

| Offset | Tamaño | Campo | Descripción |
|-------|--------|-------|-------------|
| 0–1 | u16 | `magic` | `0x4D4D` ("MM"). Cualquier frame sin este magic se descarta. |
| 2–3 | u16 | `version` | Major del protocolo (v1 = 1). Validado en el handshake. |
| 4–5 | u16 | `type` | Id del mensaje (catálogo, sección 5). |
| 6 | u8 | `flags` | Ver tabla de flags abajo. |
| 7–10 | u32 | `seq` | Per-session monotónico, **solo** para frames con `needs-ack`. |
| 11+ | — | `payload` | Protobuf del mensaje. |

**Flags (bitmask)**: bit0 `needs-ack` (0x01), bit1 `compressed` (0x02, **reservado v1**), bit2 `encrypted` (0x04, **reservado v1**).

**Límite**: el frame total (header + payload) no puede superar **64 KiB**.

> ⚠️ No confundir el `seq` del **envelope** (per-session, para needs-ack) con el `seq` del **`Snapshot`** (monotónico del server) ni con el `seq` del **`MoveInput`** (del cliente). Son tres contadores distintos — detalle en la sección 7.

### TCP

- `[4 bytes BE uint32 = longitud][payload]`, con `payload ≤ 64 KiB`.
- El reader reensambla el stream por prefijo de longitud.
- **Frame > 64 KiB → la conexión se cierra** (`ErrFrameTooLarge`). El stream no se puede resincronizar.
- **Stream truncado** a mitad de header o payload → error de frame incompleto; **nunca** se entrega un frame corrupto.

### UDP

- **1 datagrama = 1 frame**. El frame (envelope completo) no puede superar **1200 B** (MTU-safe, sin fragmentación IP).
- **Datagrama > 1200 B → rechazado entero**, nunca fragmentado ni enviado a medias.
- El bind del token (primer datagrama) es la única excepción al envelope: es el token **crudo**, sin header.

---

## 4. Handshake & ciclo de vida

```
Cliente (Unity)                          Servidor (mmo-api-server)
      |  TCP: conecta                         |
      |────── Hello{ protoVer: 1 } ──────────►|
      |◄───── ServerInfo{ protoVer:1, tickRate:20, serverTime } ─|
      |────── AuthRequest{ username, password } ►|
      |◄───── AuthResponse{ ok, playerId, spawnPos, udpToken } ─|
      |────── EnterWorld ────────────────────►|
      |◄───── WorldSnapshot (VACÍO — ack de EnterWorld, needs-ack) ─|
      |◄───── WorldSnapshot (REAL — estado completo del mundo) ─|
      |  UDP: bind                            |
      |────── udpToken (datagrama CRUDO) ────►|  ← primer datagrama
      |◄───── Snapshot{ seq, entities } ──────|  ← estado estable (10 Hz)
      |────── MoveInput{ seq, dir, speed, yaw } ►| ← por UDP
```

Reglas del ciclo de vida:

- **Fuera de orden = cierre.** Cada mensaje solo es válido en su estado (ej.: `EnterWorld` antes de auth → error de protocolo y la sesión se cierra).
- **Dos `WorldSnapshot` al entrar**: el primero es el ack del `EnterWorld` (vacío, con flag `needs-ack` — contestalo con `Ack`); el segundo lo manda el server cuando te registra en la simulación y es el estado **real** del mundo (todos los jugadores, v1 flat fanout).
- **Bind UDP**: el server asocia tu dirección UDP con tu sesión TCP autenticada usando el token. **Tokens incorrectos/desconocidos se ignoran** (no bindean). Después del bind, solo la dirección bindeada recibe snapshots.
- **Timeout de handshake**: 10 s desde la conexión (default del server, hardcodeado en `cmd/server/main.go` — no es flag). Si el handshake no completa a tiempo, el siguiente frame cierra la sesión.
- **Login duplicado**: un segundo `playerId` ya conectado se rechaza (la segunda conexión se cae).
- Tras `EnterWorld`, el cliente es **responsable de bindear UDP**: los snapshots no llegan hasta que el server tenga tu addr bindeada.

---

## 5. Catálogo de mensajes (ids 1–12)

Tipos embebidos (no son mensajes de envelope): `Vec2{ x, z }` (plano 2.5D — la **Y es de la cámara del cliente, nunca se networkea**) y `EntityState{ id, pos, velocity, yaw }` (**yaw en radianes**; **no existen** pitch/roll, es estructuralmente imposible).

| Id | Mensaje | Dirección | Transporte | Campos clave | Notas |
|----|---------|-----------|------------|--------------|-------|
| 1 | `Hello` | C → S | TCP | `protoVer (int32)` | Abre la sesión y negocia versión. Fuera del rango del server → `VersionMismatch` + cierre. |
| 2 | `ServerInfo` | S → C | TCP | `protoVer`, `tickRate (20)`, `serverTime (ms epoch)` | Respuesta a `Hello`. Usá `serverTime` para time-sync. |
| 3 | `AuthRequest` | C → S | TCP | `username`, `password` | Dev mode: acepta cualquier credencial (`username` = `playerId`). |
| 4 | `AuthResponse` | S → C | TCP | `ok`, `playerId`, `spawnPos (Vec2)`, `udpToken (bytes)`, `errorMessage` | `ok=false` → `errorMessage` y la sesión se cierra. `udpToken` = 16 bytes. |
| 5 | `EnterWorld` | C → S | TCP | — (vacío) | Solo válido tras auth OK. |
| 6 | `WorldSnapshot` | S → C | TCP | `entities[] (EntityState)` | Llegan dos: vacío (ack de `EnterWorld`, needs-ack) + real (estado completo, v1 flat). |
| 7 | `MoveInput` | C → S | UDP | `seq (int32, del cliente)`, `dir (Vec2)`, `speed (float)`, `yaw (float)` | Intención: el server valida y clampa (`≤ 10 u/s`), normaliza `dir` e integra autoritativamente. |
| 8 | `Snapshot` | S → C | UDP | `seq (int32, server, monotónico)`, `entities[]` | 10 Hz. Descartá `seq ≤ last-applied` (duplicados y viejos). |
| 9 | `VersionMismatch` | S → C | TCP | `minVer`, `maxVer` | Se envía y **cierra** la sesión. |
| 10 | `Ack` | ambos | TCP | `seq (uint32)` | Acuse de un frame con `needs-ack`. v1: **sin retransmisión**. Un `Ack` nunca se contesta con otro `Ack`. |
| 11 | `SpawnEntity` | S → C | TCP | `entityId`, `state (EntityState)` | Cuando una entidad entra en tu interés (v1 flat: al hacer join). Lleva estado completo para no esperar al próximo snapshot. |
| 12 | `DespawnEntity` | S → C | TCP | `entityId` | Cuando una entidad sale de tu interés (v1 flat: al dejar la sesión). Solo el id — removela. |

---

## 6. Reglas de serialización

- **proto3**, paquete `mmo.v1`, namespace C# `Mmo.V1`.
- **Los números de campo son parte del contrato.** No se reordenan ni se reutilizan. Un campo removido se marca `reserved` en el `.proto` — nunca se reasigna el número.
- **PROHIBIDO en el cliente Unity**: `DynamicMessage`, `Any`, `JsonParser`. Son rutas de reflexión que rompen IL2CPP/AOT. Serializá solo con las clases generadas.
- **Código generado commiteado**: `proto/v1/gen/csharp/World.cs`. **No necesitás protoc** — el Unity dev nunca corre tooling de protobuf.
- **Campos desconocidos se preservan** (compatibilidad de minor — sección 9): un mensaje nuevo con campos que tu versión no conoce no es un error, se ignora y se re-envía intacto si lo re-serializás.
- Precisión: `float32` para pos/vel/yaw; `int32`/`int64` para secuencias y tiempos.

---

## 7. Movimiento & sync del mundo

- **Tick 20 Hz** server-authoritative (delta fijo 50 ms). Cinemática: `pos += velocity × dt`. **Sin colisión ni navmesh en v1.**
- **`MoveInput`**: `dir` se normaliza, `speed` se clampa al límite del server (**10 u/s**), y el input se etiqueta al tick que lo procesa. **Over-speed → clamped**: la posición autoritativa nunca sigue un input sin validar.
- **Snapshots a 10 Hz** por jugador (cada 2 ticks), **staggered** (desfasados) entre jugadores para repartir la carga. `Snapshot.seq` es **monotónico** del server.
- **Corrección**: el snapshot es el **estado canónico** — posición + velocidad + yaw. Sin predicción en v1: el cliente aplica el último snapshot y **descarta los viejos** (duplicados y `seq ≤ last-applied`).
- **Yaw en radianes, crudo** (0..2π). No hay normalización en el wire. Un futuro delta-encoding **debe** manejar el wrap-around en 0/2π — recibir `6.28` y después `0.0` es un giro legítimo, no un salto. El render del cliente debe interpolar asumiendo el camino más corto.
- **Tres `seq` distintas** (no confundir):
  - `envelope.seq` — per-session, solo en frames con `needs-ack` (ej.: el `WorldSnapshot`-ack del server).
  - `Snapshot.seq` — monotónico del server, para descartar snapshots viejos.
  - `MoveInput.seq` — del cliente, tag de cada input (el server lo asocia al tick).
- **Interés (v1 flat)**: cada jugador ve a **todos** los demás. Al entrar, tu `WorldSnapshot` real ya lleva a todos; los que ya estaban reciben `SpawnEntity` tuyo; al salir, los demás reciben `DespawnEntity`. (El grid de chunks de 64 u / 5×5 es futuro — el contrato ya lo soporta sin cambios de mensajes.)

---

## 8. Códigos de error

v1 **no tiene códigos numéricos de error en el wire**. Las fallas se manifiestan como un mensaje explícito y/o el cierre de la conexión:

| Condición | Qué ves en el wire | Canal | Comportamiento |
|-----------|-------------------|-------|----------------|
| Versión fuera de rango | `VersionMismatch{ minVer, maxVer }` | TCP | Se envía y se cierra. |
| Auth fallida | `AuthResponse{ ok:false, errorMessage }` | TCP | Se envía y se cierra. |
| Mensaje ilegal (fuera de estado) | — (sin mensaje) | TCP | Se cierra. |
| Timeout de handshake (> 10 s) | — (sin mensaje) | TCP | Se cierra. |
| Frame TCP > 64 KiB | — (sin mensaje) | TCP | Se cierra (`ErrFrameTooLarge`). |
| Stream TCP truncado | — | TCP | Error de frame incompleto; nunca un frame corrupto. |
| Datagrama UDP > 1200 B | — | UDP | Rechazado entero, nunca fragmentado. |
| Magic inválido / tipo desconocido | — (sin mensaje) | TCP/UDP | Se cierra (TCP) o se ignora (UDP). |
| Token UDP incorrecto/desconocido | — (sin mensaje) | UDP | Ignorado — no bindea. |

> En TCP, ante cualquier violación el server **cierra** la conexión: el cliente debe reintentar con una sesión nueva. En UDP no hay cierre — los frames inválidos simplemente se descartan.

---

## 9. Versionado & compatibilidad

- **`envelope.version` = major del protocolo.** Se negocia en el handshake: tanto el `version` del envelope como `Hello.protoVer` deben caer dentro de `[minVer, maxVer]` del server.
- **Mismatch → `VersionMismatch` + cierre** (antes de cerrar, te dice el rango soportado).
- **Server v1: rango `[1, 1]`.** Un cliente con major ≠ 1 será rechazado.
- **Minor bumps son aditivos**: nuevos mensajes con ids nuevos, nuevos campos con números nuevos, campos removidos con `reserved`. Los campos desconocidos se preservan → un cliente viejo y un server nuevo (o al revés) interoperan sin romperse.
- El doc y el `.proto` evolucionan juntos; el `.proto` es la fuente de verdad.

---

## 10. Checklist Unity / IL2CPP

- [ ] **Google.Protobuf ≥ 3.35.1** (NuGet) — el C# generado se produjo con `protoc-gen-csharp v35.1`; usá el runtime de esa línea.
- [ ] **AOT-safe**: sin `DynamicMessage`, sin `Any`, sin `JsonParser` (rutas de reflexión → prohibidas en IL2CPP).
- [ ] Usá **solo las clases generadas** de `Mmo.V1` (`proto/v1/gen/csharp/World.cs`, commiteado — no corras protoc).
- [ ] Serializá/deserializá con `MessageParser` del código generado. Nada de serialización a mano, nada de gob.
- [ ] Transport: `TcpClient` + `UdpClient` de .NET (o el reemplazo Unity que uses). El server solo habla raw TCP/UDP.
- [ ] TCP: escribí `[4 bytes BE longitud] + envelope`; leé igual (reensamblá por longitud).
- [ ] UDP: un datagrama = un envelope (`≤ 1200 B`). El primer datagrama es el token **crudo**.
- [ ] Descartá `Snapshot.seq ≤ last-applied` y contestá `Ack` a los frames con flag `needs-ack`.
- [ ] Probá contra el server local: `go run ./cmd/server` o `docker compose up`.
