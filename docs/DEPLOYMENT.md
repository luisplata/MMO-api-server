# DEPLOYMENT.md — Runbook operativo: transferir, activar y revertir mapas

> **Para**: operadores del server y el dev de Unity que entrega el terreno.
> **Qué es**: el runbook de despliegue de un mapa de alturas — del export de Unity al server en producción, con verificación en cada paso y rollback N−1 garantizado.
> **Pre-requisito**: leer [`HEIGHTMAP.md`](HEIGHTMAP.md) — este documento asume el contrato binario canónico (`.heightmap` + `.manifest`, sha256 + metadatos redundantes).
> **El principio rector**: **un mapa roto jamás llega a producción** — el server valida todo al arrancar y se niega a servir con un mapa parcial o corrupto (fail-fast, `%w`, sin mapa parcial).

---

## 1. TL;DR — el pipeline en una línea

```
Unity dev exporta .heightmap + .manifest
  → heightmap-validate (gate, exit 0)
  → copia explícita a staging (nunca overwrite silencioso)
  → re-validación en staging
  → rename atómico al par activo
  → arranque con -map (boot verifica checksum + parsea ANTES de servir)
  → smoke (gate E2E de la colina)
  → activo. Rollback N−1: swap del par viejo + restart (o revert commit + redeploy v1)
```

---

## 2. Roles y artefactos

| Rol | Qué produce / hace |
|-----|--------------------|
| **Unity dev** | Exporta el par `<nombre>.heightmap` + `<nombre>.manifest` con su herramienta (contracto: `HEIGHTMAP.md`). NUNCA entrega un archivo solo. |
| **CI / dev** | Corre `heightmap-validate` sobre el par ANTES de transferir (gate pre-transfer). |
| **Operador** | Copia explícita a staging, re-valida, activa con rename atómico, arranca con `-map`, smoke, y ejecuta el rollback si hace falta. |

El server **solo lee, valida y samplea** el par — no lo escribe, no lo regenera, no lo "arregla". Un par roto se re-exporta en Unity, no se edita a mano.

---

## 3. El gate: `heightmap-validate`

Antes de que el par toque un servidor, se valida con el mismo comando que corre el mismo código del loader de boot (`world.LoadHeightmap` — los mismos checks de magic, reserved byte, version, dims, metadata, body size, muestras finitas/in-rango, sha256 y metadatos redundantes del manifest).

```bash
# Build (una vez)
go build -o heightmap-validate ./cmd/heightmap-validate

# Gate
./heightmap-validate maps/arena.heightmap
```

| Exit | Significado |
|------|-------------|
| `0` | **Válido** — el par pasa todos los checks; imprime identidad del mapa (dims, cell, origin, y_scale, bounds). |
| `1` | **Inválido** — un check falló; el motivo exacto va a stderr (el mismo error `%w` con el que el server se negaría a arrancar). |
| `2` | **Uso** — argumentos incorrectos (falta el path, o hay más de uno). |

**Regla de oro**: `exit 0` es condición necesaria para transferir. Si da `1`, el par no se despliega — se corrige en el exportador (ver `HEIGHTMAP.md` §6 checklist del dev Unity).

---

## 4. Transferencia y staging — sin overwrite silencioso

El par viaja **como una unidad atómica** (los dos archivos juntos). Un manifest viejo contra un binario nuevo (o al revés) es la causa #1 de rechazo en boot.

```bash
# 1. Gate pre-transfer (dev o CI)
./heightmap-validate arena.heightmap || exit 1

# 2. Copia EXPLÍCITA a staging — nunca cp a ciegas sobre el par activo.
#    Staging es un directorio aparte; el par activo no se toca todavía.
mkdir -p /srv/mmo/maps/staging
cp arena.heightmap arena.manifest /srv/mmo/maps/staging/

# 3. Re-validar EN staging (el par que efectivamente se va a activar)
./heightmap-validate /srv/mmo/maps/staging/arena.heightmap || exit 1
```

Nada de `cp` sobre `maps/arena.*` mientras el server corre: el server podría arrancar leyendo un `.heightmap` nuevo con un `.manifest` viejo (o el archivo a medio copiar). El staging + re-validación elimina esa ventana.

---

## 5. Activación atómica

Un solo rename por archivo, en el orden `.manifest` → `.heightmap` (o ambos, con el server detenido). El server nunca lee un par incompleto porque la activación se hace con el server **detenido** o porque la validación de boot rechaza cualquier inconsistencia:

```bash
# 4. Activar: rename atómico del par en staging → activo
mv /srv/mmo/maps/staging/arena.manifest /srv/mmo/maps/arena.manifest
mv /srv/mmo/maps/staging/arena.heightmap /srv/mmo/maps/arena.heightmap
```

> Si el server está corriendo y querés activar sin downtime: parar el server → swap → arrancar. No hay hot-reload de mapa en v1 — el mapa se lee **una vez** al arrancar y queda fijo para la vida del proceso.

---

## 6. Configuración: `-map` y arranque

