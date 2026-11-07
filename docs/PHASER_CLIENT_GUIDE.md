# Guía del cliente Phaser — primer playable contra `mmo-api-server`

> **Para**: Duarte (cliente web externo, Phaser + TypeScript).
> **Qué es**: la ruta mínima y verificada para conectar un cliente Phaser al server, autenticar, crear/elegir personaje, entrar a una sala pública, renderizar el estado autoritativo, medir RTT y reconectar.
> **Fuente de verdad**: `proto/v1/world.proto` y el código del checkout actual. Verificado contra el HEAD `2c53b79` (P22/G0). Si esta guía y el código discrepan, gana el código.
> **Transporte**: una sola conexión WebSocket binaria. En producción, `wss://<proxy-host>/ws`. No hay TCP/UDP separados, ni prefijo de longitud, ni fallback público a `ws://`.
> **Alcance**: salas públicas, 2 jugadores activos por sala, sin espectadores, auth de desarrollo. Fuera de alcance: salas privadas/por código, auth real, combate, mundo compartido multi-réplica.

---

## 0. Resultado esperado

Al terminar esta guía tu cliente puede:

1. abrir el socket y negociar la versión;
2. autenticarse en modo dev;
3. listar, crear y seleccionar un personaje;
4. reservar una sala pública y entrar;
5. dibujar a los dos jugadores con el estado del server;
6. enviar intención de movimiento;
7. medir RTT con `Ping`/`Pong`;
8. reconectar desde cero.

El camino completo **Hello → EnterWorld tiene que caber en 10 segundos** desde la conexión. Es el límite menos obvio de todo el flujo y está verificado en `internal/session/handshake.go`; se explica en §3.4.

---

## 1. Camino rápido (orden del lifecycle)

| # | Paso | Mensaje | Estado resultante | Qué verificar |
|---|------|---------|-------------------|---------------|
| 1 | Abrir `wss://<proxy-host>/ws` (o `ws://127.0.0.1:8080/ws` en local) | — | `connecting` | `binaryType = "arraybuffer"` |
| 2 | Enviar `Hello` | `Hello{ protoVer: 2 }` | `handshaking` | Envelope `version = 2` |
| 3 | Recibir `ServerInfo` | `ServerInfo{ protoVer, tickRate, serverTime }` | `handshaking` | `tickRate = 20` |
| 4 | Enviar `AuthRequest` | `{ username, password }` | `authenticating` → `selecting` | Usuario no vacío |
| 5 | Recibir `AuthResponse` | `{ ok: true, playerId, udpToken }` | `selecting` | `ok: true` |
| 6 | Listar personajes | `ListCharacters` → `CharacterList` | `selecting` | Lista vacía **no** es error |
| 7 | Crear si hace falta | `CreateCharacter{ templateId, name }` → `CreateCharacterResponse` | `selecting` | `ok: true` |
| 8 | Seleccionar | `SelectCharacter{ characterId }` → `SelectCharacterResponse` | `selecting` o `entering` | Guardar `spawnPos` |
| 9 | Unirse a sala | `JoinRoom{ mode, roomId }` → `JoinRoomResponse` | `selecting` o `entering` | Respuesta **no** all-zero |
| 10 | Entrar al mundo | `EnterWorld` → `WorldSnapshot` vacío (ack) + `WorldSnapshot` real | `in-world` | Vacío = ack; real = estado; puede intercalarse un `Snapshot` |
| 11 | Jugar | `MoveInput`, `Ping` → `Snapshot`, `Pong`, `SpawnEntity`, `DespawnEntity` | `in-world` | Aplicar snapshots por `seq` |
| 12 | Reconectar | Socket nuevo desde el paso 1 | `connecting` | No hay resume de sesión |

**Orden de Select y JoinRoom**: podés hacerlos en cualquier orden; el server acepta ambos (verificado en `handshake.go`). Recomendación: `SelectCharacter` y después `JoinRoom`, como en este camino. Una vez que tenés personaje **y** sala, la sesión pasa a `entering` y ya no podés mandar `ListCharacters`, `CreateCharacter`, `SelectCharacter` ni `JoinRoom`: el próximo mensaje válido es `EnterWorld`.

---

## 2. Transporte, framing y envelope

### 2.1 Una sola conexión WSS

| Propiedad | Valor |
|-----------|-------|
| Endpoint público | `wss://<proxy-host>/ws` (TLS termina en el proxy) |
| Upstream Go | `:8080` privado, plano, nunca publicado |
| Framing | **1 mensaje WebSocket binario = 1 envelope completo** |
| Límite | **64 KiB por frame** (header + payload) |
| Compresión | Deshabilitada en el server; no dependas de `permessage-deflate` |
| Tipos de mensaje | Solo binario; un frame de texto cierra con `1003` |

- **No hay prefijo de longitud TCP.** El framing lo da WebSocket: un `send()` = un envelope. No concatenes dos envelopes en un mismo mensaje.
- **No hay datagramas ni canal UDP.** `MoveInput` y `Snapshot` viajan por la misma conexión.
- En producción, el browser **solo** habla `wss://` contra el proxy. `ws://` directo al upstream es un modo de desarrollo local, no un fallback público.
- Si el frame supera 64 KiB, el server cierra con **`1009`** (`StatusMessageTooBig`).
- Si mandás texto, el server cierra con **`1003`** (`StatusUnsupportedData`).

### 2.2 Envelope: 11 bytes, big-endian

```
[magic u16 = 0x4D4D][version u16][type u16][flags u8][seq u32] + payload protobuf
```

| Offset | Tamaño | Campo | Regla |
|--------|--------|-------|-------|
| 0–1 | `u16` | `magic` | Siempre `0x4D4D` ("MM") |
| 2–3 | `u16` | `version` | Major del protocolo; **2** |
| 4–5 | `u16` | `type` | Id del mensaje (tabla §2.4) |
| 6 | `u8` | `flags` | bit0 `needs-ack`, bit1 `compressed` (reservado), bit2 `encrypted` (reservado) |
| 7–10 | `u32` | `seq` | Per-sesión; solo significativo en frames con `needs-ack` |
| 11+ | — | `payload` | Protobuf del mensaje |

> **Gotcha**: el comentario de `proto/v1/world.proto` dice "10-byte envelope". Es un miscount: el layout explícito y `internal/protocol/envelope.go` son **11 bytes**. Usá 11.

