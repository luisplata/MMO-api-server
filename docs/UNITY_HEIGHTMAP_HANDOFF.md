# UNITY_HEIGHTMAP_HANDOFF.md — What the Unity Dev Must Deliver (Terrain Heightmap)

> **To**: Unity developer (terrain author + exporter).
> **What this is**: the practical, self-contained handoff sheet for producing the server's terrain heightmap. The canonical binary contract lives in `docs/HEIGHTMAP.md`; this document is the action sheet on top of it, with exact bytes, Unity↔server mapping, checklist, and validation.
> **Server truth**: verified against `internal/world/heightmap.go` (loader), `internal/world/terrain.go` (sampler), `cmd/heightmap-validate/main.go` (gate), `cmd/server/main.go` (boot). If this sheet and those files disagree, **the files win** — open an issue.
> **Language note**: `MUST` = the server rejects the file if violated. `SHOULD` = strongly recommended; the server does not enforce it.

---

## 1. Executive Summary & Quick Delivery Path

You deliver **two files, always, together**:

```
hills.heightmap   # binary height grid (little-endian, fixed 34-byte header + float32 samples)
hills.manifest    # JSON sidecar: sha256 of the .heightmap bytes + redundant header metadata
```

The server **reads, validates, and samples** this pair at boot (`-map <name>.heightmap`). It never writes it — **there is no exporter in this repo, and there will not be one**. Exporting is your job, inside your Unity project. A broken pair means the server **refuses to start** (fail-fast, no partial map).

Quick path (≈ a day of work):

