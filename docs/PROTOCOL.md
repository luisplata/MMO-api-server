# Protocolo World Simulation v2 — contrato de wire

> **Para**: desarrollador del cliente web (TypeScript/protobuf-es) y mantenedores del contrato.
> **Qué es**: el contrato binario entre `mmo-api-server` (Go) y los clientes (browser TS y Unity/C#) sobre **WSS**.
> **Fuente de verdad**: `proto/v1/world.proto`. El código generado (Go, C# y TypeScript) está **commiteado** en `proto/v1/gen/` — no necesitás protoc ni buf.
> **Versión del contrato**: v2 (rango negociado `[2, 2]`). Un cliente v1 es rechazado con `VersionMismatch` antes de auth (fail-fast).
> **Terreno**: el mapa de alturas tiene su propio contrato en [`HEIGHTMAP.md`](HEIGHTMAP.md) — el server samplea el terreno y la altura viaja en `pos.y`/`spawnPos.y` (ver §5).
> **Personajes**: el flujo de personajes (listar/crear/seleccionar antes de entrar al mundo, `templateId`) tiene su hoja de handoff Unity en [`UNITY_CHARACTER_HANDOFF.md`](UNITY_CHARACTER_HANDOFF.md) (ver §4 y §5).

---

## 1. TL;DR — conectarse en 5 pasos

1. Abrí **`wss://<proxy-host>/ws`** (TLS termina en el proxy) y mandá `Hello{ protoVer: 2 }`.
2. Recibís `ServerInfo` (versión, `tickRate`, `serverTime`) → mandá `AuthRequest{ username, password }`.
3. Recibís `AuthResponse{ ok, playerId }` y quedás en **`selecting`**: `ListCharacters`, `CreateCharacter` + `SelectCharacter` (este último resuelve `spawnPos` + `stats` + `templateId`).
4. En `selecting` mandá `JoinRoom{ mode: 1 }` (`NextAvailable`) o `{ mode: 2, roomId }` (`ByID`) → `JoinRoomResponse{ roomId, capacity, occupancy }`. `EnterWorld` exige personaje **y** sala.
5. Mandá `EnterWorld` → dos `WorldSnapshot` (vacío = ack, real = estado). Estado estable: `MoveInput`/`Snapshot` por el mismo WSS y `Ping`/`Pong` para latencia.

```ts
import { create } from "@bufbuild/protobuf";
import { JoinRoomSchema, type JoinRoom } from "./gen/v1/world_pb";

const join: JoinRoom = create(JoinRoomSchema, { mode: 1, roomId: "" });
// mode=1 NextAvailable (roomId ignorado); mode=2 ByID (roomId conocido)
```

---

## 2. Transporte único: WSS

Todos los mensajes (ids 1–22) viajan por **una sola conexión WebSocket binaria**. No hay canales TCP/UDP separados ni fallback a `ws://`.

| Propiedad | Valor |
|-----------|-------|
| Endpoint público | `wss://<proxy-host>/ws` (TLS en el proxy) |
| Upstream Go | privado `:8080`, nunca publicado |
| Framing | 1 mensaje WebSocket binario = 1 frame |
| Límite | 64 KiB por frame |
| Fiabilidad | Lifecycle/comandos por cola fiable; movimiento/snapshots por cola realtime (depth-1) |

- `Ack` sigue existiendo (v2 sin retransmisión); no hay KCP ni datagramas.
- El server no escucha en ningún puerto público de juego; el proxy es el único listener.

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
| 2–3 | u16 | `version` | Major del protocolo (v2 = 2). Validado en el handshake. |
| 4–5 | u16 | `type` | Id del mensaje (catálogo, sección 5). |
| 6 | u8 | `flags` | Ver tabla de flags abajo. |
| 7–10 | u32 | `seq` | Per-session monotónico, **solo** para frames con `needs-ack`. |
| 11+ | — | `payload` | Protobuf del mensaje. |

**Flags (bitmask)**: bit0 `needs-ack` (0x01), bit1 `compressed` (0x02, **reservado v2**), bit2 `encrypted` (0x04, **reservado v2**).

**Límite**: el frame total (header + payload) no puede superar **64 KiB**.

> ⚠️ No confundir el `seq` del **envelope** (per-session, para needs-ack) con el `seq` del **`Snapshot`** (monotónico del server) ni con el `seq` del **`MoveInput`** (del cliente). Son tres contadores distintos — detalle en la sección 7.

### WSS

- Cada mensaje WebSocket **binario** lleva exactamente un frame (envelope completo).
- El server valida el tamaño (**≤ 64 KiB**) y cierra la conexión si un frame lo excede.
- No hay prefijo de longitud ni datagramas: el framing lo provee WebSocket.
- Un frame truncado o inválido cierra la conexión; nunca se entrega parcial.

---

## 4. Handshake & ciclo de vida

```
Cliente (browser)                        Servidor (mmo-api-server)
      |  WSS: conecta /ws                     |
      |────── Hello{ protoVer: 2 } ──────────►|
      |◄───── ServerInfo{ protoVer:2, tickRate:20, serverTime } ─|
      |────── AuthRequest{ username, password } ►|
      |◄───── AuthResponse{ ok, playerId } ──|  ← fase selecting
      |────── ListCharacters ────────────────►|
      |◄───── CharacterList{ characters[] } ──|
      |────── CreateCharacter{ templateId, name } ►|
      |◄───── CreateCharacterResponse{ ok, character } ─|
      |────── SelectCharacter{ characterId } ►|
      |◄───── SelectCharacterResponse{ ok, character, spawnPos } ─|
      |────── JoinRoom{ mode, roomId } ──────►|
      |◄───── JoinRoomResponse{ roomId, capacity, occupancy } ─|  ← selecting → entering
      |────── EnterWorld ────────────────────►|
      |◄───── WorldSnapshot (VACÍO — ack de EnterWorld, needs-ack) ─|
      |◄───── WorldSnapshot (REAL — estado completo del mundo) ─|
      |────── MoveInput / Ping ──────────────►|  ← mismo WSS
      |◄───── Snapshot{ seq, entities } / Pong ─|  ← 10 Hz / eco
```

Reglas del ciclo de vida:

- **Fase `selecting`**: tras un `AuthResponse` ok, `ListCharacters`/`CreateCharacter`/`SelectCharacter`/`JoinRoom` son los mensajes válidos. `SelectCharacter` resuelve `spawnPos` + `stats` + `templateId`; `JoinRoom` reserva una sala.
- **Dos requisitos**: la sesión pasa a `entering` solo con personaje seleccionado **y** sala reservada; `EnterWorld` sin cualquiera de los dos → error de protocolo y cierre.
- **Fuera de orden = cierre.** Cada mensaje solo es válido en su estado (ej.: `EnterWorld` antes de auth → cierre).
- **Dos `WorldSnapshot` al entrar**: el primero es el ack (vacío, `needs-ack` — contestalo con `Ack`); el segundo es el estado **real** del mundo (v2 flat fanout). Cada `EntityState` lleva el `templateId` del personaje.
- **Sala**: `JoinRoomResponse` all-zero significa que no se reservó nada (sala llena, inexistente o modo inválido). `roomId` es un identificador, **no** una credencial.
- **Timeout de handshake**: 10 s desde la conexión (hardcodeado en `cmd/server/main.go` — no es flag).
- **Login duplicado**: un segundo `playerId` ya conectado se rechaza.
- **Sin bind UDP**: todo el tráfico, incluido `MoveInput`/`Snapshot`, usa la misma conexión WSS. `AuthResponse.udpToken` queda en el schema por compatibilidad y se ignora.

---

## 5. Catálogo de mensajes (ids 1–22)

Tipos embebidos (no son mensajes de envelope): `Vec2{ x, z }` (plano de tierra: velocidad y `MoveInput.dir`), `Vec3{ x, y, z }` (**posición y spawn** — la `y` es la **altura derivada del terreno**, calculada por el server a partir del mapa de alturas, **nunca enviada por el cliente**; la cámara sigue siendo client-local, no se networkea), `EntityState{ id, pos, velocity, yaw, templateId }` (**yaw en radianes**; `templateId` es la **plantilla del personaje** — aditivo, campo 5 — para que el cliente elija el modelo visual, vacío en entidades no-personaje; **no existen** pitch/roll, es estructuralmente imposible), `Stats{ hp, speed, atk, def }` (los valores numéricos de la hoja del personaje) y `Character{ id, accountId, name, templateId, stats, createdAt }` (un personaje de la cuenta; `id` es la **entidad del mundo** cuando se selecciona).

| Id | Mensaje | Dirección | Transporte | Campos clave | Notas |
|----|---------|-----------|------------|--------------|-------|
| 1 | `Hello` | C → S | WSS | `protoVer (int32)` | Abre la sesión y negocia versión. Fuera del rango del server (`[2,2]`) → `VersionMismatch` + cierre. |
| 2 | `ServerInfo` | S → C | WSS | `protoVer`, `tickRate (20)`, `serverTime (ms epoch)` | Respuesta a `Hello`. Usá `serverTime` para time-sync. |
| 3 | `AuthRequest` | C → S | WSS | `username`, `password` | Dev mode: acepta cualquier credencial (`username` = `playerId`). |
| 4 | `AuthResponse` | S → C | WSS | `ok`, `playerId`, `udpToken (bytes)`, `errorMessage` | `ok=false` → `errorMessage` y la sesión se cierra. `spawnPos` ya no se usa (lo resuelve `SelectCharacter`); `udpToken` es un campo legacy ignorado por clientes WSS. |
| 5 | `EnterWorld` | C → S | WSS | — (vacío) | Solo válido con personaje seleccionado y sala reservada. |
| 6 | `WorldSnapshot` | S → C | WSS | `entities[] (EntityState)` | Llegan dos: vacío (ack de `EnterWorld`, needs-ack) + real (estado completo, v2 flat). |
| 7 | `MoveInput` | C → S | WSS | `seq (int32, del cliente)`, `dir (Vec2)`, `speed (float)`, `yaw (float)` | Intención: el server valida y clampa (`≤ 10 u/s`), normaliza `dir` e integra autoritativamente. **No lleva altura.** |
| 8 | `Snapshot` | S → C | WSS | `seq (int32, server, monotónico)`, `entities[]` | 10 Hz. Descartá `seq ≤ last-applied` (duplicados y viejos). |
| 9 | `VersionMismatch` | S → C | WSS | `minVer`, `maxVer` | Se envía y **cierra** la sesión. |
| 10 | `Ack` | ambos | WSS | `seq (uint32)` | Acuse de un frame con `needs-ack`. v2: **sin retransmisión**. Un `Ack` nunca se contesta con otro `Ack`. |
| 11 | `SpawnEntity` | S → C | WSS | `entityId`, `state (EntityState)` | Cuando una entidad entra en tu interés (v2 flat: al hacer join). Lleva estado completo (incluido `templateId`) para no esperar al próximo snapshot. |
| 12 | `DespawnEntity` | S → C | WSS | `entityId` | Cuando una entidad sale de tu interés (v2 flat: al dejar la sesión). Solo el id — removela. |
| 13 | `ListCharacters` | C → S | WSS | — (vacío) | Pide los personajes de la cuenta autenticada. Solo válido en `selecting`. |
| 14 | `CharacterList` | S → C | WSS | `characters[] (Character)` | Respuesta a `ListCharacters`. Vacío si la cuenta no tiene personajes (no es error). |
| 15 | `CreateCharacter` | C → S | WSS | `templateId`, `name` | Crea un personaje para la cuenta. Solo válido en `selecting`. Valida plantilla, nombre (trim 3–16, `[a-zA-Z0-9_]`) y unicidad por cuenta. |
| 16 | `CreateCharacterResponse` | S → C | WSS | `ok`, `character (Character)`, `errorMessage` | `ok=true` → `character` (id + `templateId` + stats congeladas de la plantilla). `ok=false` → `errorMessage` (plantilla inexistente, nombre inválido/duplicado); **no muta** estado. |
| 17 | `SelectCharacter` | C → S | WSS | `characterId` | Activa un personaje de la cuenta como entidad del mundo. Solo válido en `selecting`; verifica que exista y pertenezca a la cuenta. |
| 18 | `SelectCharacterResponse` | S → C | WSS | `ok`, `character (Character)`, `spawnPos (Vec3)`, `errorMessage` | `ok=true` → `spawnPos` (altura del terreno resuelta) + `character` (`stats` + `templateId`) y la sesión pasa a `entering`. `ok=false` → `errorMessage` (personaje ajeno/inexistente); el activo no cambia. |
| 19 | `Ping` | C → S | WSS | `nonce (uint64)`, `clientTime (int64, ms epoch)` | Probe de latencia. El server lo responde fuera del tick; máximo 5/s por sesión (el exceso se descarta). |
| 20 | `Pong` | S → C | WSS | `nonce`, `clientTime` | Echo inmediato del `Ping`. Calculá RTT con `clientTime`; ver §7. |
| 21 | `JoinRoom` | C → S | WSS | `mode (int32)`, `roomId (string)` | Modo 1 `NextAvailable` (ignora `roomId`): reutiliza cupo o crea sala. Modo 2 `ByID`: une a una sala pública conocida. Cualquier otro modo → respuesta cero. |
| 22 | `JoinRoomResponse` | S → C | WSS | `roomId`, `capacity`, `occupancy` | All-zero = nada reservado (lleno/inexistente/modo inválido). Capacidad 2, sin espectadores ni salas privadas. |

### Sala pública (MVP)

- Dos modos únicamente: `NextAvailable` (1) y `ByID` (2). No existe `Create`, `ByCode` ni `roomCode`.
- Capacidad **2 jugadores activos**; sin espectadores. El segundo entra en vivo en el spawn fijo del server, sin ready gate.
- La reserva tiene lease de **30 s**; la desconexión la libera.
- `roomId` es un identificador, no una credencial; la admisión depende de cupo, estado y ownership.
- Salas privadas/por código: SDD aparte, requieren auth real.

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

- **Tick 20 Hz** server-authoritative (delta fijo 50 ms). Cinemática: `pos += velocity × dt` (en el plano XZ). **Sin colisión ni navmesh en v2.**
- **La altura es derivada, no integrada**: después de cada tick el server resuelve `Y = HeightAt(X, Z)` sobre el mapa de alturas activo (contrato en `HEIGHTMAP.md`). `MoveInput` **nunca** lleva altura ni la modifica — el input solo afecta XZ. Sin resolver de terreno → `Y = 0` (mundo plano).
- **`MoveInput`**: `dir` se normaliza, `speed` se clampa al límite del server (**10 u/s**), y el input se etiqueta al tick que lo procesa. **Over-speed → clamped**: la posición autoritativa nunca sigue un input sin validar.
- **Input continuo mientras la tecla está apretada (cliente)**: mandá `MoveInput` de forma sostenida, como máximo **uno por tick (~20/s)**. Al soltar la tecla, mandá **inmediatamente un `MoveInput` neutro** (`dir = (0,0)`, `speed = 0`); el server lo procesa y la velocidad autoritativa queda en cero en ese mismo tick.
- **Watchdog de input obsoleto (server)**: si pasan **10 ticks (500 ms a 20 Hz)** sin un `MoveInput` procesado para esa entidad, el server pone la velocidad autoritativa en cero **antes de integrar**, así el personaje no avanza en el tick de expiración. Cualquier input procesado renueva el plazo; el neutro corta en su tick de procesamiento. Es determinista por tick (sin timer de wall-clock) y no cambia proto ni versión.
- **Snapshots a 10 Hz** por jugador (cada 2 ticks), **staggered** (desfasados) entre jugadores para repartir la carga. `Snapshot.seq` es **monotónico** del server.
- **Corrección**: el snapshot es el **estado canónico** — posición (XZ + Y derivada) + velocidad + yaw. Sin predicción en v2: el cliente aplica el último snapshot y **descarta los viejos** (duplicados y `seq ≤ last-applied`).
- **Yaw en radianes, crudo** (0..2π). No hay normalización en el wire. Un futuro delta-encoding **debe** manejar el wrap-around en 0/2π — recibir `6.28` y después `0.0` es un giro legítimo, no un salto. El render del cliente debe interpolar asumiendo el camino más corto.
- **Tres `seq` distintas** (no confundir):
  - `envelope.seq` — per-session, solo en frames con `needs-ack` (ej.: el `WorldSnapshot`-ack del server).
  - `Snapshot.seq` — monotónico del server, para descartar snapshots viejos.
  - `MoveInput.seq` — del cliente, tag de cada input (el server lo asocia al tick).
- **Interés (v2 flat)**: cada jugador ve a **todos** los demás. Al entrar, tu `WorldSnapshot` real ya lleva a todos; los que ya estaban reciben `SpawnEntity` tuyo; al salir, los demás reciben `DespawnEntity`. (El grid de chunks de 64 u / 5×5 es futuro — el contrato ya lo soporta sin cambios de mensajes.)
- **Cámara**: es client-local. Nunca se networkea (no hay pitch/roll en el contrato); el cliente posiciona su cámara por su cuenta sobre el estado autoritativo XZ+Y.

### Latencia de aplicación: Ping/Pong (ids 19–20)

- **Dirección y campos**: `Ping` C→S y `Pong` S→C por **WSS**; `nonce (uint64)` y `clientTime (int64, ms epoch)`. El server hace **echo idéntico** de ambos. Aditivos: no cambian envelope ni versionado.
- **Fuera del tick**: se responde en el read loop de la conexión. Nunca encola `MoveInput`, nunca muta la simulación y no altera la cadencia de 20 Hz ni los snapshots. Máximo **5 pings/s por sesión**; el excedente se descarta en silencio (sin error ni cierre).
- **RTT / EMA / jitter (cliente)**: `rtt = now - clientTime`; suavizá con `ema = α·rtt + (1-α)·ema` (α≈0.2) y calculá `jitter = EMA(|rtt - ema|)`. Usá el `nonce` para descartar ecos fuera de orden.
- **Stale**: tras **~10 s sin `Pong` ni `Snapshot`**, marcá el enlace como stale; los pings siguen limitados a 5/s. Ni `Ping` ni `Pong` llevan `needs-ack`.

---

## 8. Códigos de error

v2 **no tiene códigos numéricos de error en el wire**. Las fallas se manifiestan como un mensaje explícito y/o el cierre de la conexión:

| Condición | Qué ves en el wire | Canal | Comportamiento |
|-----------|-------------------|-------|----------------|
| Versión fuera de rango | `VersionMismatch{ minVer, maxVer }` | WSS | Se envía y se cierra. |
| Auth fallida | `AuthResponse{ ok:false, errorMessage }` | WSS | Se envía y se cierra. |
| Mensaje ilegal (fuera de estado) | — (sin mensaje) | WSS | Se cierra. |
| Timeout de handshake (> 10 s) | — (sin mensaje) | WSS | Se cierra. |
| Frame WSS > 64 KiB | — (sin mensaje) | WSS | Se cierra (`ErrFrameTooLarge`). |
| Frame WSS truncado | — | WSS | Error de frame incompleto; nunca un frame corrupto. |
| Magic inválido / tipo desconocido | — (sin mensaje) | WSS | Se cierra la conexión. |
| `JoinRoom` lleno/inexistente/modo inválido | `JoinRoomResponse{}` (all-zero) | WSS | Sin reserva; la sesión sigue en `selecting`. |

> Ante cualquier violación de framing o estado, el server **cierra** la conexión: el cliente debe reintentar con una sesión nueva.

### Rechazos de character-management (no cierran la sesión)

A diferencia de las violaciones de estado de arriba, los rechazos de `CreateCharacter`/`SelectCharacter` se responden con un `ok:false` + `errorMessage` y la sesión **permanece abierta** en `selecting` (el cliente puede corregir y reintentar). Rechazos posibles:

| Condición | Mensaje | Canal |
|-----------|---------|-------|
| Plantilla inexistente | `CreateCharacterResponse{ ok:false, errorMessage:"template not found" }` | WSS |
| Nombre inválido (vacío / fuera de rango / charset) | `CreateCharacterResponse{ ok:false, errorMessage:<razón> }` | WSS |
| Nombre duplicado por cuenta | `CreateCharacterResponse{ ok:false, errorMessage:"name already used in account" }` | WSS |
| Personaje inexistente | `SelectCharacterResponse{ ok:false, errorMessage:"character not found" }` | WSS |
| Personaje ajeno (de otra cuenta) | `SelectCharacterResponse{ ok:false, errorMessage:"character not owned by account" }` | WSS |

---

## 9. Versionado & compatibilidad

- **`envelope.version` = major del protocolo.** Se negocia en el handshake: tanto el `version` del envelope como `Hello.protoVer` deben caer dentro de `[minVer, maxVer]` del server.
- **Mismatch → `VersionMismatch` + cierre** (antes de cerrar, te dice el rango soportado).
- **Server v2: rango `[2, 2]`.** Un cliente con major ≠ 2 (incluidos todos los v1) es rechazado **fail-fast** antes de auth.
- **Lockstep del cliente**: v2 cambia la forma de la posición (`Vec2` → `Vec3`) y agrega la fase `selecting` + los mensajes de personaje (ids 13–18) y `EntityState.templateId`. El cliente Unity **debe** actualizar su `World.cs` generado **en la misma ventana** que el server v2 — un `World.cs` viejo contra un server v2 no puede interoperar (shape distinta, y el flujo de personaje no existe) y un cliente v1 es rechazado por versión. No hay interoperabilidad v1↔v2.
- **Minor bumps son aditivos**: nuevos mensajes con ids nuevos, nuevos campos con números nuevos, campos removidos con `reserved`. Los campos desconocidos se preservan → un cliente viejo y un server nuevo (o al revés) interoperan **dentro del mismo major** sin romperse. Los ids 13–18 y `EntityState.templateId` ya están en el `World.cs` commiteado (v2) — no requieren protoc del lado Unity.
- El doc y el `.proto` evolucionan juntos; el `.proto` es la fuente de verdad.

---

## 10. Checklist Unity / IL2CPP

- [ ] **Google.Protobuf ≥ 3.35.1** (NuGet) — el C# generado se produjo con `protoc-gen-csharp v35.1`; usá el runtime de esa línea.
- [ ] **`World.cs` v2 (lockstep)**: regenerá/reemplazá el `World.cs` del cliente con el del repo (`proto/v1/gen/csharp/World.cs`) en la misma ventana que el server v2. Contiene `Vec3`, los mensajes de personaje (ids 13–18) y `EntityState.templateId`.
- [ ] **Fase `selecting`**: tras `AuthResponse`, NO vayas directo a `EnterWorld`. Mostrá la lista (`ListCharacters`), creá/seleccioná (`CreateCharacter` + `SelectCharacter`) y **recién ahí** mandá `EnterWorld`. `EnterWorld` sin personaje seleccionado cierra la sesión.
- [ ] **`templateId`**: usalo de `SelectCharacterResponse.character.templateId`, del `WorldSnapshot` y de cada `SpawnEntity`/`Snapshot` para elegir el prefab visual del personaje. El campo es aditivo y vacío para entidades no-personaje.
- [ ] **AOT-safe**: sin `DynamicMessage`, sin `Any`, sin `JsonParser` (rutas de reflexión → prohibidas en IL2CPP).
- [ ] Usá **solo las clases generadas** de `Mmo.V1` (`proto/v1/gen/csharp/World.cs`, commiteado — no corras protoc).
- [ ] Serializá/deserializá con `MessageParser` del código generado. Nada de serialización a mano, nada de gob.
- [ ] Transport: `ClientWebSocket` de .NET (o el equivalente Unity/browser) contra `wss://<proxy-host>/ws`; un mensaje binario = un envelope.
- [ ] El cliente web usa el contrato TypeScript commiteado (`proto/v1/gen/es/v1/world_pb.ts`, protobuf-es 2.16.0); no hay bindings C# para el web client.
- [ ] Sin bind UDP ni datagramas: `MoveInput`/`Snapshot` viajan por la misma conexión WSS.
- [ ] Descartá `Snapshot.seq ≤ last-applied` y contestá `Ack` a los frames con flag `needs-ack`.
- [ ] **Tratá `pos.y`/`spawnPos.y` como altura autoritativa**: no la generes, no la mandes, no la corrijas. La cámara la posiciona el cliente sobre el estado autoritativo.
- [ ] **Terreno**: si el server corre con un mapa (`-map`), el cliente no necesita el heightmap — la altura ya viaja en los snapshots. El heightmap es solo para el pipeline de autoría (ver `HEIGHTMAP.md`).
- [ ] **Personajes**: la hoja de handoff completa del flujo de personajes está en [`UNITY_CHARACTER_HANDOFF.md`](UNITY_CHARACTER_HANDOFF.md).
- [ ] Probá contra el server local: `go run ./cmd/server` o `docker compose up`.

---

## 11. Bindings TypeScript (protobuf-es v2)

`proto/v1/gen/es/v1/world_pb.ts` es el contrato completo (ids 1-22), generado con protobuf-es v2.16.0 y commiteado. Regeneralo con `npm ci && make proto-es`; `make proto-es-check` falla ante drift.

Usá `@bufbuild/protobuf` **2.16.0** (fijado en `package.json`/lockfile) e imports sin extensión (v2).
