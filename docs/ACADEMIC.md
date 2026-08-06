# ACADEMIC.md — El protocolo de un MMO explicado desde los primeros principios

> **Para**: vos, que sos desarrollador Go + Unity y estás aprendiendo arquitectura de netcode para MMOs.
> **Qué es esto**: un documento académico que explica **por qué** este servidor está construido así: la tecnología, la estrategia y las razones de cada decisión.
> **No es el contrato operativo**: el contrato técnico exacto (bytes, mensajes, pasos) vive en [`PROTOCOL.md`](PROTOCOL.md). Este documento complementa ese contrato — leé los dos juntos.
> **Cómo leerlo**: primero la Introducción y el Glosario; después los capítulos conceptuales (cada uno responde *qué problema resuelve → cómo funciona → por qué elegimos ESTO → qué alternativas había*); el decision log; el mapeo al código; y al final el contexto histórico, el porqué del workflow del proyecto y las rutas de aprendizaje.

---

## Índice

1. [Introducción](#1-introducción)
2. [Glosario](#2-glosario)
3. [Capítulos conceptuales](#3-capítulos-conceptuales)
4. [Decision log — qué decidimos y por qué](#4-decision-log--qué-decidimos-y-por-qué)
5. [Cómo mapea el código](#5-cómo-mapea-el-código)
6. [Contexto histórico — cómo lo resolvieron los MMOs](#6-contexto-histórico--cómo-lo-resolvieron-los-mmos)
7. [Por qué el workflow del proyecto](#7-por-qué-el-workflow-del-proyecto)
8. [Preguntas frecuentes y conceptos erróneos](#8-preguntas-frecuentes-y-conceptos-erróneos)
9. [Ruta de aprendizaje](#9-ruta-de-aprendizaje)
10. [Referencias cruzadas al código](#10-referencias-cruzadas-al-código)

---

## 1. Introducción

### 1.1 ¿Qué es este servidor?

Este repo es el **servidor de un juego multijugador masivo en línea (MMO)** escrito en Go. Es el "mundo" donde los jugadores existen: recibe sus intenciones de movimiento, simula el mundo de forma **autoritativa** (el servidor manda, el cliente obedece) y les devuelve **instantáneas** periódicas del estado de todos los demás jugadores.

El cliente es un juego **Unity** (C#) que vive en otro repo. Este servidor define el **contrato binario** que ambos hablan: el protocolo de red.

### 1.2 ¿Qué problema resuelve un MMO?

Un MMO no es "muchos clientes conectados a un socket". El problema real es:

> **Muchos clientes comparten un mundo simulado, cada uno necesita ver el estado del mundo *casi* en tiempo real, y ninguno es de fiar.**

De ese enunciado salen todos los problemas técnicos que este proyecto resuelve:

| Problema | Pregunta técnica que genera |
|---|---|
| Muchos clientes | ¿Cómo hago para que un jugador vea *solo* lo que le importa? → **interest management** |
| Comparten un mundo simulado | ¿Quién decide dónde está cada uno? ¿Qué pasa si dos clientes mandan posiciones distintas? → **server-authoritative** |
| Casi en tiempo real | ¿Qué transporte uso? TCP es fiable pero lento bajo pérdida; UDP es rápido pero no fiable → **transporte híbrido** |
| Contrato binario que evoluciona | ¿Cómo cambio el protocolo sin romper a los clientes que ya están jugando? → **versionado + serialización con schema** |

### 1.3 ¿Qué vas a aprender leyendo esto?

1. Por qué el servidor usa **TCP y UDP a la vez** (y qué mensaje va en cada canal).
2. Por qué **protobuf** y no JSON, ni MessagePack, ni binario a mano.
3. Qué es el **envelope de 11 bytes**, cómo se "framea" un mensaje en TCP y por qué UDP se limita a 1200 bytes.
4. Por qué el cliente **nunca manda posiciones** — solo intenciones — y por qué el servidor corrige todo.
5. Por qué el mundo simula a **20 Hz** con un **delta fijo** y sin `time.Sleep`.
6. Qué es el **interest management por chunks** y por qué el contrato ya está listo para eso aunque v1 haga broadcast plano.
7. Cómo evoluciona el protocolo **sin romper a nadie**.
8. Por qué el proyecto se trabaja con **TDD estricto, PRs chicos y commits por work-unit**.

---

## 2. Glosario

| Término | Qué significa en criollo |
|---|---|
| **MMO** | Massive Multiplayer Online: miles de jugadores compartiendo un mismo mundo persistente y simulado. |
| **Tick** | Un paso de simulación con duración fija. El mundo no avanza "continuo": avanza de a ticks. Acá: 20 ticks por segundo (50 ms cada uno). |
| **Snapshot** | Una "foto" del estado del mundo (o de parte de él) que el servidor le manda al cliente: quién está, en qué posición, hacia dónde mira. |
| **Interpolación** | En el cliente: suavizar el movimiento entre dos snapshots (el render se mueve entre la posición de la foto A y la de la foto B). Trabajo futuro — en v1 el cliente solo aplica el último snapshot. |
| **Interest management** | "Gestión de interés": decidir **qué** le importa ver a cada jugador para no mandarle todo a todos. Acá: grid de chunks de 64 unidades, vista 5×5. |
| **Codegen** | Generación de código: un programa (protoc) lee el `.proto` y escribe el código Go y C#. El código generado **se commitea** — nadie lo escribe a mano. |
| **Protobuf** | Protocol Buffers (proto3): formato de serialización binario con schema. Los mensajes se definen en un `.proto` y el compilador genera las clases. |
| **IL2CPP** | El backend de Unity que compila C# a C++ (Ahead-Of-Time). Restringe reflexión: ciertas APIs de protobuf están **prohibidas** en el cliente. |
| **AOT** | Ahead-Of-Time: compilar antes de ejecutar (vs JIT, just-in-time). IL2CPP es AOT; rompe con código que usa reflexión en caliente. |
| **seq / ack** | Sequence number (número de orden) y acknowledgement (acuse de recibo). Con seq podés detectar duplicados, huecos y desorden. Acá hay **tres** seq distintos (envelope, Snapshot, MoveInput). |
| **Server-authoritative** | El servidor es la única fuente de verdad del estado del mundo. El cliente manda *intención*; el servidor valida, aplica y corrige. |
| **Cliente autoritativo** | Lo opuesto: el cliente manda su estado final y los demás lo aceptan. Rápido, pero trivialmente hackeable. |
| **MTU** | Maximum Transmission Unit: el tamaño máximo de un paquete de red (típicamente 1500 bytes en Ethernet). Pasarlo = fragmentación IP = problemas. |
| **Latencia** | Tiempo que tarda un paquete en ir de A a B (round-trip ≈ 2×). Se mide en ms. |
| **Jitter** | Variación de la latencia. Peor que la latencia alta: hace que el ritmo de llegada sea irregular. |
| **Head-of-line blocking** | Cuando TCP pierde un paquete, **todo lo que venía después** espera a que el perdido se reenvíe, aunque eso no le importe a nadie. El dato viejo bloquea al nuevo. |
| **Reconciliación / predicción** | Técnicas del cliente: predecir localmente (moverse sin esperar al server) y reconciliarse cuando llega el snapshot autoritativo. Trabajo futuro — el contrato ya está listo. |
| **Chunk / célula** | Celda cuadrada del grid del mundo (64×64 unidades). El servidor decide el interés por célula. |
| **Fanout** | "Abanico": a cuántos clientes se les reenvía un mensaje. v1: a todos (broadcast plano). Futuro: solo a los del view-set. |
| **Envelope** | "Sobre": el header binario fijo que envuelve a cada mensaje (magic, versión, tipo, flags, seq) + el payload protobuf. |
| **Framing** | Cómo se separan los mensajes en el wire: en TCP con prefijo de longitud; en UDP un datagrama = un frame. |
| **Broadcast** | Mandar un mensaje a todos los clientes, sin filtrar. Simple; O(n²) en el peor caso. |
| **Host / endpoint** | Dirección de red (IP + puerto) a la que te conectás. Acá: TCP `:8000`, UDP `:8001`. |

---

## 3. Capítulos conceptuales

> Cada capítulo sigue la misma estructura: **(a) problema** → **(b) cómo funciona** → **(c) por qué elegimos ESTA opción** → **(d) alternativas y tradeoffs**.

### 3.1 TCP vs UDP — por qué un MMO usa AMBOS

#### (a) Problema

Un juego en red manda dos tipos de datos con necesidades **opuestas**:

- **Datos que tienen que llegar sí o sí, en orden** (login, "entraste al mundo", "el jugador X desapareció"). Si se pierden, el estado del cliente queda roto para siempre.
- **Datos que tienen que llegar YA, y si llegan viejos, no sirven** (la posición de un jugador). Un snapshot de hace 2 segundos es **peor que ninguno**: pinta al jugador donde ya no está.

Ningún transporte resuelve ambos bien.

#### (b) Cómo funciona

- **TCP** (Transmission Control Protocol) es un **stream ordenado y fiable**. Garantiza entrega, orden y sin duplicados, y tiene control de congestión. El costo: si un segmento se pierde, los que vienen detrás **esperan** (head-of-line blocking) hasta que el perdido se reenvía. Un paquete perdido = un pico de latencia para **todo** lo que va por esa conexión.
- **UDP** (User Datagram Protocol) es **best-effort**: manda datagramas sin garantía de entrega, orden ni duplicados. Es un "tírale el sobre al otro lado y rezá". El costo: si querés fiabilidad, la tenés que **implementar vos** (retransmisión, orden, dedupe).

#### (c) Por qué elegimos ESTO — transporte híbrido

| Canal | Mensajes | Fiabilidad | Uso |
|---|---|---|---|
| **TCP** (`:8000`) | `Hello`, `ServerInfo`, `AuthRequest/Response`, `EnterWorld`, `WorldSnapshot`, `SpawnEntity`, `DespawnEntity`, `Ack` | Fiable, en orden (stream) | Lifecycle, handshake, auth, spawn/despawn, comandos fiables |
| **UDP** (`:8001`) | `MoveInput` (cliente→servidor), `Snapshot` (servidor→cliente) | No fiable — 1 datagrama = 1 frame | Movimiento y sync del mundo |

La regla es simple: **si es poco frecuente y crítico que llegue → TCP. Si es frecuente y crítico que llegue *fresco* → UDP.**

El movimiento es el caso perfecto para UDP: un `MoveInput` perdido es irrelevante (el jugador mandará otro en 50 ms), y un `Snapshot` viejo es peor que uno recién generado. La pérdida se corrige sola con el **próximo** snapshot — el servidor es autoritativo, así que "perder una actualización" no desincroniza nada de forma permanente.

#### (d) Alternativas y tradeoffs

| Criterio | Solo TCP | Solo UDP | Híbrido (elegido) |
|---|---|---|---|
| Auth/chat/inventario fiables | ✅ nativo | ❌ hay que reimplementar | ✅ TCP |
| Movimiento bajo pérdida | ❌ head-of-line blocking | ✅ lo mejor | ✅ UDP |
| Autoridad + corrección | ✅ | requiere seq/ack | ✅ seq/ack en UDP |
| Complejidad de implementación | Baja | Alta (capa de fiabilidad) | Media |
| Precedente en la industria | MMOs viejos (EVE pre-2009) | — | WoW, Planetside, Overwatch, LoL |

**Qué NO hicimos**: no agregamos una capa de fiabilidad sobre UDP (KCP, reliable-UDP). Eso es scope creep para v1: los inputs son idempotentes y baratos, el `Ack` existe como *registro* pero **no retransmite** (decisión D5). La pérdida se absorbe con el siguiente snapshot.

---

### 3.2 Serialización — protobuf y por qué está prohibido el binario a mano

#### (a) Problema

Los mensajes viajan como bytes. Necesitamos un formato que sea:
1. **Chico** (cada byte cuenta a 1200 bytes por datagrama UDP y a 10 Hz por jugador).
2. **Rápido de codificar/decodificar** (millones de mensajes por hora).
3. **Tipado en Go y en C#** (los dos lados deben producir/consumir la misma estructura).
4. **Evolucionable** (el contrato cambia sin romper a los clientes viejos).
5. **Legible como contrato** (que el dev de Unity pueda implementar contra un archivo, no contra la memoria del que lo escribió).

#### (b) Cómo funciona

**Protocol Buffers (proto3)** parte de un archivo de schema: `proto/v1/world.proto`. Ahí definís mensajes, campos y números de campo:

```proto
message EntityState {
  string id = 1;
  Vec2   pos = 2;   // x, z sobre el plano
  Vec2   velocity = 3;
  float  yaw = 4;   // en radianes
}
```

Con `protoc` (o buf) se generan **automáticamente** las clases Go (`world.pb.go`) y C# (`World.cs`). El **número de campo es parte del contrato**: 1, 2, 3, 4. No son nombres — son números que viajan en el wire (codificados como varint). Eso es lo que hace posible la evolución de schema: un cliente viejo que recibe un campo nuevo que no conoce **lo ignora y lo preserva**, y un campo removido se marca `reserved` para que **nunca** se reutilice el número.

#### (c) Por qué elegimos ESTO — protobuf

| Criterio | protobuf (elegido) | FlatBuffers | MessagePack | JSON | Binario custom |
|---|---|---|---|---|---|
| Tamaño en wire | Chico | Chico (zero-copy) | Chico | 3–10× más grande | El más chico |
| Velocidad | Rápida | Lecturas rapidísimas | Rápida | Lenta | La más rápida |
| Codegen Go | ✅ de primera clase | ✅ `flatc` | ✅ | ✅ stdlib | — |
| C# / Unity / IL2CPP | ✅ código generado AOT-safe (si evitamos reflexión) | ✅ pensado para juegos, AOT-safe | ⚠️ requiere codegen AOT | ✅ | ❌ a mano |
| Evolución de schema | ✅ números de campo + `reserved` + preservación de desconocidos (forzado por el compilador) | ✅ aditivo | ⚠️ disciplina débil | ✅ pero frágil | ❌ manual |
| Legibilidad del contrato | ✅ el `.proto` ES la documentación | ⚠️ API generada verbosa | ⚠️ atributos | ✅ | ❌ |
| Ecosistema | ✅ protoc, buf, Go+C# | ✅ flatc | Medio | ✅ | ❌ |

**Por qué gana protobuf**: una sola fuente de verdad (`.proto`), codegen de primera en Go, código C# generado **AOT-safe para IL2CPP** si evitamos las rutas de reflexión, y evolución de schema **forzada por el compilador** — no por disciplina del equipo. Y el `.proto` es el documento que el dev de Unity lee.

#### (d) Alternativas y el "qué queda PROHIBIDO"

- **JSON**: legible, pero 3–10× más grande y lento. Solo sirve como overlay de debugging. Un MMO a 10 Hz no puede pagar eso por jugador.
- **MessagePack**: compacto, pero el codegen para Unity/IL2CPP requiere trabajo extra.
- **FlatBuffers**: excelente para el hot path (lecturas zero-copy), pero API generada más verbosa. Lo dejamos como **plan B** si el profiling futuro muestra que los snapshots necesitan zero-copy.
- **gob (Go)**: formato solo de Go — el cliente C# no lo puede leer. **Prohibido**.
- **Binario custom a mano**: el contrato viviría en el código, no en un documento. Un cambio de campo rompería la sincronía entre los dos lados sin que nada te avise. **Prohibido** (hay tests que lo vigilan).

**Prohibido en el cliente Unity**: `DynamicMessage`, `Any`, `JsonParser`. Son APIs basadas en **reflexión en caliente**, y IL2CPP compila ahead-of-time: esas rutas o se rompen o explotan. El cliente serializa **solo** con las clases generadas de `Mmo.V1`.

---

### 3.3 Envelope + framing — cómo viaja un mensaje

#### (a) Problema

Un mensaje no viaja solo: en el wire hay que saber (1) **dónde empieza y termina** cada mensaje, (2) **qué tipo** de mensaje es, (3) **a qué versión** del protocolo pertenece, (4) si espera **acuse de recibo**, y (5) en qué **orden** va. Eso es el **framing** (problema 1) y el **envelope** (problemas 2–5).

#### (b) Cómo funciona

**Envelope — 11 bytes de header fijo, big-endian** (los 2+2+2+1+4 del diseño son **11 bytes**, no 10 — en la prosa vieja del spec se coló un "10-byte" que era una suma mal hecha; el layout explícito manda y así lo documenta `PROTOCOL.md`):

```
byte:  0    1    2    3    4    5    6    7    8    9   10
     ┌────────┬────────┬────────┬──────┬──────────────────┐
     │ magic  │ vers   │ type   │flags │       seq        │  payload (protobuf) ...
     └────────┴────────┴────────┴──────┴──────────────────┘
       u16      u16      u16     u8          u32
      0x4D4D   major   type-id   bits     per-session
      ("MM")                     0x01 needs-ack
                                 0x02 compressed (reservado)
                                 0x04 encrypted  (reservado)
```

- **magic** (`0x4D4D` = "MM"): si un frame no arranca con esto, se descarta al instante. Es el guardia de entrada: detecta basura, conexiones cruzadas y el canal equivocado.
- **version**: el major del protocolo. Se negocia en el handshake. El receptor puede **rechazar antes de parsear el payload**.
- **type**: el id del mensaje (1–12). El registro (`internal/protocol`) despacha a la clase concreta. No hay un `switch` gigante: hay un mapa id → mensaje.
- **flags**: bit0 `needs-ack` (pedí acuse), bit1 `compressed`, bit2 `encrypted` (reservados en v1 — el contrato ya tiene el lugar, la implementación llega después).
- **seq**: contador por sesión, solo significativo en frames con `needs-ack`.

**Framing en TCP** — TCP es un *stream*, no un mensaje: los bytes llegan pegados y en pedazos arbitrarios. Por eso cada frame va con un **prefijo de longitud**:

```
[ 4 bytes BE uint32 = longitud ][ payload ≤ 64 KiB ]
```

El reader lee los 4 bytes, sabe cuánto payload esperar, y lo lee completo — "reensambla" el stream. Reglas duras:
- **Frame > 64 KiB → se cierra la conexión.** Un stream corrompido por el medio no se puede resincronizar.
- **Stream truncado a mitad → error de frame incompleto** (`ErrIncompleteFrame`). Nunca se entrega un frame corrupto: si no está completo, no existe.

**Framing en UDP** — UDP entrega **datagramas**, que son unidades atómicas: **1 datagrama = 1 frame**, y el frame entero (header + payload) no puede superar **1200 bytes**. Por qué 1200: el MTU típico es 1500 bytes; restando los headers de IP (20) y UDP (8) quedan ~1472. 1200 es **conservador y MTU-safe**: garantiza que el datagrama **nunca se fragmente en IP**, porque la fragmentación IP es una fuente clásica de pérdidas y desorden. Los mensajes grandes (spawns, world snapshots) **no van por UDP**: van por TCP.

#### (c) Por qué elegimos ESTO

- **Header binario fijo, no un wrapper protobuf**: el hot path (decodificar un frame UDP) no puede pagar parsear un mensaje wrapper *solo para saber qué contiene*. El type id en el header deja el dispatch en un lookup directo.
- **Magic + versión + tipo en el header**: el frame es **autodescriptivo** — podés validar canal, versión y tipo sin tocar el payload.
- **Prefijo de longitud en TCP**: es la técnica estándar para convertir un stream en mensajes; el reensamblado es trivial y las fallas son detectables.
- **Límite UDP de 1200 B**: si un mensaje no entra en un datagrama, **va por TCP** (separación de canales, decisión A/D). Nunca fragmentamos.

#### (d) Alternativas

- **Framing por newline / delimitador de texto**: no puede transportar binario (un byte 0x0A dentro del payload rompe todo).
- **Sin prefijo de longitud** (mandar el mensaje y esperar que el buffer coincida): imposible de reensamblar.
- **Envelope protobuf (oneof)**: más "elegante" a primera vista, pero cada frame pagaría un parse extra y no respeta el byte-layout del contrato. Lo descartamos en D1.
- **Header con campos legibles (texto)**: más grande y más lento. El header binario de 11 bytes es fijo, predecible y barato.

---

### 3.4 Server-authoritative — por qué el cliente NO es de fiar

#### (a) Problema

El **client-side cheating** es el problema clásico: si el cliente mandara su posición final ("estoy en x=9999"), un hacker mandaría `x=9999` con un cheat engine. Pero hay un problema más sutil y más importante: **cada cliente tiene su propio reloj y su propia latencia**. Si dos clientes creen que el jugador está en lugares distintos, ¿quién tiene razón?

#### (b) Cómo funciona

La regla es una sola frase: **el servidor es la única fuente de verdad**. El cliente no manda posiciones: manda **intención** — `MoveInput{ seq, dir, speed, yaw }`. El servidor:

1. **Valida**: el speed se clampa al límite del servidor (10 u/s). Un input que implique 999 u/s se reduce a 10. La posición autoritativa **nunca** sigue un input sin validar.
2. **Aplica**: integra cinemáticamente — `pos += velocity × dt`, una vez por tick fijo.
3. **Corrige**: manda `Snapshot` con la posición **autoritativa** de todos. El cliente **aplica el último y descarta los viejos** (duplicados y `seq ≤ last-applied`).

El `MoveInput.seq` del cliente se etiqueta al tick que lo procesa (`LastInputTick` / `LastInputSeq` en la entidad): es la base contable para futura corrección/ack, y la prueba de que el servidor sabe *cuándo* procesó cada intención.

#### (c) Por qué elegimos ESTO

Porque es la única arquitectura que escala sin volverse un caos de sincronización. Con el servidor autoritativo:
- **El cheat de posición es imposible**: el servidor decide dónde estás, siempre.
- **La latencia no desincroniza**: el cliente puede estar "adelantado" en su render, pero el estado canónico es uno solo.
- **La pérdida de paquetes se autocorrige**: cada snapshot nuevo sobrescribe el estado. No hace falta retransmitir.

#### (d) Alternativas

- **Cliente autoritativo**: el cliente manda su estado final y los demás lo aceptan. Latencia local cero, pero **trivialmente hackeable** y sin una fuente única de verdad (los peers pueden divergir para siempre). Se usa en juegos casuales, nunca en un MMO serio.
- **Lockstep peer-to-peer**: todos los clientes ejecutan la misma simulación determinista y votan inputs (RTS, fighting games). Rápido pero frágil: un solo desync y el juego se rompe, y es inviable con cientos de jugadores.
- **Predicción + reconciliación (futuro)**: el cliente *adivina* su propio movimiento mientras tanto (predicción) y se *reconcilia* cuando llega el snapshot autoritativo (corrección). Es lo que usan los shooters modernos. **No está en v1** — el contrato ya está listo para eso (el contenedor `Snapshot` y la disciplina de seq no cambian cuando se agregue), pero la implementación del cliente es trabajo futuro.

---

### 3.5 Tick rate y simulación determinista

#### (a) Problema

Una simulación no puede avanzar con pasos de tiempo variables. Si un frame tarda 16 ms y el otro 40 ms, el movimiento de un jugador "se acelera y se frena" según la carga de la máquina. Y si la duración del paso varía, **dos ejecuciones de la misma secuencia de inputs producen estados distintos** — imposible de testear, imposible de reproducir bugs.

#### (b) Cómo funciona

**20 Hz, delta fijo de 50 ms, sin `time.Sleep`.**

El corazón es un **acumulador**: el tiempo real que pasó se convierte en un número **exacto** de pasos fijos, y el sobrante se guarda para el próximo despertar. Nunca hay ticks fraccionarios y el loop **nunca deriva**:

```go
// internal/game/tick.go
type Accumulator struct {
    dt        time.Duration // 50 ms
    remainder time.Duration // sobrante que se arrastra
}
func (a *Accumulator) StepCount(elapsed time.Duration) int {
    if elapsed < 0 { return 0 }        // reloj que retrocede: no correr nada
    total := a.remainder + elapsed
    steps := int(total / a.dt)
    a.remainder = total % a.dt
    return steps
}
```

El loop **no duerme**: se despierta por un canal (`wake <-chan time.Time`) y consume los pasos que el tiempo transcurrido permite:

```
time.Ticker (50 ms) → wake (timestamp) → Accumulator.StepCount(elapsed) → N pasos de Simulation.Step()
```

`time.Ticker` en producción; en tests, timestamps **sintéticos inyectados** — el mismo código, cero `time.Sleep`, determinismo total. Cada `Step()` hace: drena los inputs acumulados → valida y aplica movimiento → integra posiciones → actualiza el interés → emite snapshots (10 Hz staggered).

**Por qué 10 Hz para snapshots si el tick es 20 Hz**: el mundo simula a 20 Hz, pero cada jugador recibe snapshots **cada dos ticks** (`(tick + idx) % 2 == 0`), **staggered** — desfasados entre jugadores para que dos nunca broadcasten en el mismo tick. 10 Hz es suficiente para el render de un MMO y reparte la carga.

#### (c) Por qué 20 Hz y por qué sin `time.Sleep`

- **20 Hz**: la cadencia clásica de gameplay de la era WoW. Cada tick hace movimiento + interés + snapshots; 20 Hz es barato de CPU y alcanza para la sensación de juego de un MMO (donde el render del cliente interpola y suaviza). Subirlo (60 Hz como Overwatch) cuesta CPU y no cambia la *sensación* del MMO, donde la autoridad y el interés importan más que el frame de simulación.
- **Sin `time.Sleep`**: `Sleep` no es preciso (granularidad del scheduler del SO), y meter tiempo real en el loop lo haría **no determinista** — un test no podría reproducir el mismo resultado dos veces. El patrón "wake channel + acumulador" separa **cuándo** nos despiertan (tiempo real, inyectable) de **cuánto** simulamos (pasos fijos, puros). La misma secuencia de inputs → el mismo estado, siempre (S14.2).

#### (d) Alternativas

- **Delta variable** (`dt` real por frame): movimiento dependiente del FPS, drift de float, imposible de testear determinísticamente. Descartado.
- **Fixed dt con `time.Sleep`**: drift de sincronización, tests lentos y flaky. Descartado.
- **Tick rates más altos**: 60 Hz para la simulación — más CPU; no aporta al objetivo v1 (10 Hz de snapshots ya limitan la frescura percibida).

---

### 3.6 Interest management / chunks — por qué no broadcast a todos

#### (a) Problema

Si el servidor le mandara **a cada jugador** una actualización de **todos los demás**, el costo crecería cuadráticamente: con 1000 jugadores, cada tick serían ~1.000.000 de envíos. Además, ¿por qué le importaría a un jugador en el punto A lo que pasa en el punto B, a 5000 unidades? El ancho de banda es finito; hay que **filtrar por relevancia**.

#### (b) Cómo funciona — el grid de chunks

El mundo se divide en **células cuadradas de 64 unidades**. Cada jugador tiene una **vista** de 5×5 células (radio 2, distancia de Chebyshev):

```
    células visibles: ViewRadius = 2  →  (2·2+1)² = 5×5
    ┌───┬───┬───┬───┬───┐
    │   │   │   │   │   │
    ├───┼───┼───┼───┼───┤
    │   │   │   │   │   │
    ├───┼───┼───┼───┼───┤
    │   │   │ P │   │   │   ← P en su célula (64×64 unidades)
    ├───┼───┼───┼───┼───┤
    │   │   │   │   │   │
    ├───┼───┼───┼───┼───┤
    │   │   │   │   │   │
    └───┴───┴───┴───┴───┘
```

Cuando un jugador **cambia de célula**, se computan las células que entran y salen de su vista (`CellChange`): las que entran → los jugadores de ahí deben verlo aparecer (`SpawnEntity`); las que salen → deben verlo desaparecer (`DespawnEntity`). Ambos van por **TCP** (fiable: perder un spawn dejaría una entidad fantasma). El broadcast de snapshots va solo a los jugadores **cuyas células intersectan la vista del que se mueve**.

#### (c) Por qué elegimos ESTO — la separación contrato/implementación

El punto clave del proyecto es: **el contrato ya está listo para chunks, pero v1 implementa broadcast plano.**

- **Contrato (ya listo)**: los mensajes `SpawnEntity`/`DespawnEntity` existen (ids 11/12), y el seam `InterestResolver` está en el código — un resolver de chunks se enchufa sin tocar un solo mensaje del `.proto`.
- **Implementación v1 (plana)**: `FlatResolver` — cada jugador ve a **todos** los demás. Al entrar recibís el `WorldSnapshot` real con todos; los existentes reciben tu `SpawnEntity`; al salir, `DespawnEntity` a los que quedan.

¿Por qué tan "plano" en v1? Porque la escala objetivo todavía no está definida — los tamaños (tick, célula, broadcast) son **constantes ajustables**, no decisiones de producto. Empezar con broadcast plano valida el **pipeline completo** (conectar, mover, ver moverse a otros) con la mínima complejidad, y cuando llegue el día de los 1000 jugadores, se activa el resolver de chunks **sin cambiar el protocolo ni romper el cliente** (S19.2). Esa separación es deliberada: los mensajes de red son el contrato (estable), la lógica interna es la implementación (evolucionable).

#### (d) Alternativas

- **Broadcast a todos**: simple, O(n²), suficiente para mundos chicos. Es lo que hace v1.
- **Radio de distancia continuo**: elegante pero hay que mantener sorted sets y reevaluar distancias constantemente.
- **Zonas/rooms** (WoW clásico): el mundo se parte en zonas con boundaries fijos — más tosco que un grid.
- **Interés por prioridad** (Planetside 2): cada entidad tiene "peso" de relevancia; más complejo, para escalas enormes.
- **Grid de chunks (elegido)**: matemática entera pura (división con floor, distancia de Chebyshev), determinista, testeable con tablas, y el estándar de la industria para MMOs de mundo abierto.

---

### 3.7 Versionado de protocolo — cómo evolucionar sin romper a nadie

#### (a) Problema

Los clientes y el servidor se actualizan en momentos distintos. Si mañana agregás un mensaje o cambiás un campo, los clientes viejos **no deben romperse** — y un cliente con una versión **demasiado vieja o demasiado nueva** debe ser rechazado *educadamente*, no colgado.

#### (b) Cómo funciona

- **El major del protocolo vive en el envelope** (`version`, u16). Cada frame se identifica solo.
- **Se negocia en el handshake**: el cliente manda `Hello{ protoVer }`; el servidor valida **tanto** la versión del envelope **como** el `Hello.protoVer` contra su rango soportado (`[1,1]` en v1). Fuera de rango → `VersionMismatch{ minVer, maxVer }` y cierre.
- **Los minors son aditivos, siempre**: mensajes nuevos → **ids de tipo nuevos** (11, 12, …); campos nuevos → **números de campo nuevos**; campos removidos → `reserved`, nunca se reutiliza el número. La preservación de campos desconocidos (ver 3.2) hace que un cliente viejo y un server nuevo **interoperen**: lo que no conocés, lo ignorás y lo devolvés intacto.

```
v1 (hoy)        ids 1..12            clientes con major=1 ✔
v1.1 (futuro)   + id 13, + campo 5   clientes v1.0 siguen andando (desconocidos preservados)
v2 (futuro)     major=2              clientes major=1 → VersionMismatch + rango
```

#### (c) Por qué elegimos ESTO

Porque la alternativa — "no versionar" — es un desastre garantizado: un cambio de contrato *cualquiera* rompería a todos los clientes conectados en ese instante. Y el "major en el envelope" es lo que hace que el rechazo ocurra **antes** de parsear nada: barato, claro, y con el rango soportado como mensaje de error útil.

#### (d) Alternativas

- **Sin versionado**: cualquier cambio rompe todo. Inviable.
- **Versión como parte del payload**: tenés que parsear el mensaje para saber si podés parsearlo. Circular.
- **Minors que rompen compatibilidad**: la disciplina "aditivo o nada" es lo que mantiene la promesa de interoperabilidad; romperla la destruye silenciosamente.

---

## 4. Decision log — qué decidimos y por qué

### 4.1 Decisiones de exploración (A–E)

| # | Decisión | Qué | Por qué | Qué hubiera pasado si elegíamos otra cosa |
|---|---|---|---|---|
| **A** | Transporte híbrido TCP+UDP | TCP para lifecycle/auth/spawn; UDP para MoveInput y snapshots | Fiabilidad donde importa, frescura donde importa (3.1) | Solo TCP: movimientos con picos de latencia por head-of-line blocking. Solo UDP: reimplementar fiabilidad a mano para auth/inventario (error-prone). |
| **B** | Serialización protobuf (proto3) | Schema único `.proto` → Go + C# generados y commiteados | Single source of truth, evolución forzada por el compilador, C# AOT-safe | JSON: 3–10× más grande y lento. Binario a mano: el contrato vive en código, no en un doc — un cambio de campo desincroniza los dos lados silenciosamente. |
| **C** | Framing + envelope | TCP con prefijo de longitud ≤64 KiB; UDP 1 datagrama ≤1200 B; envelope de 11 bytes con magic/ver/type/flags/seq | Stream → mensajes reensamblables; MTU-safe sin fragmentación; frames autodescriptivos | Sin prefijo de longitud: imposible reensamblar TCP. UDP sin tope: fragmentación IP y pérdidas. |
| **D** | Mundo simulado autoritativo | Tick 20 Hz fijo, snapshots 10 Hz staggered, seq/ack, grid 64u/5×5 | El servidor es la única verdad; la pérdida se autocorrige (3.4, 3.5, 3.6) | Cliente autoritativo: cheat trivial y divergencia sin fuente única de verdad. |
| **E** | Arquitectura Go por capas | `cmd/server` + `internal/{protocol,network,session,game,world}` | Cada capa mapea a una responsabilidad de red; se testea por separado (5) | Todo en un main.go: imposible de testear, de razonar y de evolucionar. |

### 4.2 Decisiones de diseño (D1–D7)

| # | Decisión | Opciones | Por qué | Qué hubiera pasado con la otra |
|---|---|---|---|---|
| **D1** | Envelope binario de 11 bytes + payload protobuf | (a) header binario fijo; (b) wrapper protobuf oneof | Byte-layout exacto del contrato, hot path barato, type id para el registro | (b) viola el byte-layout y agrega un parse por frame UDP |
| **D2** | Registro `map[typeID]→mensaje` en `internal/protocol` | (a) registro; (b) un oneof gigante | Compleción y dispatch desconocido testeables en ambas direcciones; paridad con el `.proto` forzada por test | (b) un switch gigante que crece con cada mensaje |
| **D3** | `EntityState` = `Vec2{x,z}` + `velocity` + `yaw` (sin pitch/roll) | (a) Vec2 + yaw; (b) Vec3 + pitch/roll | "Sin pitch/roll" **estructuralmente imposible** — la cámara es local al cliente y no se networkea | (b) riesgo de sincronizar rotación que no debería ir por red |
| **D4** | Seam `InterestResolver` (interfaz) | (a) interfaz; (b) hardcodear "todos" | El resolver de chunks se enchufa después con **cero cambios de protocolo** (3.6) | (b) el día de los chunks habría que tocar el contrato o reescribir el server |
| **D5** | Ack sin retransmisión (v1) | (a) `Ack{seq}` + sin retransmitir; (b) reliable-UDP completo (KCP-like) | Cumple "seq/ack para autoridad" con lo mínimo; los inputs son idempotentes y baratos; el snapshot siguiente corrige | (b) scope creep (spec dice NO KCP) y complejidad de una capa de fiabilidad |
| **D6** | Codegen con protoc + protoc-gen-go + protoc-gen-csharp, pinned | (a) protoc; (b) buf como orquestador | Toolchain mínima; C# vía plugin remoto de buf (no hay binario Windows de protoc-gen-csharp sin toolchain C++) | (b) buf completo: mejor UX pero una dependencia más |
| **D7** | Entrega en PRs encadenados | 3 PRs (sub-cortados a 5 reviewables) | Cada PR = work-unit entregable, CI verde, revisable solo (7) | Un PR gigante de 3.300 líneas: imposible de revisar bien |

**Bump de contrato (decisión adicional)**: se agregaron `SpawnEntity` (id 11) y `DespawnEntity` (id 12) — **aditivo puro**, ids 1–10 congelados. Fue el ejemplo real de cómo evoluciona el contrato sin romper nada: se regeneró el código (Go y C#) y el registro despachó los ids nuevos. Los tests de paridad pasaron de exigir ids 1–10 a 1–12.

### 4.3 Decisiones de wiring (PR4b)

| Decisión | Qué | Por qué |
|---|---|---|
| **Goroutine sim-owner** | Una sola goroutine (`runSim`) es dueña del `Simulation`: registra/remueve jugadores, hace `Step`, drena eventos de interés | Las APIs no-threadsafe de `internal/game` no pueden correr concurrentes con `Step`; el dueño único elimina las races (ver 5.3) |
| **`internal/server` como paquete** | El wiring vive en `internal/server`, no en `cmd/server` | `cmd/server/main.go` queda como parser de flags; el E2E puede importar el server headless |
| **Simulación con `NewAccumulator` + `Step`** | `runSim` espeja `Simulation.Run` pero además atiende `simOps` | `Run` bloquearía los joins/leaves; el dueño único conserva el determinismo (mismo acumulador + Step puros) |

---

## 5. Cómo mapea el código

### 5.1 El viaje de un paquete, de punta a punta

```
CLIENTE UNITY (repo separado)                    SERVIDOR (este repo)
─────────────────────────────────                ─────────────────────────
UI/Presentation → Application → GameClient → NetworkClient → TCP/UDP
                                        │
        MoveCommand                     │
        GameClient.Serialize ───────────┼──────────►  TCP :8000 / UDP :8001
                                        │
                                        ▼
              internal/network   framing: TCP (prefijo longitud) / UDP (datagrama)
                                        │
                                        ▼
              internal/protocol  envelope 11B (magic/ver/type/flags/seq)
                                        │        + registro type-id 1..12 → encode/decode
                                        ▼
              internal/session   máquina de estados: connecting → handshaking →
                                        │      authenticating → entering → in-world
                                        │      handshake 7 pasos, auth, UDP bind
                        │ join/leave (simOps)      │ MoveInput (QueueInput)
                        ▼                          ▼
              internal/server   runSim — UNA goroutine dueña de la simulación
                        │        │
                        ▼        ▼
              internal/game    Simulation.Step (20 Hz): movimiento + snapshots 10 Hz
                        │        │
                        ▼        ▼
              internal/world   InterestTracker + InterestResolver (v1 flat)
                        │
                        ▼
              snapshots → sink UDP (por el peer bindeado)
              SpawnEntity/DespawnEntity → TCP (fanout de interés)
```

### 5.2 Qué hace cada paquete y por qué se llama así

| Paquete | Responsabilidad | Por qué se llama así |
|---|---|---|
| `proto/v1/world.proto` | **EL CONTRATO**: todos los mensajes, type-ids 1–12 | Es el schema del "mundo" que el juego simula; `v1` por la versión mayor |
| `proto/v1/gen/` | Código generado (Go `world.pb.go`, C# `World.cs`) **commiteado** | El Unity dev nunca corre protoc — el binario ya está en el repo |
| `internal/protocol` | Envelope (header 11 B) + registro id↔mensaje | La capa del *protocolo*: cómo se ven los bytes |
| `internal/network` | Framing TCP (prefijo longitud) + UDP (datagrama ≤1200 B) + interfaz `Transport` | La capa de *red*: cómo viajan los bytes |
| `internal/session` | Máquina de estados, handshake 7 pasos, auth, UDP bind | Una *sesión* = una conexión de cliente y su ciclo de vida |
| `internal/game` | Tick loop, movimiento cinemático, snapshots | La *simulación del juego*: entidades, física, broadcast |
| `internal/world` | Grid de chunks (64u, 5×5), `InterestResolver`, `InterestTracker` | El *mundo*: dónde está cada cosa y quién la ve |
| `internal/server` | Wiring: accept loop, UDP loop, sim-owner, fanout, shutdown | El *servidor* que pega todas las capas |
| `cmd/server/main.go` | Flags (`-tcp`, `-udp`, `-tick`, `-dev-auth`, `-spawn-x/z`) + señal de cierre | El entry point ejecutable |
| `internal/e2e` | Gate headless: A se mueve → B lo ve (TCP loopback + UDP real) | La prueba *end-to-end* del slice completo |
| `docs/PROTOCOL.md` | El contrato en lenguaje humano para el dev de Unity | La documentación que se lee, vs el `.proto` que se compila |

### 5.3 El ciclo de vida de un jugador (el flujo completo)

```
Cliente                                      Servidor
  │ TCP connect (:8000)                         │ acceptLoop → session.NewSession
  ├────────────────────────────────────────────►│  state = connecting
  │ Hello{ protoVer: 1 }                        │  checkVersion + Hello.ProtoVer ∈ [1,1]
  ├────────────────────────────────────────────►│  → handshaking
  │◄── ServerInfo{ protoVer:1, tickRate:20, serverTime } ─┤
  │ AuthRequest{ username, password }           │  → authenticating
  ├────────────────────────────────────────────►│  devAuthenticator: username = playerID
  │◄── AuthResponse{ ok, playerId, spawnPos, udpToken(16B) } ─┤  → entering
  │ EnterWorld                                  │  → in-world
  ├────────────────────────────────────────────►│  WorldSnapshot VACÍO (ack, needs-ack)
  │◄── WorldSnapshot vacío ─────────────────────┤  enterWorld: RegisterPlayer + WS REAL
  │◄── WorldSnapshot REAL (todos, v1 flat) ─────┤  fanout: SpawnEntity tuyo a los demás
  │ UDP :8001 — primer datagrama = token CRUDO  │  udpLoop → HandleUDP → bind addr
  ├────────────────────────────────────────────►│
  │ MoveInput{ seq, dir, speed, yaw }           │  QueueInput (mutex-safe, de cualquier goroutine)
  ├────────────────────────────────────────────►│  runSim: Step (20 Hz, dt fijo 50 ms)
  │◄── Snapshot{ seq, entities } @10 Hz staggered┤  FullStateAssembler → sink → UDP
  │◄── SpawnEntity / DespawnEntity (TCP) ───────┤  fanoutInterest (interés v1 flat)
```

Detalles finos que vale la pena notar (verificados contra el código):

- **Dos `WorldSnapshot` al entrar**: el primero (vacío) es el acuse del `EnterWorld` y lleva `needs-ack`; el segundo (real) lo manda `internal/server.enterWorld` después de registrar al jugador en la simulación — es la primera vista autoritativa del mundo.
- **El token UDP es el datagrama crudo**: no hay mensaje `BindRequest` en v1. El primer datagrama *es* el token; si coincide exacto, se bindea la dirección y **solo** esa dirección recibe snapshots. Tokens incorrectos se ignoran.
- **Tres seq distintas**: `envelope.seq` (per-session, solo en frames con needs-ack), `Snapshot.seq` (monotónico global del server, para descartar viejos), `MoveInput.seq` (del cliente, etiqueta el input al tick).
- **Cada frame se codifica con la versión del *destinatario*** (`sess.WireVersion()`): si en el futuro negociás versiones distintas por cliente, el fanout respeta la versión de cada uno.
- **Login duplicado se rechaza**: un segundo `playerId` ya conectado cae (la segunda conexión).

### 5.4 Concurrencia: las tres goroutines del server

El server corre tres goroutines coordinadas:

```
┌─ acceptLoop: acepta TCP, una goroutine por conexión (handleConn)
│     → sesión → al llegar a in-world: simDo(RegisterPlayer) + WorldSnapshot REAL
│     → al desconectarse: simDo(RemovePlayer) → DespawnEntity a los que quedan
│
├─ udpLoop: lee datagramas → HandleUDP (bind/foreign) → QueueInput (MUTEX-safe)
│     QueueInput es el único punto de entrada concurrente a la simulación
│
└─ runSim (SIM-OWNER): dueño exclusivo de Simulation.Step / Register / Remove /
      TakeInterestEvents / AssembleWorldSnapshot
      → atiende simOps (join/leave) y wakes del ticker (Step)
      → tras cada step/op: fanoutInterest (SpawnEntity/DespawnEntity por TCP)
      → sink de snapshots: SendSnapshot → UDP del peer bindeado
```

**Por qué un dueño único**: `internal/game` no fue diseñado threadsafe (solo `QueueInput` tiene mutex). Si dos goroutines llamaran `Step` a la vez, habría races en el registro de entidades y en los eventos de interés. La goroutine sim-owner serializa **todo** — igual que la cola de un actor en Erlang — y elimina la clase entera de bugs de concurrencia sin tocar el paquete `game`.

---

## 6. Contexto histórico — cómo lo resolvieron los MMOs

Estas decisiones no son nuevas: son el resultado de 25 años de MMOs aprendiendo a golpes.

- **Era pre-MMO (Quake, 1996)**: el netcode por UDP era el estándar, con **predicción del cliente** (QuakeWorld) para esconder la latencia. El servidor manda snapshots y el cliente *adivina* entre medio.
- **WoW (2004)**: consolidó el patrón **autoritativo + interest management**. El servidor simula el mundo y los jugadores ven "bandas de relevancia" alrededor de su personaje (cerca = updates frecuentes, lejos = no ve nada). TCP para todo el gameplay; la escala se controla con *no te mando lo que no te importa*.
- **Planetside 2 (2012)**: el caso extremo de **interest management**: cientos de jugadores en el mismo hexágono. Introduce "ghosts" (representaciones ligeras de entidades fuera de la vista directa) y priorización de updates por relevancia.
- **Shooters modernos (Overwatch, Valorant)**: UDP con capas de fiabilidad propias, **snapshots a 30–60 Hz**, **predicción + reconciliación** del cliente y **lag compensation** (reproyectar el hit contra la posición que el servidor veía).
- **EVE Online**: el servidor *puede* **ralentizar el tiempo** del mundo entero (time dilation) cuando la carga es insostenible — la autoridad del servidor lo permite.
- **El patrón que sobrevivió a todo**: *simulación autoritativa + snapshots + interest + (en clientes modernos) predicción/interpolación*. Exactamente la columna vertebral de este proyecto.

¿Por qué híbrido TCP/UDP y no solo UDP como Quake? Porque un MMO tiene una capa de **estado persistente** (cuentas, inventario, presencia) que Quake no tenía. Esa capa quiere TCP. Y la capa de **movimiento** quiere UDP. La industria convergió en el split; nosotros también.

---

## 7. Por qué el workflow del proyecto

El usuario preguntó *por qué hacemos las cosas así*. Respuesta corta: **porque el código se lee más veces de las que se escribe, y se escribe más veces de las que se compila.**

### 7.1 TDD estricto — el test es el diseño

En este proyecto **no se escribe código de producción antes que su test**. El ciclo es RED → GREEN → REFACTOR: primero el test que describe el comportamiento que el spec pide (y falla), después el mínimo código para que pase, después se limpia sin cambiar comportamiento.

¿Por qué? Tres razones:

1. **El test ES el spec ejecutable.** Cada escenario del spec (S2.1, S14.2, S20.1…) tiene un test que lo verifica. "El envelope round-trip funciona" no es una creencia: es un test que falla si alguien lo rompe.
2. **Fuerza a pensar el contrato antes de la implementación.** No podés escribir un test de una API que no definiste. El test te obliga a decidir *qué hace* la función antes de decidir *cómo*.
3. **Red de seguridad para refactorizar.** Con tests verdes, cambiar la implementación es barato y seguro. Sin tests, cada cambio es una apuesta.

Aplicado acá: fuzz tests del codec (nunca panic), tablas del state machine (cada transición legal/ilegal), determinismo del tick (dos sims con los mismos inputs → mismos estados), el gate E2E (A se mueve → B lo ve). Cada uno es una afirmación sobre el *comportamiento*, no sobre cómo está escrito el código.

### 7.2 Presupuesto de 400 líneas por PR — el review importa

Un PR de 2.000 líneas se revisa mal. El **presupuesto de 400 líneas** (additions + deletions) protege la única cosa que no se puede fabricar: la atención del revisor. Un PR chico se puede leer completo, entender completo y criticar completo. Los bugs de diseño se encuentran en el review, no en producción.

### 7.3 PRs encadenados (stacked-to-main) — cada uno es un work-unit

Este proyecto se entregó en **5 PRs encadenados** (cada uno sobre el anterior):

| PR | Work-unit | Entregable |
|---|---|---|
| PR1 | Scaffold + contrato + codegen | `world.proto`, bindings Go/C# commiteados, tests de round-trip |
| PR2 | Codec + framing | Envelope 11 B, registro, TCP/UDP framing, `Transport` |
| PR3 | Sesión | State machine, handshake 7 pasos, auth, UDP bind |
| PR4 | Sim + mundo + wiring + E2E | Tick 20 Hz, movimiento, snapshots, chunks, `internal/server`, gate E2E |
| PR5 | Docs + Docker | `PROTOCOL.md`, `ACADEMIC.md` (este), Dockerfile + compose |

Cada PR es **independientemente revisable, CI-verde y con rollback limpio** (revertir ese commit). El código generado (PR1, ~2.200 líneas) y los docs (PR5) llevan `size:exception`: la excepción se justifica porque no son "carga de review" — el generado es mecánico y los docs se leen, no se discuten línea por línea.

### 7.4 Commits por work-unit — el `git log` cuenta una historia

Los commits no se agrupan por tipo de archivo ("docs", "tests", "fix") sino por **unidad de trabajo entregable**: cada commit deja el repo en un estado que *tiene sentido solo*. El `git log --oneline` de este proyecto es una historia legible:

```
6268f19 build(docker): containerize the MMO server with compose
722d901 docs(protocol): add PROTOCOL.md wire contract for the Unity client
3ce529c test(e2e): prove two players see each other move end-to-end
b6b7516 feat(server): wire TCP/UDP sessions into the 20Hz simulation
...
```

Tests y docs viajan **junto al código que verifican**, no en commits separados. Eso significa que el reviewer puede validar "el código y su prueba" en una sola pasada, y que `git bisect` encuentra el commit que rompió algo con precisión.

---

## 8. Preguntas frecuentes y conceptos erróneos

### ¿Por qué no JSON?

Porque a 10 Hz × N jugadores, el tamaño y la CPU importan. JSON es 3–10× más grande (los nombres de campo viajan por el wire) y más lento de parsear. Además no tiene schema: un cambio de campo se detecta en runtime, no en compilación. JSON queda como **overlay de debugging** si algún día se necesita inspeccionar frames.

### ¿Por qué no confiar en el cliente?

Porque un cliente es un programa que corre en una máquina que **no es tuya**. Cualquiera puede modificarlo. Pero más allá del cheat: cada cliente tiene su propio reloj y su latencia — "la posición" no existe como hecho objetivo si cada uno la define distinto. El servidor es el único punto donde el mundo tiene un estado **único y ordenado**.

### ¿Por qué no un solo socket?

Un solo TCP es lo más simple, pero bajo pérdida todo se bloquea (head-of-line). Un solo UDP obliga a implementar fiabilidad para el lifecycle (auth, spawn) — reimplementar TCP mal. El híbrido toma lo bueno de cada uno: TCP donde el orden importa, UDP donde la frescura importa.

### ¿UDP no es inseguro?

No — la seguridad no depende del transporte. UDP es tan "seguro" como TCP: ambos llevan payload plano si no se encriptan. La seguridad real es **autenticación + cifrado** (TLS en TCP, DTLS o cifrado propio en UDP), que en v1 está **diferida** (contrato plaintext, decisión consciente — el flag `encrypted` del envelope ya está reservado). El token UDP aleatorio de 16 bytes es el control de identidad del canal de movimiento.

### ¿Por qué generar código en vez de escribirlo?

Porque escribir serializadores a mano es la mejor forma de producir bugs sutiles (offsets mal calculados, endianness equivocada, campos que se desincronizan) y de que el contrato "se pierda" en el código. El `.proto` es una fuente de verdad **única y legible**: protoc genera Go y C# que están garantizados de coincidir. El código generado se commitea para que el dev de Unity **nunca** tenga que correr tooling.

### ¿Y si el cliente manda posiciones absurdas?

No puede: el cliente no manda posiciones, manda `MoveInput` (dirección + velocidad + yaw) y el servidor **valida**. Speed > 10 u/s → se clampa a 10. Dirección cero → velocidad cero. El snapshot siguiente lleva la posición **autoritativa** y sobrescribe cualquier cosa que el cliente creyera. El abuso máximo que puede hacer un cliente es moverse a la velocidad máxima permitida.

### ¿Por qué un header binario y no algo legible?

El header binario de 11 bytes es fijo, predecible y cuesta nada de parsear. Un header de texto sería más grande, más lento y ambiguo. La "legibilidad" del contrato está en el `.proto` y en `PROTOCOL.md` — no en el wire.

### ¿Por qué el `Ack` si no hay retransmisión?

El `Ack` es el **registro de recepción** (decisión D5): cumple la promesa de "seq/ack para autoridad" del contrato sin meter una capa de fiabilidad. En v1 los inputs son idempotentes y baratos, y la pérdida la corrige el siguiente snapshot; el `Ack` queda como el mecanismo base para cuando (si) se necesite retransmisión selectiva. Un `Ack` nunca se contesta con otro `Ack` (evita loops).

### ¿Por qué el token UDP viaja como datagrama crudo y no como mensaje?

Porque v1 no define un mensaje `BindRequest` — el contrato se mantuvo mínimo. El primer datagrama UDP **es** el token (16 bytes crudos, sin envelope). Es la única excepción al envelope, está documentado en `PROTOCOL.md`, y si algún día hace falta un handshake UDP más rico, se agrega un mensaje **nuevo** (aditivo).

### ¿Por qué 1200 bytes y no 1472?

1472 es el máximo teórico de un payload UDP sobre Ethernet (1500 MTU − 20 IP − 8 UDP). 1200 deja margen para VPNs, túneles y diferencias de MTU de la ruta. El objetivo es **nunca fragmentar en IP**: la fragmentación multiplica la pérdida (si se pierde un fragmento, se pierde el datagrama entero).

### ¿El servidor no duerme nunca?

Correcto — el loop de simulación **no usa `time.Sleep`**. Se despierta por un `time.Ticker` (en producción) o por timestamps inyectados (en tests), y el acumulador convierte el tiempo transcurrido en un número exacto de pasos fijos. Dormir haría el loop impreciso y los tests no deterministas.

### ¿Por qué dos `WorldSnapshot` al entrar?

El primero (vacío) es el **acuse** del `EnterWorld` — la capa de sesión no sabe nada del mundo. El segundo (real) lo manda la capa de wiring **después** de registrarte en la simulación. Separar las dos responsabilidades (sesión vs mundo) obliga al cliente a esperar el segundo, que es el que realmente tiene estado.

---

## 9. Ruta de aprendizaje

Este proyecto es un mapa. Cada concepto de acá abre un tema más profundo — estos son los topics que valen la pena investigar, en orden:

### Nivel 1 — lo que ya viste acá, en profundidad

- **Protocol Buffers**: varints, encoding de campos, `reserved`, unknown-field preservation. Leé la doc de proto3 y el `.pb.go` generado.
- **TCP/IP**: el three-way handshake, ventana de congestión, Nagle, head-of-line blocking, cómo afecta a los juegos.
- **UDP / MTU / fragmentación**: por qué los juegos odian la fragmentación IP, qué es el PMTUD.
- **Concurrencia en Go**: goroutines, canales, mutex — y el patrón "una goroutine dueña del estado" (actor/sim-owner) que usamos en `internal/server`.
- **Determinismo**: floats, orden de operaciones, y por qué el mismo input no siempre da el mismo resultado (y cómo lo garantizamos acá con delta fijo).

### Nivel 2 — lo que el contrato ya preparó

- **Predicción y reconciliación del cliente**: cómo el cliente simula localmente y se corrige con el snapshot. El contenedor `Snapshot` ya está listo.
- **Interpolación de snapshots**: buffer del cliente, interpolar entre dos snapshots, manejar el jitter.
- **Interest management real**: resolver de chunks, qué eventos spawn/despawn se disparan al cruzar células, y cómo se ve el fanout dirigido.
- **Delta encoding / compresión de snapshots**: mandar solo lo que cambió; el wrap-around de yaw en 0/2π es el ejemplo clásico de edge case.

### Nivel 3 — arquitectura de servidores de juegos a escala

- **Reliable-UDP / KCP / QUIC**: cuándo una capa de fiabilidad propia supera a TCP (y por qué la spec de este proyecto la excluye de v1).
- **Lockstep y rollback** (fighting games/RTS): la alternativa determinista por votación de inputs.
- **Sharding / zonas**: partir el mundo en servidores cuando un solo proceso no alcanza.
- **NAT traversal / relays**: el mundo real detrás de routers; STUN/TURN.
- **Time dilation y load control** (EVE): qué hace el server cuando la simulación no da abasto.

---

## 10. Referencias cruzadas al código

| Concepto | Dónde está en el código |
|---|---|
| Contrato (todos los mensajes, ids 1–12) | `proto/v1/world.proto` |
| Código generado Go / C# | `proto/v1/gen/go/v1/world.pb.go` · `proto/v1/gen/csharp/World.cs` |
| Envelope de 11 bytes (magic/ver/type/flags/seq) | `internal/protocol/envelope.go` (`HeaderSize = 2+2+2+1+4`) |
| Registro id↔mensaje (dispatch) | `internal/protocol/registry.go` (`NewWorldRegistry`) |
| Framing TCP (prefijo longitud ≤64 KiB) | `internal/network/tcp.go` (`WriteFrame`/`ReadFrame`) |
| Framing UDP (1 datagrama ≤1200 B) | `internal/network/udp.go` (`SendDatagram`/`ReadDatagram`) |
| Interfaz `Transport` (seam para tests) | `internal/network/transport.go` |
| Máquina de estados de sesión | `internal/session/state.go` (tabla `legalTransitions`) |
| Handshake 7 pasos + versionado + Ack D5 | `internal/session/handshake.go` |
| UDP bind por token | `internal/session/udpbind.go` |
| Tick 20 Hz + acumulador (sin `time.Sleep`) | `internal/game/tick.go` |
| Movimiento cinemático + clamp de speed | `internal/game/movement.go` |
| Snapshots 10 Hz staggered + `SeqFilter` | `internal/game/snapshot.go` |
| Grid de chunks (64u, vista 5×5) | `internal/world/chunk.go` |
| `InterestResolver` (seam) + `FlatResolver` v1 + tracker | `internal/world/interest.go` |
| Wiring (sim-owner, udp loop, accept loop, fanout, sink) | `internal/server/*.go` (`server.go`, `sim.go`, `udp.go`, `sink.go`) |
| Auth dev (cualquier credencial → username) | `internal/server/auth.go` |
| Entry point + flags | `cmd/server/main.go` |
| Gate E2E (A se mueve → B lo ve) | `internal/e2e/simulation_test.go` |
| Contrato humano para el cliente | `docs/PROTOCOL.md` |
| Toolchain de codegen pinned | `Makefile` |
| Contenedor (multi-stage, no-root) | `Dockerfile` · `docker-compose.yml` |

---

*Documento académico complementario a `docs/PROTOCOL.md`. Todo lo que afirma acá fue verificado contra el código commiteado del repo (pass de exactitud: world.proto, envelope.go, handshake.go, tick.go, interest.go/chunk.go, internal/server, network, registry, main.go). Si un día el código y este documento se contradicen, el código manda — y este documento se actualiza.*