| # | Step | Result |
|---|------|--------|
| 1 | Read `docs/HEIGHTMAP.md` + this sheet | You know the contract |
| 2 | Author the terrain in Unity (heightmapResolution N, size x/z, position) | The source of truth |
| 3 | Write the exporter (editor C# script) per §6 | `.heightmap` + `.manifest` |
| 4 | Validate locally: `go run ./cmd/heightmap-validate mymap.heightmap` | exit `0` |
| 5 | Ship the pair + validation log in the handoff package (§7) | Server team deploys |
| 6 | Server boots `./server -map mymap.heightmap`; smoke: spawn lands on a known feature | Terrain live |

Total time-box: exporter + validation + handoff. Most failures are one of 4 causes (§8.4) — the validator catches all of them.

---

## 2. What Unity Must Provide — and What the Server Does NOT Need

### Unity provides (MUST)
- The `.heightmap` binary per the exact layout in §3.
- The `.manifest` sidecar per §5.
- Both together, same base name, generated from the **same export run**.

### The server does NOT need (do not send, do not build for it)
- **No exporter code in this repo** — you own the exporter in your Unity project. The server never writes a heightmap.
- No textures, splat maps, tree/detail placements, grass, LODs, physics meshes, or collision data. The server consumes **only the height grid**.
- No Unity runtime components, no scene files, no terrain asset `.asset` files. Only the two exported files.
- No jump/gravity, no client-side interpolation of terrain, no dynamic map edits (see §9).

---

## 3. Exact Canonical Binary Format (`.heightmap`)

**ALL fields are little-endian (LE).** Fixed 34-byte header, then the sample body. No padding, no trailing bytes.

| Offset | Size | Field | Type | Value / Rule |
|--------|------|-------|------|--------------|
| 0 | 7 | `magic` | ASCII | `"MMOHMAP"` exactly. Anything else → rejected. |
| 7 | 1 | `reserved` | byte | **Must be `0x00`.** Nonzero → rejected (future extension space). |
| 8 | 2 | `version` | u16 LE | **`1`**. Anything else → rejected. |
| 10 | 4 | `width` | u32 LE | `1..2048`. Samples along **X**. |
| 14 | 4 | `height` | u32 LE | `1..2048`. Samples along **Z**. |
| 18 | 4 | `cell_size` | f32 LE | `> 0`, finite. World meters per sample step. |
| 22 | 4 | `origin_x` | f32 LE | Finite. World X of sample `(0,0)`. |
| 26 | 4 | `origin_z` | f32 LE | Finite. World Z of sample `(0,0)`. |
| 30 | 4 | `y_scale` | f32 LE | `> 0`, finite. `world Y = sample × y_scale`. |
| 34 | `width×height×4` | `samples` | f32 LE | Row-major, see below. |

**Size limits (MUST)**
- `width, height ≤ 2048` → body ≤ **16 MiB** (2048×2048×4). Bigger → rejected.
- File length MUST be exactly `34 + width×height×4` bytes. Extra trailing bytes or a truncated body → rejected.

**Sample layout (MUST)**
- Row-major: sample `(i, j)` (column `i`, row `j`) is at byte `34 + (j×width + i)×4`, value `samples[j*width + i]`.
- **Row 0 = minimum Z, column 0 = minimum X.** Row index increases along +Z, column along +X.
- World position of sample `(i, j)`: `x = origin_x + i×cell_size`, `z = origin_z + j×cell_size`.
- World height of a sample: `y = sample × y_scale`. The file stores **amplitude**; the scale applies on the server.

**Finite / range constraints (MUST)**
- Every sample MUST be finite — no `NaN`, no `±Inf`. One bad sample → whole file rejected.
- Every sample MUST satisfy `|sample × y_scale| ≤ 10,000` (world meters). Outside → rejected.
- `cell_size`, `y_scale` MUST be `> 0` and finite; `origin_x`, `origin_z` MUST be finite.

**No-data policy (MUST understand)**
- **There is NO no-data sentinel.** No reserved value means "no ground here". If a cell has no ground (valley, pit, void), **bake the height you want into the grid** — the server treats every sample as real height.
- Sampling is deterministic bilinear interpolation with out-of-bounds **clamp to the nearest edge/corner sample** (`HeightAt` never errors). The client should not rely on OOB behavior.

**Implicit bounds**: `max_x = origin_x + (width−1)×cell_size`, `max_z = origin_z + (height−1)×cell_size`.

### 3.1 Worked byte example — minimal valid 2×2 (verified)

A flat 2×2 grid, `cell_size = 1`, `origin = (0,0)`, `y_scale = 1`, all samples `0.0`. Total file = **50 bytes**, sha256 `4943b33e68d07c04bd84b663a9f54a685b69c6c2e02defbe9da656a8457c29f1`.

```
Offset  Bytes                                 Field
0      4d 4d 4f 48 4d 41 50                    magic "MMOHMAP"
7      00                                      reserved
8      01 00                                   version u16 = 1
10     02 00 00 00                             width u32 = 2
14     02 00 00 00                             height u32 = 2
18     00 00 80 3f                             cell_size f32 = 1.0
22     00 00 00 00                             origin_x f32 = 0.0
26     00 00 00 00                             origin_z f32 = 0.0
30     00 00 80 3f                             y_scale f32 = 1.0
34     00 00 00 00  00 00 00 00                samples (0,0) (1,0) = 0.0
42     00 00 00 00  00 00 00 00                samples (0,1) (1,1) = 0.0
```

Reproduce these 50 bytes exactly, add the matching manifest (§5.1), and `heightmap-validate` MUST exit `0`. This is your smallest possible round-trip test.

---

## 4. Unity Terrain → Heightmap Mapping

### 4.1 The coordinate contract

| Concept | Unity | Server `.heightmap` |
|---------|-------|---------------------|
| Ground plane | X / Z | X / Z (same) |
| Up | Y (left-handed, up) | Y derived from terrain (`world Y = sample × y_scale`) |
| Grid samples per side | `heightmapResolution` (N) | `width` (= X), `height` (= Z) |
| Sample `(i, j)` corner | terrain origin corner | `(origin_x, origin_z)` |
| +X direction | terrain local +X | column index `i` increases |
| +Z direction | terrain local +Z | row index `j` increases |
| Cell extent | `terrainSize.x / (N−1)` | `cell_size` |

**MUST**: keep the terrain at **rotation (0, 0, 0)** (no yaw/pitch/roll), or account for it in the exporter. Unity is left-handed/Y-up; the server only cares about X and Z. If the terrain is rotated 180° on Y, your map comes out mirrored — the validator cannot catch that, only your landmark check (§4.3) can.

### 4.2 The exact mapping formulas

Given a Unity terrain with:
- `N = terrainData.heightmapResolution`
- `size = terrainData.size` (meters, `.x` and `.z`)
- `position = terrain.transform.position`

```
width        = N                  // samples along X
height       = N                  // samples along Z
cell_size    = size.x / (N - 1)   // MUST equal size.z / (N - 1); if not, fix the terrain first
origin_x     = position.x         // world X of the terrain origin corner
origin_z     = position.z         // world Z of the terrain origin corner
y_scale      = 1.0                // RECOMMENDED: sample = world meters (as the fixture does)
```

> **Off-by-one trap**: Unity's surface extent is `(N−1)` cells, NOT `N`. `size.x / N` is wrong — it silently shrinks the map. The fixture (`N=32`, `size=(310, 310)`, `cell_size=10`) is exactly `310/(32−1) = 10`.

### 4.3 Reading the heights (row/column order)

`TerrainData.GetHeights(xBase, zBase, w, h)` returns a 2D array indexed **`[z][x]`**: first index = row (Z), second = column (X). `[0][0]` is the terrain origin corner (min X, min Z). This **already matches** the server's row-major layout — copy it straight through:

```csharp
float[,] h = terrainData.GetHeights(0, 0, N, N);
for (int z = 0; z < N; z++)            // row = Z  (server: row 0 = min Z)
    for (int x = 0; x < N; x++)        // col = X  (server: col 0 = min X)
        samples[z * N + x] = h[z, x] * size.y / y_scale;   // 0..1 normalized -> world meters / y_scale
```

`GetHeight(x, z)` returns world units directly (meters); the normalized 0..1 grid times `size.y` gives the same thing. With `y_scale = 1.0`, `sample == world meters`.

### 4.4 Preserving the server coordinate system (landmark check)

The fixture that ships with the server has its hill peak at **world (100, 200)** → sample `(10, 20)`. Before you trust your exporter:

1. Put a distinctive feature (hill/pit) at a **known world coordinate** `(X, Z)` in Unity.
2. Export and check the sample at `i = (X − origin_x)/cell_size`, `j = (Z − origin_z)/cell_size` carries the expected height.
3. On the server side, the dev-auth spawn at `(100, 200)` lands on whatever height that sample has — the E2E hill gate asserts `HeightAt(100,200) == 25` for the fixture.

This is the ONLY check that catches orientation flips (Z mirrored / X mirrored) and origin drift. Do it once per terrain.

---

## 5. `.manifest` JSON Sidecar

JSON (UTF-8), one per heightmap, **same base name** (`hills.heightmap` → `hills.manifest`). Field order is free; Go parses any valid JSON.

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

| Field | Meaning | Server enforces? |
|-------|---------|------------------|
| `file` | Name of the paired `.heightmap` | No (informational) |
| `sha256` | **sha256, hex, LOWERCASE, over the exact bytes of the `.heightmap`** | ✅ Yes — mismatch → reject |
| `version` | Format version, MUST be `1` | ✅ Yes — must equal header |
| `width`, `height` | MUST equal header dims | ✅ Yes |
| `cell_size`, `origin_x`, `origin_z`, `y_scale` | MUST equal the header floats exactly | ✅ Yes (compared as float32) |
| `min_height`, `max_height` | World min/max of the grid (tooling only) | No (informational) |

### 5.1 SHA-256 generation (MUST be exact)

- Hash over the **final bytes** of the `.heightmap` — so **generate the manifest AFTER writing the file**, and **regenerate it every time you re-export** (even one sample changed).
- Linux / Git Bash / WSL: `sha256sum mymap.heightmap` (already lowercase).
- PowerShell: `(Get-FileHash -Algorithm SHA256 mymap.heightmap).Hash.ToLower()` — **Get-FileHash returns UPPERCASE; the server compares lowercase**. Forgetting `.ToLower()` is a top-3 failure cause.
- C# (in the exporter): `Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant()`.

### 5.2 Float consistency between header and JSON (MUST)

`cell_size` / `origin_x` / `origin_z` / `y_scale` in the JSON MUST unmarshal to the **same float32** you wrote in the header. The loader compares them bit-for-bit. Rules:

- **Do not hand-type these numbers.** Emit the manifest from the same source-of-truth values that produced the header.
- Use shortest-round-trip decimal formatting for the f32 (C#: `value.ToString("R")` / `"G9"`; Python: `repr`). `10` → `10`, `0.1` → `0.1` both round-trip fine; only a re-typed, over-precise decimal can drift.
- `version` / `width` / `height` are integers in JSON — do not emit `32.0` or strings.

### 5.3 The golden rule

The `.heightmap` and `.manifest` are generated **together** and travel **together**. A stale manifest against a new binary (or vice versa) is the **#1 cause of boot rejection**.

---

## 6. Exporter Implementation Checklist (Unity dev)

### 6.1 Checklist

- [ ] **MUST**: emit exactly two files: `<name>.heightmap` + `<name>.manifest`, same base name.
- [ ] **MUST**: header fields in the exact offset order of §3 — magic `"MMOHMAP"` (7 ASCII) + reserved `0x00` + version u16 `1` + width/height u32 + cell_size/origin_x/origin_z/y_scale f32.
- [ ] **MUST**: everything little-endian. If you pack bytes manually, write LE explicitly; if you use a stream writer, confirm the platform is LE (it is on x86/ARM64 .NET) and never use a big-endian writer.
- [ ] **MUST**: `width = height = N` (Unity `heightmapResolution`), `cell_size = size.x/(N−1) = size.z/(N−1)`, origin = terrain corner position.
- [ ] **MUST**: body = exactly `width×height` f32 samples, row-major, row 0 = min Z, col 0 = min X, no padding, no trailing bytes.
- [ ] **MUST**: every sample finite; `|sample × y_scale| ≤ 10000`. Watch for `NaN` after any sample post-processing (smoothing filters, division by zero on flat zones).
- [ ] **MUST**: no no-data sentinel — bake gaps into the grid.
- [ ] **MUST**: manifest sha256 = lowercase hex of the exact file bytes; regenerate after every export.
- [ ] **MUST**: manifest metadata identical to the header (version/dims/cell_size/origin/y_scale).
- [ ] **SHOULD**: `y_scale = 1` so samples are world meters (self-documenting, matches the fixture).
- [ ] **SHOULD**: compute `min_height`/`max_height` from the exported grid (informational, keeps tooling honest).
- [ ] **SHOULD**: deterministic output — same terrain + same settings ⇒ byte-identical file ⇒ same sha256 (reviewable diffs).

### 6.2 Deterministic output rules

1. Fixed serialization order: header fields in offset order, then samples row-major.
2. No timestamps, no GUIDs, no locale-dependent number formatting anywhere in the files.
3. Use invariant culture for the JSON numbers (`CultureInfo.InvariantCulture`) — a decimal comma breaks everything.
4. Iterate `z` outer / `x` inner so row-major order is stable across runs.
5. Emit the sha256 from the written bytes, not from a pre-computed buffer.

### 6.3 Reference fixture expectation (what "correct" looks like)

The committed fixture `internal/world/testdata/hills.*` is the canonical reference. Your exporter SHOULD be able to reproduce its shape:

- 32×32, `cell_size = 10`, `origin = (0, 0)`, `y_scale = 1` → world bounds `[0, 310] × [0, 310]`.
- Smooth hill centered at sample `(10, 20)` = world **(100, 200)**: peak `25` m, radius `8` samples, flat base `0`.
- `min_height = 0`, `max_height = 25`; sha256 `32f621978f1d6d3acf60f4c4d19609b782af9e63ad6c9056c619b39660a9fba8`.

Expected server behavior on it (your acceptance target):
- `HeightAt(100, 200) == 25` (spawn lands on the peak).
- `HeightAt(0, 0) == 0` (flat base).
- Validator exits `0` with the summary below.

And the smallest possible fixture (from §3.1): the 50-byte flat 2×2 with sha256 `4943b33e68d07c04bd84b663a9f54a685b69c6c2e02defbe9da656a8457c29f1`.

---

## 7. Local Validation

### 7.1 The validator command (read the actual usage — it takes a positional path, NOT `--map`)

```bash
# from the repo root (D:\Web\MMO-api-server)
go run ./cmd/heightmap-validate mymap.heightmap
```

`<map.heightmap>` is a **positional argument**. There is no `--map` flag on this command — `-map` is the **server's** flag (`./server -map mymap.heightmap`). Passing `--map` or zero/multiple paths exits `2` with usage on stderr.

Exit codes:

| Exit | Meaning |
|------|---------|
| `0` | **Valid** — the pair passes every boot check (magic, reserved, version, dims, metadata, body size, finite/in-range samples, manifest sha256 + redundant metadata). |
| `1` | **Invalid** — a check failed; the actionable reason is printed to **stderr** (the same wrapped error the server would refuse to boot with). |
| `2` | **Usage error** — wrong arguments. |

Valid output (the fixture):

```
valid: <path-to-hills.heightmap>
  dims: 32x32, cell 10, origin (0, 0), y_scale 1
  bounds: x [0, 310], z [0, 310]
  manifest: sha256 + redundant metadata verified
```

**Failure example** (expected exit `1`):

```
heightmap-validate: <path>.heightmap: invalid: world: heightmap manifest mismatch: sha256 <...>, want <...>
```

### 7.2 What the validator checks (it IS the server's boot check)

The command imports only `internal/world` and delegates to the exact loader the server boots with (`world.LoadHeightmap`). Zero parsing duplication: **if it exits 0, the server will accept the map at boot.** Corruption classes it rejects (all exit 1): bad magic, nonzero reserved byte, bad version, dims out of range, non-finite sample, out-of-range sample, truncated/trailing body, missing files, manifest sha256 mismatch, manifest metadata mismatch.

### 7.3 The handoff package (what you send)

```
terrain-handoff/
├── mymap.heightmap          # binary
├── mymap.manifest           # JSON sidecar
└── validation.log           # captured stdout+stderr of heightmap-validate (exit 0)
```

Include in the package or the PR description:
- The validator's `valid:` summary (dims/cell/origin/y_scale/bounds) — proves WHICH map passed.
- The sha256 you got vs. the manifest's `sha256` (they must match).
- A note on the coordinate landmark you verified (§4.4) and its expected height.

---

## 8. Transfer, Deployment, Versioning, Review

### 8.1 Transfer (staging, no silent overwrite)

1. **Gate**: run `heightmap-validate` on the pair in CI or locally — exit `0` required before anything moves.
2. **Stage**: copy **both** files to staging → re-validate at the staging path → **atomic rename** into place.
3. **Never** `cp` over the active pair blindly. If the new pair fails, the server keeps serving the old one until you finish.
4. Full operational runbook: `docs/DEPLOYMENT.md` (gate, staging, activation, smoke, rollback).

### 8.2 Server boot

```bash
./server -map /data/maps/mymap.heightmap
```

- The server reads the pair, verifies sha256 + metadata, and parses **before** serving. Any failure → `log.Fatalf` → **refuses to start** (no partial map, no degraded serving).
- Boot log line: `server: map active: <path>`.
- No `-map` flag → the embedded hills fixture boots (default map for dev).
- Config in docker-compose: mount the pair directory and pass `-map` as above (see `docs/DEPLOYMENT.md`).

### 8.3 Versioning

- **MUST**: the `version` field stays `1` — it is the *format* version, not a content version. Bumping it to `2` rejects the file.
- **SHOULD**: version *content* via the filename or the manifest pair (e.g., `world_v2.heightmap` + `world_v2.manifest`), and by committing both to source control together.
- Every map revision = a **new pair** committed/staged atomically. Never mix a new `.heightmap` with an old `.manifest`.

### 8.4 Common failure modes (all caught by the validator except the last two)

| # | Failure | Symptom | Fix |
|---|---------|---------|-----|
| 1 | Stale manifest (regenerated `.heightmap`, reused manifest) | Boot/validator: `sha256` mismatch | Regenerate the pair together; never reuse manifests |
| 2 | Uppercase sha256 (PowerShell `Get-FileHash`) | `sha256` mismatch | `.ToLower()` |
| 3 | Big-endian packing or wrong field order | `bad heightmap magic` / garbage header | Follow §3 byte-for-byte; use LE explicitly |
| 4 | Wrong body length (trailing bytes, floats written as double, extra newline) | `body length mismatch` / `truncated heightmap` | Write exactly `width×height` f32, nothing else |
| 5 | `NaN`/`Inf` sample (post-processing, division by zero) | `sample is not finite` | Clamp/fix pipeline; validate before transfer |
| 6 | Sample too tall (`|sample × y_scale| > 10000`) | `sample out of range` | Fix scale or y_scale |
| 7 | Off-by-one cell_size (`size.x/N` instead of `size.x/(N−1)`) | **Not caught by validator** — map silently 10% too small | Use `(N−1)`; verify world bounds in the valid summary |
| 8 | Z or X mirrored (terrain rotated 180°) | **Not caught by validator** — terrain backwards | Landmark check §4.4; keep rotation (0,0,0) |
| 9 | Origin at terrain center instead of corner | **Not caught by validator** — map shifted | `origin = terrain.position` (corner); landmark check |
| 10 | Missing `.manifest` | `read manifest: no such file` | Export both files |

### 8.5 Review checklist (for whoever merges/receives the handoff)

- [ ] Pair exists, same base name, generated in the same run.
- [ ] `heightmap-validate` exits `0`; the `valid:` summary matches the intended dims/origin/bounds.
- [ ] Manifest sha256 equals `sha256sum` of the binary (lowercase).
- [ ] Header metadata == manifest metadata (the loader enforces this — just confirm no surprise).
- [ ] Landmark documented: a known world coordinate → expected sample height.
- [ ] `./server -map <pair>` boots; boot log names the map; smoke spawn height is sane.
- [ ] Deployment path uses staging + re-validate + atomic rename (no silent overwrite).
- [ ] Rollback plan = previous pair (N−1 `.heightmap` + `.manifest` together) or revert commit (`docs/DEPLOYMENT.md` §8).

---

## 9. Explicitly Out of Scope

- **Runtime Unity exporter implementation in this repo** — this repo ships loader + sampler + validator only. The exporter lives in your Unity project; this sheet + `docs/HEIGHTMAP.md` are its spec.
- **Jump / gravity / character physics** — the server derives entity Y from the terrain at spawn and during movement; jump/gravity are client-side/gameplay concerns outside the heightmap contract.
- **Dynamic map edits at runtime** — the heightmap is a static grid loaded once at boot. Runtime terrain mutation is not supported; change the map and redeploy.
- **Collision meshes, textures, splat maps, details, LODs, physics** — never sent, never consumed.
- **Non-heightmap terrain data** (e.g., water level, walkable-area masks) — out of contract. Bake what gameplay needs into heights or propose a format change (version bump + this sheet updated together).