# HEIGHTMAP.md — Contrato del `.heightmap` (handoff Unity → server)

> **Para**: desarrollador del cliente Unity (autor del terreno) y operadores del server.
> **Qué es**: el contrato binario **canónico** del mapa de alturas que el dev de Unity exporta y el server consume. Este documento ES el contrato: si un archivo no cumple esto, el server **se niega a arrancar**.
> **Quién produce / quién consume**: Unity produce `.heightmap` + `.manifest`; el server solo los **lee, valida y samplea** (nunca los escribe — no hay exporter en el repo).
> **Fuente de verdad**: este documento + `internal/world/heightmap.go` (loader) + `internal/world/terrain.go` (sampler). Los tres evolucionan juntos.
> **Fixture de referencia**: `internal/world/testdata/hills.*` — 32×32, cell 10, colina en el spawn (100, 200).

---

## 1. TL;DR — lo que el Unity dev tiene que saber

1. Exportás **DOS archivos** siempre, juntos: `<nombre>.heightmap` (binario) y `<nombre>.manifest` (JSON con sha256 + metadatos redundantes).
2. El binario es **little-endian, header fijo de 34 bytes** + muestras `float32` row-major. Nada de NaN, nada de "no-data": **si no hay terreno, lo horneás en el grid**.
3. El server valida **todo** al arrancar: magic, versión, dimensiones, metadatos, muestras, **sha256 contra el manifest** y coherencia de metadatos. Cualquier falla → **no arranca** (fail-fast, `%w`, sin mapa parcial).
4. Un mapa roto no se "arregla solo": regenerá el par completo con la herramienta que lo exportó y validá antes de transferir.
5. La colina del fixture vive en el spawn (100, 200): el personaje arranca sobre el terreno.

---

## 2. Layout binario (formato pinned)

Header **fijo de 34 bytes**, **TODO little-endian** (LE):

| Offset | Tamaño | Campo | Tipo | Regla |
|--------|--------|-------|------|-------|
| 0 | 7 | `magic` | ASCII | `"MMOHMAP"` exacto. Otro valor → rechazo. |
| 7 | 1 | `reserved` | byte | **Debe ser `0x00`**. Nonzero → rechazo (espacio de extensión futuro). |
| 8 | 2 | `version` | u16 | `1`. Otro valor → rechazo. |
| 10 | 4 | `width` | u32 | `1..2048`. |
| 14 | 4 | `height` | u32 | `1..2048`. |
| 18 | 4 | `cell_size` | f32 | `> 0`, finito. Metros de mundo por paso de muestra. |
| 22 | 4 | `origin_x` | f32 | Finito. Coordenada X de mundo de la muestra (0,0). |
| 26 | 4 | `origin_z` | f32 | Finito. Coordenada Z de mundo de la muestra (0,0). |
| 30 | 4 | `y_scale` | f32 | `> 0`, finito. `world Y = sample × y_scale`. |
| 34 | `width×height×4` | `samples` | f32 row-major | Ver §3. |

**Cota de tamaño**: `width, height ≤ 2048` → body ≤ **16 MiB** (2048×2048×4). No hay archivos más grandes.

---

## 3. Muestras — orientación, unidades y política de no-data

- **Layout row-major**: `samples[j*width + i]` es la muestra de la columna `i` (min X) y la fila `j` (min Z). **Fila 0 = Z mínima, columna 0 = X mínima**.
- **Posición en el mundo**: la muestra `(i, j)` está en `(origin_x + i×cell_size, origin_z + j×cell_size)`.
- **Altura de mundo**: `world Y = sample × y_scale`. El valor del archivo es la **amplitud**; la escala la aplica el server al samplear.
- **Rango seguro**: toda muestra debe ser **finita** (nunca NaN ni ±Inf) y `|sample × y_scale| ≤ 10.000` (metros de mundo). Fuera de rango → rechazo.
- **NO existe sentinel de no-data.** No hay un valor "reservado" que signifique "acá no hay terreno". **Si una celda no tiene suelo, el autor hornea la altura en el grid** (ej.: valle, fosa, o el valor que el gameplay necesite). El server trata toda muestra como altura real.
- **Sampling**: bilinear (4 esquinas, exacto en muestras, OOB → clamp a la muestra de borde más cercana). Determinista: misma grid → mismos valores, siempre.