Reglas del cliente:

- Enviá **`version = 2` en todos los frames**, no solo en `Hello`. El server valida el envelope en cada frame y cierra si está fuera de `[2,2]`.
- Para mensajes normales del cliente: `flags = 0`, `seq = 0`.
- `Ack` es la excepción: el payload lleva el `seq` del frame que estás acusando; el envelope sigue con `flags = 0`.
- No inventes `needs-ack` para tus mensajes: el server no retransmite en v2. (`Ack` existe, pero no hay retransmisión.)
- Magic inválido, frame truncado o type desconocido → el server cierra la conexión; no los ignora.

### 2.3 Codec mínimo en TypeScript (exacto al layout)

```ts
const MAGIC = 0x4d4d;
const HEADER_SIZE = 11;
const MAX_FRAME = 64 * 1024;

function encodeFrame(
  type: number,
  version: number,
  payload: Uint8Array,
  flags = 0,
  seq = 0,
): Uint8Array {
  if (HEADER_SIZE + payload.length > MAX_FRAME) {
    throw new Error("frame too large");
  }
  const frame = new Uint8Array(HEADER_SIZE + payload.length);
  const dv = new DataView(frame.buffer);
  dv.setUint16(0, MAGIC);       // big-endian por defecto
  dv.setUint16(2, version);
  dv.setUint16(4, type);
  dv.setUint8(6, flags);
  dv.setUint32(7, seq);
  frame.set(payload, HEADER_SIZE);
  return frame;
}

function decodeFrame(frame: Uint8Array) {
  if (frame.length < HEADER_SIZE) throw new Error("truncated frame");
  if (frame.length > MAX_FRAME) throw new Error("oversized frame");
  const dv = new DataView(frame.buffer, frame.byteOffset, frame.byteLength);
  if (dv.getUint16(0) !== MAGIC) throw new Error("bad magic");
  return {
    version: dv.getUint16(2),
    type: dv.getUint16(4),
    flags: dv.getUint8(6),
    seq: dv.getUint32(7),
    payload: frame.subarray(HEADER_SIZE),
  };
}

const ws = new WebSocket("wss://game.example.com/ws");
ws.binaryType = "arraybuffer";

function send(typeId: number, payload: Uint8Array) {
  if (ws.readyState !== WebSocket.OPEN) return;
  ws.send(encodeFrame(typeId, 2, payload));
}
```

### 2.4 Catálogo de mensajes (ids 1–22)

Todos son protobuf proto3, paquete `mmo.v1`. `int64`/`uint64` se representan como `bigint` en TypeScript; `bytes` como `Uint8Array`.

| Id | Mensaje | Dirección | Campos importantes | Notas |
|----|---------|-----------|--------------------|-------|
| 1 | `Hello` | C → S | `protoVer` (`int32`) | Primer mensaje de la conexión. |
| 2 | `ServerInfo` | S → C | `protoVer`, `tickRate`, `serverTime` (`int64`, ms epoch) | `tickRate` es 20. |
| 3 | `AuthRequest` | C → S | `username`, `password` | Dev: usuario no vacío; password se ignora. |
| 4 | `AuthResponse` | S → C | `ok`, `playerId`, `spawnPos` (sin uso), `udpToken`, `errorMessage` | `ok=false` → cierra. `udpToken` se ignora. |
| 5 | `EnterWorld` | C → S | — (vacío) | Exige personaje **y** sala. |
| 6 | `WorldSnapshot` | S → C | `entities[]` de `EntityState` | Vacío (ack, `needs-ack`) y real; puede intercalarse un `Snapshot`. |
| 7 | `MoveInput` | C → S | `seq` (`int32`), `dir` (`Vec2`), `speed` (`float`), `yaw` (`float`) | Sin altura; el server valida y clampa. |
| 8 | `Snapshot` | S → C | `seq` (`int32`), `entities[]` | 10 Hz; descartá `seq <= last-applied`. |
| 9 | `VersionMismatch` | S → C | `minVer`, `maxVer` | Se envía y se cierra. |
| 10 | `Ack` | ambos | `seq` (`uint32`) | Acuse de un frame `needs-ack`; no se retransmite. |
| 11 | `SpawnEntity` | S → C | `entityId`, `state` (`EntityState`) | Entra una entidad a tu interés, con estado completo. |
| 12 | `DespawnEntity` | S → C | `entityId` | Sale una entidad de tu interés. |
| 13 | `ListCharacters` | C → S | — (vacío) | Solo en `selecting`. |
| 14 | `CharacterList` | S → C | `characters[]` de `Character` | Vacía si la cuenta no tiene personajes. |
| 15 | `CreateCharacter` | C → S | `templateId`, `name` | Solo en `selecting`. |
| 16 | `CreateCharacterResponse` | S → C | `ok`, `character`, `errorMessage` | Rechazo **no** cierra la sesión. |
| 17 | `SelectCharacter` | C → S | `characterId` | Solo en `selecting`. |
| 18 | `SelectCharacterResponse` | S → C | `ok`, `character`, `spawnPos`, `errorMessage` | Trae `spawnPos` y `templateId`. |
| 19 | `Ping` | C → S | `nonce` (`uint64`), `clientTime` (`int64`, ms epoch) | Máximo 5/s; el exceso se descarta. |
| 20 | `Pong` | S → C | `nonce`, `clientTime` | Echo inmediato. |
| 21 | `JoinRoom` | C → S | `mode` (`int32`), `roomId` (`string`) | Modo 1 `NextAvailable`, 2 `ByID`. |
| 22 | `JoinRoomResponse` | S → C | `roomId`, `capacity`, `occupancy` | All-zero = no se reservó nada. |

Tipos embebidos:

| Tipo | Campos | Uso |
|------|--------|-----|
| `Vec2` | `x`, `z` | Plano de tierra: `velocity` y `MoveInput.dir`. |
| `Vec3` | `x`, `y`, `z` | Posición y spawn; `y` es altura de terreno derivada por el server. |
| `EntityState` | `id`, `pos` (`Vec3`), `velocity` (`Vec2`), `yaw` (`float`), `templateId` (`string`) | Estado autoritativo de una entidad. |
| `Stats` | `hp`, `speed`, `atk`, `def` | Hoja congelada del personaje. |
| `Character` | `id`, `accountId`, `name`, `templateId`, `stats`, `createdAt` | Personaje de la cuenta. |