El server recibe el mapa con el flag `-map` (sin valor = fixture embebido `hills`):

```bash
./mmo-server -tcp :8000 -udp :8001 -map /srv/mmo/maps/arena.heightmap
```

Con docker-compose (el server se configura por flags, ver `docker-compose.yml`):

```yaml
services:
  mmo-server:
    image: mmo-server:latest
    volumes:
      - /srv/mmo/maps:/maps:ro
    command:
      - -tcp
      - :8000
      - -udp
      - :8001
      - -map
      - /maps/arena.heightmap
```

**Boot verification (automática, no opcional)**: al arrancar, `cmd/server` carga el par con `world.LoadHeightmap` — lee los dos archivos, verifica sha256 contra el manifest y cruza los metadatos redundantes contra el header, y parsea el body ANTES de escuchar en ningún puerto. Cualquier falla → `log.Fatalf` → el proceso sale con error **sin haber servido ni una conexión** (no hay mapa parcial). El log de boot nombra el mapa activo:

```
server: map active: /srv/mmo/maps/arena.heightmap
```

---

## 7. Smoke: gate E2E de la colina

Después del arranque, se corre el smoke que prueba el pipeline completo de terreno (dos jugadores, uno en la colina; el otro observa `Y == HeightAt(X,Z)` y `spawnPos.y == 25` en el fixture):

```bash
go test ./internal/e2e/ -run TestTwoPlayersSeeEachOtherMoveEndToEnd
```

El mismo gate que valida el fixture embebido sirve para validar un `-map` externo (ambos pasan por el mismo loader). Si el smoke falla, se activa el rollback (§8).

---

## 8. Rollback N−1

Dos vías, según qué haya fallado:

### 8a. Falló el mapa (swap de artefacto, sin tocar código)

El server **nunca** arranca con un mapa roto (fail-fast), así que "rollback de mapa" aplica cuando el mapa **sí arrancó** pero el gameplay/terreno está mal (p. ej. el smoke falla).

```bash
# 1. Detener el server
systemctl stop mmo-server   # o: docker compose stop mmo-server

# 2. Respaldar el par actual (N) como N+1 por si acaso
mv /srv/mmo/maps/arena.heightmap /srv/mmo/maps/arena.heightmap.failed
mv /srv/mmo/maps/arena.manifest   /srv/mmo/maps/arena.manifest.failed

# 3. Restaurar el par N−1 (que se guardó al activar N, §5) — SIEMPRE juntos
cp /srv/mmo/maps/backup/arena.heightmap /srv/mmo/maps/arena.heightmap
cp /srv/mmo/maps/backup/arena.manifest   /srv/mmo/maps/arena.manifest

# 4. Re-validar el par restaurado (gate otra vez)
./heightmap-validate /srv/mmo/maps/arena.heightmap || exit 1

# 5. Arrancar
systemctl start mmo-server
```

> **Práctica recomendada**: al activar un mapa nuevo, copiar el par anterior a `backup/` (nunca borrarlo). El rollback es entonces "copiar el backup de vuelta + restart" — minutos, no horas.

### 8b. Falló el código del server (revert commit + redeploy)

Los clientes Unity **v2** (envelope `[2,2]`) hacen fail-fast contra un server v1: un server revertido a v1 rechaza el Hello v2 con `VersionMismatch` + cierre. Por eso el rollback de código es un paquete: **server y client viajan en la misma ventana** — `Unity World.cs` del cliente se entrega en lockstep con el server v2, y un revert de server a v1 exige que el cliente también vuelva a v1 (o aceptar que los clientes v2 no conecten hasta el redeploy).

```bash
git revert <commit-del-server-v2>   # o: git checkout <tag-v1> -- cmd internal
# rebuild + redeploy del binario v1
# los clientes v2 se quedan afuera con VersionMismatch hasta que el server v2 vuelva
```

### Qué NUNCA hacer

- **No** editar el `.heightmap` o `.manifest` a mano para "arreglar" un check — el sha256 y los metadatos redundantes lo van a rechazar igual.
- **No** desplegar un `.heightmap` sin su `.manifest` (o viceversa) — el loader falla en boot por diseño.
- **No** mantener dos mapas "activos" a medias: el par activo es **uno solo**, y el backup N−1 es la única red de seguridad.

---

## 9. Checklist del operador (despliegue completo)

- [ ] `heightmap-validate` del par → `exit 0`.
- [ ] Par copiado a staging (nunca `cp` sobre el activo).
- [ ] Re-validación del par en staging → `exit 0`.
- [ ] Backup del par activo anterior guardado en `backup/` (para N−1).
- [ ] Rename atómico del par staging → activo (`.manifest` y `.heightmap` juntos).
- [ ] `-map` apunta al `.heightmap` activo (compose/systemd actualizado).
- [ ] Boot: el log muestra `server: map active: <path>` y el proceso queda escuchando.
- [ ] Smoke E2E de la colina verde.
- [ ] (Si algo falló) Rollback §8a o §8b ejecutado y verificado.