**Bounds implícitos**: `max_x = origin_x + (width−1)×cell_size`, `max_z = origin_z + (height−1)×cell_size`. Fuera de esos límites el server **clampa** — el cliente no debería depender de eso.

---

## 4. Manifest sidecar `<nombre>.manifest`

JSON (UTF-8), **un archivo por heightmap**, mismo nombre base:

```json
{
  "file": "hills.heightmap",
  "sha256": "32f621978f1d6d3acf60f4c4d19609b782af9e63ad6c9056c619b39660a9fba8",
  "version": 1,
  "width": 32,
  "height": 32,
  "cell_size": 10,
  "origin_x": 0,
  "origin_z": 0,
  "y_scale": 1,
  "min_height": 0,
  "max_height": 25
}
```

| Campo | Qué es | El server lo verifica |
|-------|--------|----------------------|
| `file` | Nombre del `.heightmap` asociado | Informativo |
| `sha256` | **sha256 (hex, lowercase) sobre los bytes EXACTOS del `.heightmap`** | ✅ sí — mismatch → rechazo |
| `version`, `width`, `height`, `cell_size`, `origin_x`, `origin_z`, `y_scale` | Metadatos redundantes del header | ✅ sí — cualquier diferencia contra el header → rechazo |
| `min_height`, `max_height` | Alturas mín/máx de mundo del grid (informativo para tooling) | No (solo informativo) |

**Regla de oro**: el `.heightmap` y su `.manifest` se generan **juntos** y viajan **juntos**. Un manifest viejo contra un binario nuevo (o al revés) es la causa #1 de rechazo en boot.

---

## 5. Verificación en transferencia y despliegue

1. **Gate pre-transfer (CI del dev Unity)**: corré el validador (`cmd/heightmap-validate`, Slice C) sobre el par: `exit 0` = apto.
2. **Copia explícita, sin overwrite silencioso**: copiá el par a staging → re-validá → rename atómico. Nunca un `cp` a ciegas sobre el par activo.
3. **Boot**: el server con `-map <archivo>.heightmap` lee el par, verifica sha256 + metadatos y parsea ANTES de servir. Falló → **refuse to start** (sin mapa parcial, sin servir).
4. **Smoke**: el gate E2E de la colina (Slice B) confirma que el terreno activo samplea como se espera.
5. **Rollback**: revertí al par N−1 (`.heightmap` + `.manifest` **viejos, juntos**) y reiniciá, o revertí el commit del server. El runbook operativo completo vive en `docs/DEPLOYMENT.md` (Slice C).

---

## 6. Checklist del dev Unity (producir el artifact)

- [ ] Exportás SIEMPRE el par `.heightmap` + `.manifest` (nunca uno solo).
- [ ] Header: `"MMOHMAP"` (7 bytes ASCII) + byte reservado `0x00`.
- [ ] Todo little-endian: version u16=1, width/height u32 (1..2048), cell_size/origin/y_scale f32.
- [ ] `cell_size > 0` y `y_scale > 0`, todos los f32 finitos.
- [ ] Body = exactamente `width×height` muestras f32 row-major (fila 0 = min Z, col 0 = min X), sin bytes de más ni de menos.
- [ ] Ninguna muestra NaN/±Inf; `|sample × y_scale| ≤ 10.000`.
- [ ] Sin sentinel de no-data: los huecos de terreno van horneados como altura real.
- [ ] `sha256` del manifest = hash EXACTO de los bytes del `.heightmap`.
- [ ] Metadatos del manifest idénticos al header (version/dims/cell_size/origin/y_scale).
- [ ] Validaste con `heightmap-validate` (exit 0) antes de transferir.
- [ ] Transferís el par como una unidad atómica (staging → re-validar → rename).

---

## 7. Ejemplo canónico — la fixture `hills`

El fixture commiteado (`internal/world/testdata/hills.*`) es un mapa de referencia válido:

- 32×32 muestras, `cell_size = 10`, `origin = (0, 0)`, `y_scale = 1` → cubre `[0, 310] × [0, 310]`.
- Colina suave centrada en la muestra `(10, 20)` = **mundo (100, 200)**, el punto de spawn del dev-auth: pico `25` m, radio `8` muestras, base plana `0`.
- `min_height = 0`, `max_height = 25`.

Sirve para probar el pipeline completo sin que Unity exporte nada: el server lo embebe como mapa default (`DefaultHeightfield`).