### 2.5 Binding TypeScript generado

| Dato | Valor |
|------|-------|
| Archivo | `proto/v1/gen/es/v1/world_pb.ts` (commiteado) |
| Runtime | `@bufbuild/protobuf` **2.16.0** (exacto) |
| Codegen | `@bufbuild/protoc-gen-es` **2.16.0** |
| Regenerar | `npm ci && make proto-es` |
| Verificar drift | `make proto-es-check` |
| Imports | Sin extensión (protobuf-es v2) |

El archivo ya contiene los ids 1–22. Copialo a tu cliente (por ejemplo `src/gen/v1/world_pb.ts`) y importá los schemas:

```ts
import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import {
  HelloSchema,
  ServerInfoSchema,
  AuthRequestSchema,
  AuthResponseSchema,
  EnterWorldSchema,
  WorldSnapshotSchema,
  MoveInputSchema,
  SnapshotSchema,
  VersionMismatchSchema,
  AckSchema,
  SpawnEntitySchema,
  DespawnEntitySchema,
  ListCharactersSchema,
  CharacterListSchema,
  CreateCharacterSchema,
  CreateCharacterResponseSchema,
  SelectCharacterSchema,
  SelectCharacterResponseSchema,
  PingSchema,
  PongSchema,
  JoinRoomSchema,
  JoinRoomResponseSchema,
  type Pong,
} from "./gen/v1/world_pb";
```

Ejemplos exactos:

```ts
let inputSeq = 0;

send(1, toBinary(HelloSchema, create(HelloSchema, { protoVer: 2 })));
send(3, toBinary(AuthRequestSchema, create(AuthRequestSchema, {
  username: "duarte",
  password: "", // dev: se ignora
})));
send(13, toBinary(ListCharactersSchema, create(ListCharactersSchema, {})));
send(15, toBinary(CreateCharacterSchema, create(CreateCharacterSchema, {
  templateId: "warrior",
  name: "Duarte",
})));
send(17, toBinary(SelectCharacterSchema, create(SelectCharacterSchema, {
  characterId: "char-abc",
})));
send(21, toBinary(JoinRoomSchema, create(JoinRoomSchema, {
  mode: 1,     // NextAvailable; mode 2 = ByID con roomId conocido
  roomId: "",
})));
send(5, toBinary(EnterWorldSchema, create(EnterWorldSchema, {})));
send(7, toBinary(MoveInputSchema, create(MoveInputSchema, {
  seq: inputSeq++,
  dir: { x: 1, z: 0 },  // plano XZ; el server normaliza
  speed: 5,             // se clampa a 10
  yaw: 0,               // radianes
})));
// dentro de onmessage, cuando env.flags & 0x01:
send(10, toBinary(AckSchema, create(AckSchema, { seq: env.seq })));
```

Para decodificar, un switch por type id alcanza para el cliente:

```ts
function decodePayload(type: number, payload: Uint8Array): unknown {
  switch (type) {
    case 2:  return fromBinary(ServerInfoSchema, payload);
    case 4:  return fromBinary(AuthResponseSchema, payload);
    case 6:  return fromBinary(WorldSnapshotSchema, payload);
    case 8:  return fromBinary(SnapshotSchema, payload);
    case 9:  return fromBinary(VersionMismatchSchema, payload);
    case 11: return fromBinary(SpawnEntitySchema, payload);
    case 12: return fromBinary(DespawnEntitySchema, payload);
    case 14: return fromBinary(CharacterListSchema, payload);
    case 16: return fromBinary(CreateCharacterResponseSchema, payload);
    case 18: return fromBinary(SelectCharacterResponseSchema, payload);
    case 20: return fromBinary(PongSchema, payload);
    case 22: return fromBinary(JoinRoomResponseSchema, payload);
    default: return undefined; // los C→S no se decodifican en el cliente
  }
}

ws.onmessage = (ev) => {
  if (!(ev.data instanceof ArrayBuffer)) return;
  const env = decodeFrame(new Uint8Array(ev.data));
  if (env.version !== 2) {
    ws.close();
    return;
  }
  // El único frame needs-ack hoy es el WorldSnapshot vacío de EnterWorld.
  if (env.flags & 0x01) {
    send(10, toBinary(AckSchema, create(AckSchema, { seq: env.seq })));
  }
  const msg = decodePayload(env.type, env.payload);
  // dispatch por type: 2/4/6/8/9/11/12/14/16/18/20/22
};
```

---

## 3. Handshake, auth y deadline

### 3.1 Secuencia

```
Cliente                                      Server
  |-- Hello{ protoVer: 2 } ------------------>|
  |<-- ServerInfo{ protoVer: 2, tickRate: 20, serverTime } --|
  |-- AuthRequest{ username, password } ----->|
  |<-- AuthResponse{ ok: true, playerId, udpToken } ----------|  selecting
  |-- ListCharacters ------------------------>|
  |<-- CharacterList{ characters[] } ---------|
  |-- CreateCharacter / SelectCharacter ----->|
  |<-- ...Response{ ok, character, spawnPos } -|
  |-- JoinRoom{ mode, roomId } -------------->|
  |<-- JoinRoomResponse{ roomId, capacity, occupancy } -------|  entering
  |-- EnterWorld ----------------------------->|
  |<-- WorldSnapshot{} (ack, needs-ack) ------|
  |<-- WorldSnapshot{ entities[] } (real) ----|  in-world
```

### 3.2 Reglas duras

- **`Hello` debe ser el primer mensaje.** Cualquier otro mensaje en `connecting` cierra la sesión.
- **Envelope `version = 2` y `Hello.protoVer = 2`.** El server soporta exactamente `[2,2]`. Si cualquiera de los dos está fuera de rango, manda `VersionMismatch{ minVer: 2, maxVer: 2 }` y cierra antes de auth.
- **`ServerInfo.tickRate` es 20.** El server rechaza cualquier `-tick` distinto de 20; no diseñes para otra cadencia.
- **`ServerInfo.serverTime`** es ms desde epoch; útil para time-sync, no obligatorio para el primer playable.

### 3.3 Dev auth: no es identidad

