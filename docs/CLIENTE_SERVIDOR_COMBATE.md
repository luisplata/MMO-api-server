# Cliente ↔ Servidor: combate, mapa y contenido

> **Para**: Duarte (cliente Phaser/TypeScript, repo `PokemonPvpWeb`) y mantenedores de `mmo-api-server`.
> **Qué es**: handoff vivo entre los dos repos. No es una spec cerrada ni un pedido unilateral: es el lugar donde cliente y server acuerdan qué falta, quién lo entrega y qué se acepta como terminado.
> **Cómo se usa**: cada iteración agrega una fila en la §5 y actualiza su estado. Ningún equipo manda detalles técnicos por chat esperando que el otro los reconstruya.
> **Fuente de verdad del wire**: `proto/v1/world.proto` + `docs/PROTOCOL.md` + `docs/PHASER_CLIENT_GUIDE.md`. Si este documento y el código discrepan, gana el código.
> **Verificado contra**: HEAD `d2f64bb` (2026-11-07) de `mmo-api-server`, y el pedido del cliente en `D:\Web\PokemonPvpWeb\docs\SERVIDOR_PENDIENTES.md`.
> **Rutas**: son locales a este repo (`D:\Web\MMO-api-server`) salvo que se aclare el repo cliente. No son links relativos entre repos: cada equipo las resuelve en su checkout.

---

## 1. Estado ejecutivo

Movimiento y salas ya pasaron un test real con el cliente Phaser: 1v1, movimiento a 20 Hz, snapshots a 10 Hz, salas públicas de 2 jugadores y personajes. Ese baseline **no se toca**. Lo que falta es todo lo que convierte el lobby en partida: combate, habilidades, estados, colisión, ciclo de partida y la identidad de mapa en el wire.

### 1.1 Lo que ya está en `main`

| Pieza | Valor vigente | Fuente |
|---|---|---|
| Transporte | Una sola conexión WSS, 1 mensaje = 1 envelope de **11 bytes** BE | `docs/PROTOCOL.md` §2–§3 |
| Envelope | magic `0x4D4D`, version `2`, type u16, flags u8, seq u32 | `internal/protocol/envelope.go` |
| Versión | Rango negociado `[2,2]`; mismatch → `VersionMismatch` + cierre | `proto/v1/world.proto` |
| Tick | 20 Hz fijo (50 ms), integración `pos += velocity × dt` | `internal/game/tick.go` |
| Snapshots | 10 Hz por jugador, staggered, estado completo, `seq` por sala | `internal/game/snapshot.go`, `internal/server/runner.go` |
| Movimiento | `MoveInput` normaliza `dir`, clampa `speed` a **10 u/s**, `yaw` crudo en radianes | `internal/game/movement.go` |
| Stop por input obsoleto | 10 ticks (500 ms) sin input → velocidad autoritativa a 0 antes de integrar | `internal/game/movement.go` |
| Salas | Públicas, capacidad 2, `NextAvailable`/`ByID`, lease 30 s | `internal/rooms/manager.go`, `docs/PROTOCOL.md` §5 |
| Personajes | Dev auth, listar/crear/seleccionar, `templateId` + stats congeladas al crear | `internal/session/handshake.go` |
| Latencia | `Ping`/`Pong` fuera del tick, máx. 5/s, exceso descartado | `internal/session/ping.go` |
| Terreno | Altura derivada por el server en `pos.y`; heightmap embebido 32×32 | `docs/HEIGHTMAP.md` §7 |
| Interés | Flat: cada jugador ve a todos los de su sala | `internal/world/interest.go` (`FlatResolver`) |

### 1.2 Lo que no existe todavía

| Ausente | Consecuencia hoy |
|---|---|
| Combate: daño, HP, muerte, escudo | No hay partida, solo un lobby con gente caminando |
| Habilidades y primitivas (21) | No hay acciones ni kits ejecutables |
| Clases reales (8) y kits (24) | El server solo tiene 3 templates (`warrior`/`mage`/`ranger`) |
| Estados (`stun`, `root`, `slow`, `shield`, `burn`, `poison`, …) | 18 de las 24 habilidades de kit quedarían en daño plano |
| Colisión, pilares, límites de arena, agua | El online atraviesa todo; el cliente online **oculta** los pilares |
| Ciclo de partida (`Start`/`End`, rendición, revancha, timeout) | La partida empieza por `SpawnEntity` y nunca termina del lado del server |
| Identidad de mapa en el wire | No hay `mapId` ni `contentVersion` en `ServerInfo` ni en snapshots |
| Spawn por jugador | Todos los personajes van al mismo punto configurado (`-spawn-x/z`) |
| Proyectiles/zonas/traps en el snapshot | El cliente no puede dibujar lo que el server resuelve |
| Rate cap de `MoveInput` y dedupe por `seq` | La cola se engorda; v2 no deduplica |
| Resume de sesión | Reconectar es sesión nueva; se pierde la partida |