| Hecho | Verificado |
|-------|------------|
| `-dev-auth=true` es el default (también en `docker-compose.yml`) | `cmd/server/main.go`, `docker-compose.yml` |
| Acepta cualquier `username` no vacío | `internal/server/auth.go` |
| `password` se ignora | `internal/server/auth.go` |
| El `username` se usa como **account ID** y como `AuthResponse.playerId` | `internal/server/auth.go` |
| `-dev-auth=false` rechaza **todo** | `internal/server/auth.go` |

- No uses `playerId` como identidad de gameplay ni como entity id. El entity id del mundo es **`character.id`**.
- Con `-dev-auth=false`, `AuthResponse{ ok:false, errorMessage:"server: authentication disabled (real auth pending)" }` y la sesión se cierra. Hoy no hay auth real en producción.
- Username vacío → `AuthResponse{ ok:false, errorMessage:"server: empty username" }` + cierre.

### 3.4 Deadline de 10 s (el detalle que te puede morder)

El server configura `HandshakeTimeout = 10 * time.Second` en `cmd/server/main.go`. El deadline cubre **Hello → EnterWorld inclusive**, es decir, también la fase `selecting` (listar/crear/elegir personaje y unirse a sala).

Particularidades verificadas en `internal/session/handshake.go`:

- No hay un timer de wall-clock que corte solo: el deadline se evalúa **cuando llega el próximo frame**. Si no mandás nada, la conexión puede quedar abierta; si mandás algo después de 10 s y todavía no entraste al mundo, el server cierra.
- Si tu UI de selección/creación puede tardar más de 10 s, **reconectá antes de mandar el siguiente mensaje**; no alcanza con "mandar rápido".
- Implementá tu propio timeout de handshake en el cliente (por ejemplo, 10 s sin `ServerInfo`/respuesta → cerrar y reintentar).

### 3.5 Duplicados: se indexa por personaje, no por `playerId`

El server registra conexiones por **`character.id`**:

- Dos sesiones de la **misma cuenta** con **personajes distintos** pueden coexistir.
- El **mismo personaje** en dos conexiones no: la segunda es rechazada al hacer `EnterWorld` y la conexión se **corta sin mensaje de protocolo** (solo log del server).
- Si reconectás demasiado rápido, el socket viejo puede seguir vivo (no hay heartbeat del server); esperá el cierre y reintentá con backoff.

---

## 4. Personajes (fase `selecting`)

### 4.1 Listar

- `ListCharacters` (13) no lleva campos; el server usa la cuenta de la sesión.
- `CharacterList` (14) devuelve `characters[]`. **Lista vacía no es error**: mostrá la UI de creación.

### 4.2 Crear

`CreateCharacter{ templateId, name }` (15). Reglas del server:

| Regla | Valor |
|-------|-------|
| `name` | `trim` + 3–16 caracteres, solo `[a-zA-Z0-9_]` |
| Unicidad | Por cuenta, case-insensitive |
| `templateId` | Debe existir en el catálogo del server |
| `stats` | Snapshot congelado de `template.baseStats` al crear; no se re-deriva |

Catálogo seed (`data/templates.json`):

| `templateId` | Nombre | `hp` / `speed` / `atk` / `def` |
|--------------|--------|-------------------------------|
| `warrior` | Guerrero | 120 / 3 / 10 / 8 |
| `mage` | Mago | 80 / 4 / 14 / 4 |
| `ranger` | Explorador | 90 / 5 / 9 / 5 |

**No hay mensaje para listar templates.** El cliente conoce los ids o los recibe de su configuración. Si el catálogo cambia, manejá `template not found`.

Rechazos de `CreateCharacter` (no cierran la sesión; podés corregir y reintentar dentro del deadline):

| Condición | `errorMessage` exacto |
|-----------|-----------------------|
| Plantilla inexistente | `template not found` |
| Nombre inválido | `character: invalid name` |
| Nombre duplicado en la cuenta | `name already used in account` |

### 4.3 Seleccionar

`SelectCharacter{ characterId }` (17). El server:

- verifica que el personaje exista y pertenezca a la cuenta;
- resuelve `spawnPos` (`Vec3`, con `y` de terreno) + `stats` + `templateId`;
- devuelve `SelectCharacterResponse{ ok: true, character, spawnPos }` (18);
- si ya tenías sala reservada, pasa a `entering`; si no, sigue en `selecting`.

Rechazos (no cierran):

| Condición | `errorMessage` exacto |
|-----------|-----------------------|
| Personaje inexistente | `character not found` |
| Personaje de otra cuenta | `character not owned by account` |

Reglas de identidad:

- **Entity id del mundo = `character.id`**, no `playerId` ni `accountId`.
- Usá `character.templateId` (o `EntityState.templateId`) para elegir el modelo visual. `visualHint` **no viaja** en el wire.
- `Character.stats` es solo hoja de personaje. En v2, **`stats.speed` no determina la velocidad de movimiento**; la velocidad la manda `MoveInput.speed` y el server la clampa a 10 u/s (ver §7).

---

## 5. Matchmaking: salas públicas

### 5.1 Modos

| Modo | `JoinRoom.mode` | Comportamiento verificado |
|------|-----------------|---------------------------|
| `NextAvailable` | `1` | Ignora `roomId`. Reutiliza una sala con cupo; si ninguna tiene cupo, **crea** una sala pública y reserva atómicamente. |
| `ByID` | `2` | Une a una sala pública **existente** conocida por `roomId`. Si no existe, **no la crea**. |
| Cualquier otro | — | Responde all-zero. |

Detalle de `NextAvailable` (`reserveNext` en `handshake.go`): prioriza `default`, reutiliza una sala pública existente con cupo y, si no hay, crea una nueva. El `roomId` es opaco: **no deduzcas el nombre ni el orden de asignación**.

### 5.2 Capacidad, reserva y respuesta

- Capacidad: **2 jugadores activos por sala**. Sin espectadores.
- `JoinRoomResponse{ roomId, capacity, occupancy }` (22):
  - `capacity` es 2 en una reserva exitosa.
  - `occupancy` incluye **miembros activos + reservas aún no comprometidas**; es informativo, la admisión la decide el server.
- **All-zero = rechazo**: `roomId == ""`, `capacity == 0`, `occupancy == 0` significa que no se reservó nada (sala llena, inexistente o modo inválido). La sesión **sigue en `selecting`**; podés reintentar con `NextAvailable` u otro `roomId`.
- Reserva con **lease de 30 s**; se compromete en `EnterWorld` y se libera al desconectar. Si reservaste y no entraste, el slot se libera al vencer el lease o al cerrar.
- El segundo jugador entra **en vivo**, en el spawn fijo del server, sin ready gate.
- Un **tercer** jugador con `NextAvailable` **no** es rechazado: como no hay cupo en la sala existente, el server le crea una sala nueva. `ByID` sobre una sala llena (o inexistente) sí devuelve all-zero.

### 5.3 Límites del MVP

- Solo salas públicas. **No** hay `Create`, `ByCode`, `roomCode`, salas privadas ni por código; eso requiere auth real y es un SDD aparte.
- `roomId` es un **identificador, no una credencial**. Cualquiera con una cuenta y el ID puede intentar entrar; la admisión depende de cupo, estado y ownership del personaje.
- **No hay descubrimiento de salas**: ningún mensaje lista salas. Para `ByID` necesitás un `roomId` obtenido antes (de tu propio `JoinRoomResponse` o compartido out-of-band).
- **El directorio de salas es process-local y en memoria.**
  - Un `roomId` muere si el proceso reinicia, si la sala queda vacía y se destruye (las salas no-default), o si la sala nunca existió.
  - Con N réplicas, cada una tiene su propio directorio: un `roomId` creado en una **no existe** en otra. Hoy asumí una sola instancia; el mundo compartido multi-réplica es trabajo futuro.
- Mandá `JoinRoom` **una sola vez**. Si ya tenés sala y seguís en `selecting`, un segundo `JoinRoom` devuelve all-zero; si ya pasaste a `entering`, un `JoinRoom` es un mensaje ilegal y **cierra** la conexión.

---

## 6. `EnterWorld` y estado del mundo

### 6.1 Requisitos

`EnterWorld` (5) solo es válido en `entering`, o sea con **personaje seleccionado y sala reservada**. Si falta cualquiera de los dos, el server responde con error de protocolo y **cierra**. No lo mandes "por si acaso".

### 6.2 Los dos `WorldSnapshot`

Al entrar recibís **dos** `WorldSnapshot` (6):

1. **Ack vacío** (`entities.length === 0`), con flag `needs-ack`. No es estado del mundo. Respondé `Ack{ seq }` usando el `seq` del **envelope** de ese frame.
2. **Estado real** (`entities.length > 0`), con tu personaje y todos los jugadores de la sala.

Cuidados verificados:

- **No cuentes "dos WorldSnapshot consecutivos"**: entre el ack y el real puede intercalarse un `Snapshot` (10 Hz) del mismo jugador. Distinguí por contenido, no por posición.
- Podés recibir un `Snapshot` **antes** del `WorldSnapshot` real. No asumas que el primer `WorldSnapshot` es el real.
- El `WorldSnapshot` real **incluye tu propia entidad**; tu personaje no llega por `SpawnEntity`.
- Tu `WorldSnapshot` real y los `SpawnEntity`/`DespawnEntity` van por la **cola reliable** (FIFO). Los `Snapshot` van por la **cola realtime** (depth-1, last-wins). Entre canales no hay orden estricto: un `Snapshot` viejo puede llegar después de un `DespawnEntity` y "resucitar" brevemente una entidad; el siguiente `Snapshot` la excluye. Usá `SpawnEntity`/`DespawnEntity` como lifecycle y los snapshots como estado.

### 6.3 Spawn, despawn e interés

- Al entrar vos: los jugadores que ya estaban reciben `SpawnEntity{ entityId, state }` con tu estado completo.
- Cuando entra otro: vos recibís `SpawnEntity` con su estado completo (incluido `templateId`).
- Cuando alguien sale: los demás reciben `DespawnEntity{ entityId }`, solo el id.
- Interés v2: **flat**, cada jugador ve a todos los de su sala. No hay chunks, ni deltas, ni interés por celda en v2.

### 6.4 `Snapshot` (8): frecuencia y secuencia

- **10 Hz por jugador** (cada 2 ticks de 20 Hz), **staggered** entre jugadores. No asumas exactamente 100 ms; usá `seq`.
- Es **estado completo**: `entities[]` con todos los jugadores de la sala.
- `Snapshot.seq` es **monotónico dentro de la simulación de esa sala**. Descartá cualquier snapshot con `seq <= last-applied`.
- **`seq` es per-sala, no global del proceso.** Al reconectar o cambiar de sala, reseteá el filtro (sin `last-applied`); una sala destruida y recreada puede reiniciar la secuencia.
- No hay `Ack` para `Snapshot`; no lleva `needs-ack`.

### 6.5 Entity state

Cada `EntityState` trae:

```text
id, pos { x, y, z }, velocity { x, z }, yaw (radianes), templateId
```

- `id` es el `character.id`.
- `templateId` elige el modelo visual; si no lo reconocés, usá un fallback y no crashees.
- No hay pitch/roll: la cámara es client-local y no se networkea.

---

## 7. Coordenadas, movimiento y rendering 2D

### 7.1 El contrato

| Eje | Significado |
|-----|-------------|
| `x` | Plano de juego (autoritativo) |
| `z` | Plano de juego (autoritativo) |
| `y` | **Altura de terreno derivada por el server**; nunca la manda el cliente |

- Para un render 2D, usá **X/Z como plano de gameplay**. Por ejemplo, en top-down: `screenX = world.x`, `screenY = world.z`; usá `world.y` como elevación visual si tu arte lo necesita. La proyección exacta es **decisión de tu cliente**, no del contrato.
- **Nunca mandes `y`**: `MoveInput` no tiene campo de altura y `Vec3` de posición solo lo llena el server.
- No necesitás el heightmap en el cliente: la altura ya viene en `pos.y` y `spawnPos.y`.
- La cámara es tuya: no hay pitch/roll ni estado de cámara en el wire.

### 7.2 `MoveInput` (7)

```text
MoveInput { seq, dir { x, z }, speed, yaw }
```

- `dir` es intención en el plano XZ; el server lo **normaliza**.
- `speed` se **clampa a 10 u/s**; negativa → 0. `dir` cero → velocidad cero (aunque `speed` no sea cero).
- `yaw` va en **radianes crudos**; el server no lo normaliza. Normalizá vos para el render y interpolá por el camino más corto (recibir `6.28` y después `0.0` es un giro legítimo, no un salto).
- El server integra `pos.x += velocity.x * dt`, `pos.z += velocity.z * dt` a **20 Hz** (dt fijo de 50 ms) y después resuelve `y = HeightAt(x, z)`.
- Sin colisión ni navmesh en v2: el server no bloquea paredes ni obstáculos.