**Nota de autoridad**: el reporte de que movimiento y salas "están cerrados" es del cliente y es consistente con el contrato del server. No hay un test E2E automatizado que lo certifique desde este repo, así que se trata como gate cumplido por evidencia de integración real, no por CI.

### 1.3 El desajuste que bloquea el combate ahora

| Dimensión | Server (`mmo-api-server`) | Cliente (`PokemonPvpWeb`) |
|---|---|---|
| Identidad | 3 `templateId`: `warrior`, `mage`, `ranger` | 8 clases: `terravox`, `umbraclaw`, `psyflame`, `floraviva`, `campeon`, `mortero`, `franco`, `disparador` |
| HP | 120 / 80 / 90 (congelado al crear) | 850–1500 por clase |
| Kit | No existe | 1 básico + 3 habilidades por clase (`KITS`, 24 en total) |
| Stats | `hp`, `speed`, `atk`, `def` | `hp`, `res`/`resRegen`, `speed` en px/s, `radius`; **sin `atk`/`def` en la fórmula** |

Hoy el cliente colapsa sus 8 clases en esos 3 templates para poder entrar. Con combate eso deja de ser cosmético: el HP, la velocidad y el kit dependen de la clase real. **Mientras este desajuste no se resuelva, cualquier daño que implemente el server será el de otra criatura.**

---

## 2. Próximo handoff — lo que necesitamos de Duarte ahora

### 2.1 Contrato de arena/mapa versionado

Necesitamos **un** mapa canónico, no tres aproximaciones. El paquete tiene que traer:

- [ ] **Dimensiones y grilla**: `width × height`, `cell_size`, `origin_x`, `origin_z`, `y_scale`.
- [ ] **Transformación de coordenadas**: `u = px / 20`; el eje vertical del render es `z_server` (`y_cliente ≡ z_server`); origen en la esquina mínima; orientación de grilla: fila 0 = Z mínima, columna 0 = X mínima.
- [ ] **Heightfield**: par `.heightmap` + `.manifest` con `sha256`, o un manifest equivalente si el cliente no puede exportar el binario.
- [ ] **Bounds jugables** y clamp `[radius, W − radius]` en ambos ejes.
- [ ] **Región de agua**: `z0`, `z1` y multiplicador de velocidad (el cliente reporta `z ∈ [38,47] u`, slow `0.7`).
- [ ] **Pilares**: cantidad, `x`/`z`/`r`, y quién los genera (seed).
- [ ] **Spawn points** por jugador/clase (el cliente espera `(65, 68.5)` y `(65, 16.5) u`).
- [ ] **Geometría visual vs colisionable**: qué dibuja el cliente y qué resuelve el server. Sin esta lista, el server no puede saber qué bloquea.
- [ ] **Revisión del asset package**: commit/hash del repo cliente del que salió el paquete.

**El desajuste observado, explícito:**

| Fuente | Grilla | `cell_size` | Extensión en unidades |
|---|---|---|---|
| Server, default (fixture `hills` embebida) | 32×32 | 10 | `[0,310] × [0,310]` ≈ **310×310 u** |
| Server, `internal/world/testdata/combate_map.*` (opcional con `-map`) | 257×257 | 0.78125 | `[−100,100]²` = **200×200 u** |
| Cliente, arena de Carnaval Solar | — | 20 px/u | 2600×1700 px = **130×85 u** |

**Ninguno coincide.** El mapa por default es cuadrado y ~2.4× más grande que la arena real; `combate_map` existe en el repo pero no es el default, no viaja en el wire y no tiene procedencia documentada. **No asumir que la altura, los bordes ni los spawns están alineados.** El primer paso es elegir UNO y firmarlo; recién después se implementa runtime.

### 2.2 Export de contenido machine-readable

Una tabla transcripta a mano no alcanza: el primer patch de balance deja dos verdades y el síntoma es daño distinto en cada pantalla, sin error en ningún log. Necesitamos:

- [ ] **JSON** (o formato acordado) generado desde `src/data/classes.js`, `src/data/abilities.js` y `src/data/types.js`, con script reproducible.
- [ ] **Revisión/hash** del repo cliente + fecha de export.
- [ ] **8 clases** con stats exactos: `hp`, `res`, `resRegen`, `speed` (px/s o u/s, aclarado), `radius`, `preferredRange`, básico completo (`cd`, `dmg`, `range`, `arc`/`projSpeed`).
- [ ] **Catálogo completo de 40 habilidades** con efectos en orden y parámetros numéricos; `pp` ausente = infinito.
- [ ] **24 de kit** (3 por clase) + 16 de pool, con el mapping clase → kit.
- [ ] **Tipos**: `BEATS` completo y multiplicadores `1.4` / `0.72` / `1.0`.
- [ ] **Mundo/agua/pilares/spawns**, o referencia explícita al paquete de mapa de §2.1.
- [ ] **Validación de boot**: ids existen, 3 habilidades por kit, todas las primitivas del JSON están implementadas. Si falla, el server no arranca.

### 2.3 Preguntas de diseño bloqueantes

Cada recomendación está marcada **propuesto, no aprobado**: el server propone, Duarte confirma o corrige en la §5.

| # | Pregunta | Recomendación del server | Estado |
|---|---|---|---|
| 1 | ¿Aim como punto (`aim{x,z}`) o `targetId`? | Punto + `aimMode`; el server clampea el punto al `range` alrededor del caster. Es lo que el cliente ya produce y no requiere que ambos elijan el mismo target. | Propuesto, no aprobado |
| 2 | ¿24 habilidades de kit o las 40? | Datos de las 40, aceptación sobre las 24 de kit. Las 40 salen casi gratis una vez implementadas las primitivas. | Propuesto, no aprobado |
| 3 | ¿`classId` o `templateId`? | `classId` como campo aditivo nuevo (número a definir en el `.proto`). `templateId` queda como hint visual; no alcanza para HP/kit. | Propuesto, no aprobado |
| 4 | ¿`contentVersion`? | Hash corto (8 hex) en `ServerInfo` y en el arranque de partida; mismatch bloquea la partida, no la conexión. Campo aditivo nuevo. | Propuesto, no aprobado |
| 5 | ¿Quién autoriza pilares/arena? | El paquete de mapa es la única fuente; el server publica el layout (seed + lista) y el cliente dibuja exactamente eso. Nunca dos layouts. | Propuesto, no aprobado |
| 6 | ¿`moveMag` o `speed` absoluto? | `moveMag 0..1`; el server calcula `speed_clase × moveMag × multiplicadores`. `speed` absoluto es el agujero por el que un cliente manda 30 u/s. Necesario además porque `umbraclaw` corre 10.25 u/s y con `veil` llega a 14.86 u/s, por encima del clamp actual de 10. | Propuesto, no aprobado |
| 7 | ¿Tick de 20 Hz o más? | Mantener 20 Hz en el wire; sub-steppear internamente (2–4 pasos) y consumir el remanente de duraciones. No cambia el contrato ni la frecuencia de input. | Propuesto, no aprobado |
| 8 | Ciclo de partida | Mensajes explícitos de inicio/fin (ids nuevos a asignar), rendición como input del jugador, revancha solo si la piden los dos. Sin resume, la desconexión debe resolver en forfeit inmediato; una gracia de 10 s solo tiene sentido si existe resume. Timeout: decisión del server, con duración explícita. | Propuesto, no aprobado |
| 9 | PP, invuln y primitivas cosméticas | `pp = -1` para infinito (`0` = sin usos). `invuln` reservado y sin contenido que lo otorgue. `ring`/`floater` son cosméticos: no se simulan en el server; si hacen falta eventos, se agregan como evento, no como estado. | Propuesto, no aprobado |
| 10 | `Stats.atk`/`def` en daño | **No participan.** El daño es `cantidad × multiplicador de tipo` (+ `empowered`); HP es float. Si el server agrega defensa, las dos pantallas muestran números distintos. | Propuesto, no aprobado |

### 2.4 Autoridad (no negociable)

El cliente **puede proponer** comportamiento, contenido y forma de los mensajes. El server **es dueño del contrato autoritativo final** de:

- daño, curación y absorción de escudo;
- cooldowns, energía (`res`) y PP;
- colisión y posición;
- aplicación/expiración de estados;
- validez de un cast y resultado de la partida.

Un rechazo de cast inválido **no cierra la sesión** (a diferencia de una violación de framing o de estado). El cliente puede previsualizar; el estado final es el del snapshot.

---

## 3. Roadmap por hitos

El baseline de movimiento ya está hecho. El orden importa: mapa antes de habilidades, estado core antes de combate, una rebanada vertical antes de las 21 primitivas.