### 7.3 Frecuencia y predicción

- Tick del server: **20 Hz**. Snapshots: **10 Hz** por jugador.
- **v2 no tiene client prediction.** El snapshot es el estado canónico: aplicá el último y descartá los viejos.
- Si querés interpolar o suavizar, es una **decisión de tu cliente**; no cambia la posición autoritativa ni se manda al server. No inventes correcciones de posición.
- **No hay rate cap de `MoveInput` verificado en el server**: la cola de inputs es por tick y no rechaza por frecuencia. Recomendación de cliente: mandá **como máximo uno por tick (~20/s)** para no engordar la cola. Si mandás varios en un mismo tick, se aplican todos y el último gana para velocidad/yaw; la posición se integra una sola vez.
- `MoveInput.seq` es un tag del cliente; en v2 el server **no deduplica ni descarta por seq**. Usalo igual para tu bookkeeping.
- `stats.speed` **no** es la velocidad de movimiento en v2; la fuente es `MoveInput.speed` clampada a 10.

---

## 8. `Ping` / `Pong` y RTT

### 8.1 Contrato

| Propiedad | Valor |
|-----------|-------|
| Dirección | `Ping` C → S (19), `Pong` S → C (20) |
| Campos | `nonce` (`uint64` → `bigint`), `clientTime` (`int64`, ms epoch → `bigint`) |
| Echo | El server devuelve **idénticos** `nonce` y `clientTime` |
| Tick | Fuera del tick de 20 Hz; no muta la simulación |
| Rate cap | **Máximo 5 `Ping` por ventana de 1 s por sesión**; el exceso se descarta **en silencio** (sin error ni cierre) |
| `needs-ack` | Ni `Ping` ni `Pong` lo llevan |
| Estado | `Ping` se acepta en cualquier estado vivo (también en `selecting`) |

### 8.2 RTT, EMA y jitter (política de cliente)

`rtt = now - clientTime`. Suavizado recomendado por `docs/PROTOCOL.md`: `ema = α·rtt + (1-α)·ema` con `α ≈ 0.2`, y `jitter = EMA(|rtt - ema|)`. Usá el `nonce` para descartar ecos fuera de orden.

```ts
let nonce = 0n;
let ema = 0;
let jitter = 0;

function ping() {
  nonce += 1n;
  const sentAt = Date.now();
  send(19, toBinary(PingSchema, create(PingSchema, {
    nonce,
    clientTime: BigInt(sentAt),
  })));
}

function onPong(p: Pong) {
  if (p.nonce !== nonce) return;              // eco viejo: descartar
  const rtt = Date.now() - Number(p.clientTime);
  ema = 0.2 * rtt + 0.8 * ema;
  jitter = 0.2 * Math.abs(rtt - ema) + 0.8 * jitter;
}
```

- `Number(p.clientTime)` es seguro para ms epoch (muy por debajo de `2^53`).
- Si mandás varios pings en vuelo, guardá los nonces esperados; el snippet asume "uno a la vez / último gana".
- **No hay heartbeat del server** verificado. La detección de enlace muerto es tuya: `docs/PROTOCOL.md` recomienda marcar **stale tras ~10 s sin `Pong` ni `Snapshot`**. Es política de cliente, no una garantía del server.

---

## 9. Errores, cierre y reconexión

### 9.1 Qué ves en el wire

| Condición | Wire | Comportamiento |
|-----------|------|----------------|
| Versión fuera de `[2,2]` | `VersionMismatch{ minVer: 2, maxVer: 2 }` | Se envía y se cierra. |
| Auth fallida | `AuthResponse{ ok:false, errorMessage }` | Se envía y se cierra. |
| Mensaje ilegal para el estado | — (sin mensaje) | Se cierra. |
| Deadline de handshake vencido | — (sin mensaje) | Se cierra al llegar el próximo frame. |
| Frame > 64 KiB | — | Cierra con **1009**. |
| Frame truncado / magic inválido / type desconocido | — | Se cierra. |
| Frame de texto | — | Cierra con **1003**. |
| `JoinRoom` lleno / inexistente / modo inválido | `JoinRoomResponse` all-zero | **No** cierra; sigue en `selecting`. |
| `CreateCharacter` / `SelectCharacter` rechazado | `ok:false` + `errorMessage` | **No** cierra; podés reintentar. |

- v2 **no tiene códigos numéricos de error en el wire**. `errorMessage` es texto.
- El server normalmente cierra con **1000** (normal) incluso en violaciones de protocolo, y con **1001** en shutdown. No te guíes por el close code para diagnosticar: procesá el frame previo (`VersionMismatch`, `AuthResponse`, all-zero) y reiniciá.
- Un `Ack` nunca se contesta con otro `Ack`.

### 9.2 Reconexión

No hay resume de sesión, token de reconexión ni migración de personaje. Reconectar es **una sesión nueva**:

1. cerrá el socket viejo (si sigue abierto);
2. abrí un WebSocket nuevo;
3. `Hello{ protoVer: 2 }`;
4. `AuthRequest` (en dev, el mismo `username` te da la misma cuenta y sus personajes);
5. `ListCharacters` / `SelectCharacter`;
6. `JoinRoom` (`NextAvailable`, o `ByID` si el `roomId` sigue existiendo);
7. `EnterWorld`;
8. reseteá el filtro de `Snapshot.seq`, las entidades locales, los nonces y las métricas de RTT.

Cuidados:

- Un `roomId` cacheado puede haber muerto (sala vacía, reinicio, otra réplica). Si `ByID` devuelve all-zero, caé a `NextAvailable` o mostrá un error.
- Si reconectás con el **mismo personaje** demasiado rápido, el server puede rechazar el `EnterWorld` porque el personaje sigue conectado en el socket viejo (no hay heartbeat). Reintentá con backoff; no asumas liberación instantánea.
- El personaje se libera al cerrar la sesión; la sala también se libera y se destruye si queda vacía (excepto `default`).
- No hagas un loop de reconexión agresivo: el handshake tiene deadline y el server cierra ante frames inválidos. Backoff exponencial.

---

## 10. Operación y seguridad (lo que afecta al cliente)

### 10.1 Origin allowlist: formato exacto

El server exige `Origin` en el upgrade; sin header → **HTTP 403** antes de abrir el WebSocket. El allowlist se pasa por `-wss-origins`:

- Lista **separada por comas**, sin esquema, sin path.
- Se compara contra el **host del Origin, incluido el puerto** (si el browser lo incluye), case-insensitive, con `filepath.Match`.
- Un `*` en cualquier pattern **aborta el boot**. Por eso `*.example.com` también se rechaza: no hay wildcard de subdominios.
- El host del propio request siempre está autorizado (same-host), aunque no esté en la lista.

Ejemplos:

```bash
# Producción: browser en https://game.example.com
-wss-origins game.example.com

# Local: Vite en http://localhost:5173 contra el upstream 127.0.0.1:8080
-wss-origins localhost:5173

# Si también servís la página desde 127.0.0.1:5173
-wss-origins localhost:5173,127.0.0.1:5173
```

Si tu página se sirve desde otro puerto, el `Origin` cambia y tenés que agregarlo. No pongas el esquema (`https://...`) en el pattern: no matchea.

### 10.2 WSS en producción

- El browser usa `wss://<proxy-host>/ws`; el proxy termina TLS y reenvía a `mmo-server:8080` en la red privada.
- El binario Go **no** tiene flags de certificado y habla HTTP plano. **No publiques el `:8080`**.
- El proxy debe: `proxy_http_version 1.1`, reenviar `Upgrade`/`Connection`, y **preservar `Host` y `Origin`**. Reescribir `Origin` a un valor fijo anula la allowlist.
- `proxy_read_timeout` debe ser mayor al gap de tus pings (el ejemplo usa 30 s).
- Base: `deploy/proxy/nginx.conf`.

### 10.3 Dev auth, `udpToken` y logs

- **Dev auth no es producción**: acepta cualquier usuario no vacío y no prueba identidad. No documentes cuentas confiables ni uses `playerId` como autorización.
- **`AuthResponse.udpToken` es legacy y se ignora en WSS.** El server no abre ningún listener UDP; no lo guardes, no lo mandes, no lo muestres.
- **Metadata logs siempre activos** (un evento JSON por frame reliable: sesión, account/room/character, typeID, size, seq).
- **`-log-payloads=true` es opt-in y dev-only**: agrega solo campos decodificados y allowlisted (`joinMode`, `roomId`, `characterId`, `nonce`, `clientTime`). `AuthRequest` no se decodifica para payload, así que las credenciales no llegan al log; password/`udpToken`/secretos se redactan siempre.
- Los `Snapshot` realtime no generan un log por frame: se agregan por segundo.

### 10.4 Límites de despliegue

- Un solo proceso modular sirve `/ws` y hospeda todas las salas **en memoria**.
- Cada réplica es un **mundo independiente**: no hay directorio compartido ni placement. `--scale N` sirve para probar el protocolo, no para un mundo compartido.
- Los personajes se persisten en un archivo JSON (`data/characters.json` por defecto). En Docker, sin volumen, el filesystem del contenedor es efímero: recrear el contenedor puede perder los personajes.
- El estado del mundo (posiciones, salas) **no** se persiste: un reinicio empieza de cero.

---

## 11. Checklist de implementación para Duarte

**Transporte y codec**

- [ ] Abrir el WebSocket con `binaryType = "arraybuffer"`; rechazar frames que no sean `ArrayBuffer`.
- [ ] Implementar el envelope de **11 bytes** big-endian (magic `0x4D4D`, version 2, type, flags, seq).
- [ ] Mandar **un envelope por mensaje WebSocket**; no concatenar, no usar prefijo de longitud.
- [ ] Validar el límite de 64 KiB antes de enviar.
- [ ] Copiar `proto/v1/gen/es/v1/world_pb.ts` y fijar `@bufbuild/protobuf@2.16.0`.
- [ ] Usar `create` / `toBinary` / `fromBinary`; recordar `bigint` para `int64`/`uint64`.

**Lifecycle**

- [ ] Mandar `Hello{ protoVer: 2 }` como primer mensaje, con envelope `version = 2`.
- [ ] Implementar timeout de handshake propio (10 s) y cerrar/reintentar.
- [ ] Tratar dev auth como **no-identidad**; no usar `playerId` como entity id.
- [ ] Listar personajes; lista vacía → UI de creación.
- [ ] Crear con `templateId` + nombre válido; manejar `ok:false` sin cerrar.
- [ ] Seleccionar y guardar `character.id`, `spawnPos`, `stats`, `templateId`.
- [ ] Unirse con `NextAvailable` (recomendado para el primer playable) y manejar `ByID`.
- [ ] Tratar `JoinRoomResponse` all-zero como rechazo y quedarse en `selecting`.
- [ ] Mandar `EnterWorld` solo con personaje **y** sala.
- [ ] Responder `Ack` al `WorldSnapshot` vacío (`needs-ack`).
- [ ] Distinguir el `WorldSnapshot` real por `entities.length > 0`, no por orden.

**Mundo**

- [ ] Renderizar X/Z como plano; usar Y como altura, sin mandarla.
- [ ] Aplicar `Snapshot` solo si `seq > last-applied`; resetear al reconectar.
- [ ] Mantener entidades por `id`; `SpawnEntity` agrega, `DespawnEntity` quita.
- [ ] Elegir el visual por `templateId` con fallback.
- [ ] Mandar `MoveInput` a ~20/s como máximo; `dir` XZ, `speed` ≤ 10, `yaw` en radianes.
- [ ] No implementar predicción autoritativa; si interpolás, que sea solo visual.
- [ ] `Ping` a ≤ 5/s (250 ms de intervalo deja margen); calcular RTT/EMA/jitter con `clientTime` y `nonce`.
- [ ] Marcar stale tras ~10 s sin `Pong` ni `Snapshot` (política propia).

**Errores y reconexión**

- [ ] Manejar `VersionMismatch`, `AuthResponse{ok:false}`, close sin mensaje y close `1009`/`1003`.
- [ ] Reconectar con sesión nueva, reset de estado y backoff.
- [ ] Si `ByID` falla, caer a `NextAvailable` o mostrar error.
- [ ] Probar dos pestañas con **usernames distintos** (dos cuentas) para ver spawn/despawn.

---

## 12. Verificación local

Arrancá el server (comando para tu entorno; no requiere Docker):

```bash
go run ./cmd/server \
  -wss 127.0.0.1:8080 \
  -wss-origins localhost:5173
```