| Hito | Entregable | Server | Cliente | Depende de | Gate de aceptación |
|---|---|---|---|---|---|
| **M0 — Baseline** ✅ | Movimiento + salas + personajes | Hecho | Hecho | — | **Test real online: pasado** (movimiento 1v1, salas, snapshots) |
| **M1 — Contrato de mapa** | Documento + JSON/manifest firmado de §2.1 | Revisa y valida el manifest; define formato de identidad de mapa | Exporta el paquete y la transformación de coordenadas | §2.1 | Ambos confirman dims, `cell_size`, origen, hash y lista de geometría colisionable. Sin runtime. |
| **M2 — Runtime de mapa** | Terreno, bounds, agua, pilares, spawns por jugador | Samplea heightmap, clampea, resuelve pilares y spawns; publica identidad/layout | Dibuja el mismo layout y aplica el mismo clamp visual | M1 | **Gate de staging online**: dos clientes ven el mismo mapa; un jugador no atraviesa un pilar; el agua frena; cada uno spawnea en su punto. |
| **M3 — Ciclo de partida + estado core** | Inicio/fin explícitos; `hp`/`maxHp`/`alive` por entidad; identidad de clase (`classId`, sujeto a §2.3 #3) | Emite inicio y fin, aplica muerte, resuelve ganador/revancha/rendición | Congela input al terminar, muestra resultado, pide revancha | M2 | KO y rendición terminan la partida para ambos con el mismo ganador; la revancha reinicia HP/estado sin volver a la cola. |
| **M4 — Primera rebanada vertical** | 1 clase, básico + 1 habilidad con sus primitivas mínimas | Valida cast, aplica daño, cooldown y un estado; emite eventos | Dibuja cast, hit y HP real | M3 | **Gate de staging online con Duarte**: el daño y el HP coinciden en las dos pantallas; mantener apretado no recastea. |
| **M5 — Swept collision** | Test de segmento para proyectiles y dashes | Resuelve `pos → pos + vel·dt` contra círculos; impacto por el más cercano al origen | Sin cambio de contrato; ajusta FX al evento | M4 | Un proyectil rápido contra un objetivo en movimiento produce **un** hit, no cero ni dos. Sin impactos fantasma. |
| **M6 — Kits completos** | 21 primitivas, 24 kits, estados completos | Implementa primitivas como datos + runners; resuelve orden de efectos | Reproduce FX por evento; no calcula daño | M5 | Escenarios de aceptación del cliente (E1–E14; E15 cae en M8) verdes en server; muestreo en cliente. |
| **M7 — Catálogo completo** | 40 habilidades, PP infinito, `invuln` reservado, cosméticos | Valida boot del contenido; expone `projectiles[]`, `zones[]`, `traps[]` | Dibuja las tres listas e interpola por `id` estable | M6 | Contenido completo carga sin error; una partida con zonas y traps se ve igual en ambos clientes. |
| **M8 — Soak final** | Versionado, rollback y estabilidad | `contentVersion` bloquea mismatch; métricas de tick y snapshots | Muestra "contenido desactualizado" y no arranca | M7 | 30+ min de soak con 2 jugadores sin drift ni cierre inesperado; rollback de mapa/proto documentado. |

**Regla de secuencia**: no se agenda un único "primer test online" al final. Ya hubo uno en M0; hay gates online en M2 (alineación de mapa) y M4 (primera habilidad), y el soak de M8 es el cierre, no el primer contacto.

---

## 4. Baseline de protocolo para el cliente

**Lo que Duarte puede esperar hoy del server**: el contrato de abajo, estable y verificado en `main`; ninguna promesa de combate hasta que exista un mensaje en el `.proto`. Referencias canónicas (no duplicar acá): `docs/PROTOCOL.md` y `docs/PHASER_CLIENT_GUIDE.md`. Esta sección resume lo usable y marca lo que **no** existe.

### 4.1 Transporte y envelope

| Propiedad | Valor |
|---|---|
| Endpoint | `wss://<proxy-host>/ws` (TLS en el proxy; `:8080` Go nunca se publica) |
| Framing | 1 mensaje WebSocket binario = 1 envelope completo |
| Envelope | **11 bytes** BE: magic `0x4D4D`, version u16 (`2`), type u16, flags u8, seq u32 |
| Límite | 64 KiB por frame (header + payload) |
| Versión | `[2,2]`; fuera de rango → `VersionMismatch` + cierre |
| Catálogo | Ids **1–22** exhaustivos hoy |

> El comentario de `proto/v1/world.proto` dice "10-byte envelope": es un miscount. El layout real y `internal/protocol/envelope.go` son **11 bytes**.

### 4.2 Movimiento (`MoveInput`, id 7)

- `dir{x,z}` es intención; el server lo **normaliza**.
- `speed` se **clampa a 10 u/s**; negativa → 0; `dir` cero → velocidad cero.
- `yaw` va en radianes crudos; el server no lo normaliza.
- Se manda **como máximo uno por tick (~20/s)**. Al soltar, neutro inmediato (`dir 0`, `speed 0`).
- Watchdog: 10 ticks (500 ms) sin input procesado → velocidad autoritativa a 0 antes de integrar.
- `MoveInput.seq` es tag del cliente; en v2 el server **no deduplica por `seq`**.
- Sin predicción: el snapshot es la verdad. Interpolar es decisión visual del cliente.

### 4.3 Salas y personajes

- Salas públicas, capacidad **2**, sin espectadores; `NextAvailable` (1) reutiliza o crea, `ByID` (2) une a una conocida y **no** la crea.
- `JoinRoomResponse` all-zero = no se reservó nada; la sesión sigue en `selecting`.
- Lease de reserva 30 s; se compromete en `EnterWorld` y se libera al desconectar.
- Entity id del mundo = `character.id`, no `playerId` ni `accountId`.
- Dev auth: usuario no vacío, password ignorada. No es identidad real.

### 4.4 Lo que todavía no está

- **No hay mensajes de combate, habilidades ni estados.** No hay ids nuevos que usar: el catálogo termina en 22.
- **No hay identidad de mapa ni `contentVersion`** en `ServerInfo`. El mapa activo solo aparece en el log de boot.
- **No hay `MatchStart`/`MatchEnd`/rendición/revancha/timeout.** La partida empieza por `SpawnEntity` y no termina del lado del server.
- **No hay colisión, pilares ni agua** en la simulación.
- **No hay spawn por jugador**: todos van al punto configurado.
- **No hay resume de sesión.**

Cuando estos mensajes existan, sus ids y campos se agregan al `.proto` y a este documento **antes** de que el cliente los use. Hasta entonces, cualquier campo inventado del lado cliente es deuda, no contrato.

---

## 5. Tabla de decisión / iteración

**Este es el registro compartido.** Cada ciclo agrega una fila; no se reescribe la historia. Estados posibles: `confirmado`, `propuesto`, `abierto`. El owner de la fila mueve el estado cuando entrega el artefacto.

| Iteración | Owner | Artefacto de entrada / salida | Estado | Decisión / aceptación |
|---|---|---|---|---|
| **I0** — 2026-11-07 | Duarte | Entrada: `SERVIDOR_PENDIENTES.md`. Salida: reporte de test real de movimiento/salas | `confirmado` | Movimiento v2 y salas aceptados como baseline. No se rediseñan. |
| **I1** — 2026-11-07 | Server | Salida: este documento completo, recomendaciones de §2.3 | `propuesto` | El server propone contrato de mapa, export JSON y orden de hitos. Espera confirmación de Duarte. |
| **I1 — plantilla Duarte** | Duarte | Entrada: §2.1 + §2.2 + §2.3. Salida: paquete de mapa, JSON de contenido, respuestas a las 10 preguntas | `abierto` | Completar esta fila con hash/revisión y cambiar estado a `confirmado` o `propuesto` con correcciones. |
| **I2 — plantilla Server** | Server | Entrada: respuesta de Duarte. Salida: `.proto`/design del primer slice, contrato de mapa en `docs/`, campos nuevos con número asignado | `abierto` | El server responde pregunta por pregunta y publica el contrato antes de implementar. |
| **I3 — plantilla ambos** | Duarte + Server | Entrada: build de staging. Salida: resultado del gate de M2 (alineación de mapa) | `abierto` | Un jugador no atraviesa pilares, el agua frena y ambos ven el mismo layout. |
| **I1-server (respuesta)** — 2026-11-07 | Server | Entrada: `COMBATE_CLIENTE_A_SERVIDOR.md` I1. Salida: §7 (respuesta a 10+12, pedido de mapa/contenido/proto v3) | `propuesto` | Duarte responde §7.3–§7.5 en su I2 con `map.json`, `content.json`, hash y confirmación de v3. |

**Cómo editar sin romper el handoff**: agregá una fila al final, actualizá el estado de la anterior si cambió, y dejá el artefacto concreto (commit, hash, ruta). No borres filas viejas: son la evidencia de por qué el contrato dice lo que dice.

---

## 6. Fuentes verificadas y alcance

| Archivo | Qué se verificó |
|---|---|
| `proto/v1/world.proto` | Envelope v2, ids 1–22, `MoveInput`, `Snapshot`, `EntityState`, `ServerInfo` sin mapa ni `contentVersion` |
| `docs/PROTOCOL.md` | WSS único, envelope 11 bytes, 20 Hz tick, 10 Hz snapshots, clamp 10 u/s, watchdog 500 ms, salas |
| `docs/PHASER_CLIENT_GUIDE.md` | Flujo completo del cliente Phaser, rooms, `MoveInput`, límites y fuera de alcance (combate, HP, chat) |
| `docs/HEIGHTMAP.md` | Formato `.heightmap` + `.manifest`, fixture `hills` 32×32 cell 10 → `[0,310]` |
| `internal/game/movement.go` | Normalización, clamp, integración XZ, stop a los 10 ticks |
| `internal/game/tick.go` | Tick 20 Hz, cola de input por tick, Y derivada post-integración |
| `internal/game/snapshot.go` | 10 Hz staggered, `seq` por simulación de sala, `EntityState` sin HP/clase |
| `internal/world/heightmap.go`, `terrain.go` | Validación fail-fast, sampling bilineal, bounds implícitos |
| `internal/server/spawn.go` | Un único spawn configurable para todos los personajes |
| `internal/server/sim.go`, `runner.go` | Una simulación por sala, fanout flat, snapshot de mundo real |
| `internal/rooms/manager.go` | Capacidad 2, lease 30 s |
| `internal/session/ping.go` | `Ping`/`Pong` fuera del tick, 5/s |
| `data/templates.json` | 3 templates con HP 120/80/90, `atk`/`def` presentes |
| `internal/world/testdata/combate_map.manifest` | 257×257, cell 0.78125, `[−100,100]²`, no default |
| `D:\Web\PokemonPvpWeb\docs\SERVIDOR_PENDIENTES.md` | Pedido del cliente: 8 clases, 24 kits, 21 primitivas, reglas de combate |
| `D:\Web\PokemonPvpWeb\src\data\types.js` | `WORLD { w: 2600, h: 1700 }` |
| `D:\Web\PokemonPvpWeb\src\net\config.js` | `PX_PER_UNIT = 20` |
| `D:\Web\PokemonPvpWeb\src\data\classes.js` | 8 clases, `KITS` de 3, stats y básicos |
| `D:\Web\PokemonPvpWeb\src\data\abilities.js` | 40 habilidades en catálogo |

**Este documento no implementa nada.** No crea mensajes, no asigna ids nuevos, no modifica `SERVIDOR_PENDIENTES.md` y no promete comportamiento que no esté en `main`.

---

## 7. I1 — Respuesta del servidor / pedido para I2 (2026-11-07)

> **Verificado contra**: HEAD `89df73f` (2026-11-07) de `mmo-api-server` y la I1 del cliente (`D:\Web\PokemonPvpWeb\docs\COMBATE_CLIENTE_A_SERVIDOR.md`, HEAD `3c986f7`).
> **Alineación de numeración**: cliente I1 (respuestas) → server I1 (esta sección) → cliente I2 (paquete de mapa + contenido). La §5 conserva sus filas y suma la respuesta del server.
> **Etiquetas**: `confirmado` = ya en `main` o decisión cerrada por ambas partes; `propuesto` = **server proposal — awaiting Duarte confirmation**; `abierto` = falta un artefacto o un número.

### 7.1 Aceptado (confirmado)

| Punto | Acuerdo | Nota verificada |
|---|---|---|
| **D1** aim | Punto `aim{x,z}` + `aimMode`; el server clampea al `range` del caster y a bounds. | Semántica exacta del clamp (radial vs. por eje, orden range/bounds) se fija con el primer `AbilityCast`. No hay código de combate todavía. |
| **D2** export | Datos de las 40; gates de aceptación sobre las 24 de kit. | El export no se recorta. |
| **D5** arena | Una sola arena canónica, layout fijo, sin radios aleatorios por cliente/partida. | Los pilares siguen sin existir en la simulación; es contrato de M2. |
| **D7** tick | 20 Hz en el wire; el server puede sub-steppear internamente y consumir remanentes. | `TickRate = 20`, `TickInterval = 50 ms`; los sub-steps no cambian el contrato ni la frecuencia de input. |
| **D9** PP/invuln/cosméticos | `pp` ausente → infinito (`-1` en el wire), `0` = sin usos; `invuln` reservado; `ring`/`floater` solo cliente. | **`face` no es cosmético puro**: fija el `facing` autoritativo del caster (`Entity.Yaw`), y el server lo usará al resolver el cast. `ring`/`floater` no se simulan. |
| **D10** atk/def | No participan del daño: `cantidad × typeMult` (+ `empowered`). | `Stats.atk/def` existen en el wire y en `data/templates.json`, pero combate no los leerá. Cambiar la fórmula requiere aprobación de ambos equipos. |
| **P23** stop | Al soltar, el cliente manda `MoveInput` neutro; mientras mantiene, repite intención. Watchdog del server: 10 ticks / 500 ms → velocidad 0. | Verificado en `internal/game/movement.go` (`DefaultMaxSpeed = 10`, `InputStopAfterTicks = 10`). v2 no deduplica por `seq`. |

### 7.2 Respuesta a las 12 preguntas del cliente (§5 de su documento)

| # | Respuesta del server | Estado |
|---|---|---|
| 1 | **No hay ids nuevos.** El catálogo sigue siendo 1–22 exhaustivo. Propuesta: `AbilityCast`, `Hit`/`Damage`, `CastRejected`, `CastInterrupted`, `Death`, `MatchStart`, `MatchEnd` y rendición/revancha como mensajes discretos en canal confiable. El server publica ids y campos **después** de cerrar §7.3 #5. Inventar ids del lado cliente sigue siendo deuda, no contrato. | `propuesto` |
| 2 | **Sí, con campos acotados**: `hp`, `maxHp`, `alive`, `classId`, `res`, PP y cooldowns por slot, estados con `remaining`/`stacks`, `shield`, `invis`, `aiming`. Se publican en el snapshot autoritativo; el cliente no calcula ninguno. | `propuesto` |
| 3 | **Sí**: `projectiles[]`, `zones[]`, `traps[]` con `id` estable, `ownerId`, primitiva, posición, radio/ángulo, TTL y estado. Listas acotadas y ordenadas por `id` para interpolar sin adivinar. | `propuesto` |
| 4 | Hoy el snapshot es **estado completo**, flat y a 10 Hz. Estimación: un `EntityState` ronda 80–100 bytes en el wire → ~650 entidades caben en 64 KiB, pero el server **pide máximos simultáneos** antes de fijar caps. Primer slice: seguir full-state; delta recién con datos de soak. | `propuesto` + `abierto` (caps) |
| 5 | v2 no tiene rate cap ni dedupe. Propuesta v3: máximo 1 `MoveInput` procesado por tick, gana el último; se descarta `seq <= último procesado`; el exceso sobre 20/s se drena sin aplicar. | `propuesto` |
| 6 | **v3 con `moveMag` autoritativo** (ver §7.3 #3). No hay cambio silencioso de semántica en v2: sube el major y un cliente viejo no conecta. | `propuesto` |
| 7 | Rechazo de cast = **evento explícito** (`CastRejected` con motivo y slot), nunca cierre de sesión. La regla de §2.4 ya está confirmada; solo falta el mensaje. | `propuesto` (regla `confirmado`) |
| 8 | Sin timeout en el primer slice salvo que Duarte entregue duración y criterio de ganador. | `propuesto` |
| 9 | Revancha en la **misma sala**, la piden los dos, reinicia HP/estado/cooldowns/PP, no cambia de mapa y no vuelve a la cola. | `propuesto` |
| 10 | `contentVersion` viaja en `ServerInfo` y `MatchStart`; mismatch **bloquea la partida, no la conexión**; UX "contenido desactualizado". Requiere campos aditivos nuevos. | `propuesto` |
| 11 | **24 kits implementados y aceptados; 40 exportadas.** Las 16 de pool quedan no jugables hasta priorización explícita. | `confirmado` (alcance) |
| 12 | **`carnaval-solar-v1` es plano**, `height = 0`. El loader actual lo acepta con una grilla de ceros (ver §7.3 #1). | `confirmado` |

### 7.3 Puntos que necesitan respuesta de Duarte

**1. Mapa — el desajuste sigue abierto.** El server confirma que `carnaval-solar-v1` 130×85 u, Y=0, agua `z ∈ [38,47]` con `speedScale 0.7` es el objetivo. Hoy el default es `hills` 32×32 cell 10 → 310×310 u, y `combate_map` 257×257 cell 0.78125 → 200×200 u es opcional con `-map`; ninguno viaja en el wire. **server proposal — awaiting Duarte confirmation**: entregar el paquete completo con los 18 pilares y **radio fijo por pilar**, los dos spawns, bounds jugables, `origin`/orientación de grilla, `mapId` y hash. El `sha256` del manifest ya existe y sirve como hash de mapa; falta publicarlo. Sin `mapId` en el wire, dos clientes pueden dibujar arenas distintas sin error.

> Detalle del loader (verificado): la cobertura es `(Width−1) × CellSize`. Para 130×85 u con `cell_size = 1` hace falta **131×86**, no 130×85; con `cell_size = 0.5`, 261×171. `y_scale` debe ser `> 0` y todas las muestras `0` dan altura 0. El origin puede ser `(0,0)`. El formato binario `.heightmap` + `.manifest` es obligatorio para `-map`; la alternativa es que el server acepte una representación plana sin archivo (cambio de código, no de datos).

**2. Export de contenido.** **server proposal — awaiting Duarte confirmation**: `content.json` reproducible desde `classes.js`/`abilities.js`/`types.js`, con commit/hash de origen, mapping estable `classId 1–8` (§7.4) y lista explícita de comportamientos no representables por las 21 primitivas. Verificado en el cliente: hay **40 entradas**, exactamente **21 runners `do`**, y el catálogo es 100% data; las closures viven en la capa `RUNNERS`/`statusOnHit`, no por habilidad. El server valida en boot (ids, 3 por kit, primitivas implementadas) y **no copia tablas a mano**.

**3. Input de movimiento y versión.** v2 manda `speed` absoluto y clampa en 10 u/s; el cliente necesita 10.25 (umbraclaw) y 14.86 con `veil` (+45%). **server proposal — awaiting Duarte confirmation**: nuevo major **v3** con `moveMag 0..1` autoritativo; el server deriva `speed_clase × moveMag × multiplicadores` (agua, veil, etc.). Sin cambio silencioso en v2: `protoVer` negocia `[3,3]` y un cliente v2 recibe `VersionMismatch`. Confirmar v3 / no compatibilidad con clientes viejos, o proponer otro plan de convivencia.

**4. Ciclo de partida.** **server proposal — awaiting Duarte confirmation**: auto-start cuando entra el segundo jugador (consistente con que no hay ready gate), `MatchEnd` por KO o rendición, revancha mutua en la misma sala, forfeit inmediato al desconectar (no hay resume), y **sin timeout** en el primer slice salvo duración explícita del cliente.

**5. Contrato de wire/datos.** No hay ids de combate asignados. **server proposal — awaiting Duarte confirmation**: mensajes discretos (`AbilityCast`, `Hit`, `CastRejected`, `CastInterrupted`, `Death`, `MatchEnd`) en canal confiable; snapshots con HP/alive/classId/res/PP/cooldowns/estados y listas acotadas de proyectiles/zonas/traps. Duarte debe dar **máximos simultáneos y campos máximos por lista** para mantener cada envelope < 64 KiB. El server publica proto ids/campos concretos recién después de estas respuestas.

**6. Versiones.** Se acepta `contentVersion` de 8 hex como **detector de mismatch de contenido**, no como token de seguridad ni de autorización. **server proposal — awaiting Duarte confirmation**: regla canónica de serialización/hash (JSON con orden de claves fijo y documentado) y `mapId` + hash de mapa separados, para detectar arena equivocada además de contenido viejo.

### 7.4 Mapping `classId` (abierto)

Propuesto en el orden de la tabla de clases del cliente; Duarte confirma que sea estable:

| `classId` | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 |
|---|---|---|---|---|---|---|---|---|
| key | `terravox` | `umbraclaw` | `psyflame` | `floraviva` | `campeon` | `mortero` | `franco` | `disparador` |

`classId` es aditivo y numérico; `templateId` queda solo como hint visual heredado. Sin este número congelado, el server no puede asignar HP/kit correctos.

### 7.5 Alcance del primer slice (propuesto)

Se implementan y aceptan las **24 habilidades de kit**; las **40** se exportan y cargan, y las **16 de pool** quedan no jugables hasta priorización separada. El primer combate online va **después de M0**: un set chico (p. ej. Terravox vs. Psyflame) y luego olas de primitivas. El server **no** se compromete a las 21 primitivas en una sola entrega monolítica.

### 7.6 Acciones para Duarte (I2)

- [ ] Entregar el paquete completo de arena plana con los 18 radios fijos y ambos spawns; si 130×85 no es la dimensión correcta, corregirla explícitamente.
- [ ] Entregar `content.json` reproducible, commit/hash de origen y excepciones de closures.
- [ ] Confirmar plan de proto v3, mapping `classId`, ciclo de partida/desconexión/timeout, canonicalización del hash y caps de entidades del snapshot.
- [ ] Registrar las respuestas en `PokemonPvpWeb/docs/COMBATE_CLIENTE_A_SERVIDOR.md` y anexar acá el estado; **no** editar el protocolo hasta que el server publique el schema versionado.