Ajustá `localhost:5173` al origen real de tu dev server (incluí el puerto). Después verificá, en orden:

- [ ] `Hello{2}` → `ServerInfo` con `tickRate = 20` y `serverTime` razonable.
- [ ] `AuthRequest{ username: "duarte", password: "" }` → `AuthResponse{ ok: true }`.
- [ ] `ListCharacters` → `CharacterList` vacía (primera vez), sin cerrar.
- [ ] `CreateCharacter{ templateId: "warrior", name: "Duarte" }` → `ok: true` con `character.id`.
- [ ] `SelectCharacter{ characterId }` → `ok: true` con `spawnPos` y `templateId`.
- [ ] `JoinRoom{ mode: 1 }` → `roomId` no vacío, `capacity = 2`, `occupancy = 1`.
- [ ] `EnterWorld` → `WorldSnapshot` vacío (`needs-ack`) + `WorldSnapshot` real con tu personaje.
- [ ] En el fixture `hills` embebido y spawn por defecto `(0,0)`, `pos.y` debe ser **25**; si cambiás `-spawn-x`/`-spawn-z`, no asumas ese valor.
- [ ] `MoveInput` con `dir { x: 1, z: 0 }`, `speed: 5` → `Snapshot` a ~10 Hz con X creciente.
- [ ] `Ping` → `Pong` con el mismo `nonce`/`clientTime`; el 6.º ping dentro de la misma ventana de 1 s no responde.
- [ ] Segunda pestaña con **otro username**: creá/seleccioná otro personaje, `JoinRoom{ mode: 1 }` (misma sala mientras haya cupo), `EnterWorld`.
  - [ ] La pestaña 1 recibe `SpawnEntity` del jugador 2.
  - [ ] El `WorldSnapshot`/`Snapshot` de la pestaña 2 incluye a ambos.
- [ ] Cerrar la pestaña 2 → la pestaña 1 recibe `DespawnEntity`.
- [ ] Reconectar la pestaña 1 desde cero y verificar que el flujo completo funciona de nuevo.

Notas de diagnóstico:

- En DevTools, los frames WebSocket son binarios: logueá `type`, `size` y `seq`, no el payload crudo con credenciales.
- Un close inmediato sin mensaje suele ser un mensaje ilegal para el estado (por ejemplo, `EnterWorld` antes de tiempo o un segundo `JoinRoom` en `entering`).
- Si el upgrade devuelve **403**, revisá `Origin` y `-wss-origins` (incluido el puerto).
- Si el upgrade devuelve **426**, el proxy no está reenviando `Upgrade`/`Connection`.

---

## 13. Fuentes verificadas

| Archivo | Qué se verificó |
|---------|-----------------|
| `proto/v1/world.proto` | Mensajes, campos, ids 1–22, `Vec3`/`Vec2`, `EntityState`, `Ping`/`Pong`, `JoinRoom`. |
| `docs/PROTOCOL.md` | Contrato de wire, WSS único, lifecycle, catálogo, movimiento, errores, versionado, binding TS. |
| `docs/DEPLOYMENT.md` | WSS detrás del proxy, matchmaking público, dev auth, `-log-payloads`, single-instance. |
| `internal/protocol/envelope.go` | Header de **11 bytes**, magic, flags, límite 64 KiB. |
| `internal/protocol/registry.go` | Type ids 1–22 y modos de `JoinRoom`. |
| `internal/session/handshake.go` | Estados, orden, `Hello`/auth/character/room, deadline, `JoinRoom` all-zero, `Select`/`Join` en cualquier orden. |
| `internal/session/ping.go` | `Ping`/`Pong`, 5/s, drop silencioso. |
| `internal/server/ws.go` | `/ws`, `Origin` obligatorio, allowlist, read limit 64 KiB, WSS por proxy. |
| `internal/server/wstransport.go` | Un mensaje binario por envelope; texto → `1003`. |
| `internal/server/auth.go` | Dev auth: usuario no vacío, password ignorado, `username` = account id. |
| `internal/server/server.go` | Registro por `character.id`, duplicado, `enterWorld`, limpieza. |
| `internal/server/runner.go` | Una `Simulation` por sala; destrucción de la sala no-default cuando queda vacía. |
| `internal/server/sim.go` | `WorldSnapshot` real, `SpawnEntity`/`DespawnEntity`, fanout por sala. |
| `internal/server/sink.go` | `Snapshot` por la cola realtime (depth-1) y agregado de telemetría. |
| `internal/server/telemetry.go` | Payload logging opt-in y allowlist. |
| `internal/observability/logger.go` | Redacción de credenciales/secretos. |
| `internal/game/movement.go` | Normalización de `dir`, clamp a 10 u/s, integración XZ, `Y` derivada. |
| `internal/game/snapshot.go` | 10 Hz, staggered, `SeqFilter`, `EntityState` con `templateId`. |
| `internal/game/tick.go` | Tick 20 Hz, `QueueInput`, `Stats.speed` no usado para movimiento. |
| `internal/rooms/manager.go` | `Capacity = 2`, `DefaultRoom = "default"`, `ReserveKnown` no crea. |
| `internal/rooms/lease.go` | Lease de 30 s. |
| `internal/network/sink.go` | Reliable FIFO vs realtime depth-1. |
| `proto/v1/gen/es/v1/world_pb.ts` | Schemas y tipos TS; `int64` → `bigint`. |
| `package.json`, `buf.gen.es.yaml`, `Makefile` | protobuf-es 2.16.0, ruta de codegen, `make proto-es`. |

---

## 14. Fuera de alcance / lo que no está garantizado

- Salas privadas, por código, `Create`, `ByCode`: **no existen** en este MVP; requieren auth real.
- Espectadores: **no** hay. Solo jugadores con personaje y cupo.
- Auth real: **no** hay. `-dev-auth` no prueba identidad.
- Mundo compartido multi-réplica: **no** hay. Cada proceso es un mundo independiente y el directorio de salas es local.
- Descubrimiento de salas: **no** hay mensaje. `ByID` requiere un id conocido.
- Client prediction/interpolación: **no** son parte del contrato; si las agregás, son decisión y riesgo de tu cliente.
- Heartbeat del server: **no** verificado; la detección de staleness es del cliente.
- Combate, HP, ítems, chat: **no** hay mensajes en v2.
