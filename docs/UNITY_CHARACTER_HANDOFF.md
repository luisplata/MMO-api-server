# UNITY_CHARACTER_HANDOFF.md — What the Unity Dev Must Do (Character-Management Flow)

> **To**: Unity developer (client).
> **What this is**: the practical, self-contained action sheet for the character-management flow (list / create / select a character before entering the world, and pick the visual prefab via `templateId`). The canonical wire contract lives in `docs/PROTOCOL.md`; this document is the action sheet on top of it.
> **Server truth**: verified against `proto/v1/world.proto` (contract, ids 13–18 + `EntityState.templateId`), `internal/session/handshake.go` (the `selecting` phase), `internal/server` (wiring). If this sheet and those files disagree, **the files win** — open an issue.
> **Note**: the terrain heightmap has its own handoff sheet — `docs/UNITY_HEIGHTMAP_HANDOFF.md`. Do not conflate the two: this sheet is about the *character* flow, not terrain.
> **Language note**: `MUST` = the server rejects/errors if violated. `SHOULD` = strongly recommended.

---

## 1. Executive Summary & Quick Delivery Path

In v2 the login no longer drops you straight into the world. After `AuthResponse` the session sits in a **`selecting`** phase where you must **list, create and select a character** over TCP *before* `EnterWorld`. Each character references a **template**; the server resolves its stats and exposes the `templateId` so you can pick the right visual model.

Quick path (no protoc needed — bindings are committed):

| # | Step | Result |
|---|------|--------|
| 1 | Refresh `World.cs` from the repo (`proto/v1/gen/csharp/World.cs`) | You have ids 13–18 + `EntityState.templateId` |
| 2 | After `AuthResponse`, show the character list (`ListCharacters`) | Account's characters, or empty |
| 3 | Let the player create a character (`CreateCharacter`) or pick one | You have a `Character { id, templateId, stats }` |
| 4 | Send `SelectCharacter` with that id | Server returns `spawnPos` + `templateId`; session → `entering` |
| 5 | Only then send `EnterWorld` | You get the real `WorldSnapshot` |
| 6 | Use `templateId` (from the response / snapshot / `SpawnEntity`) to pick the prefab | Correct model rendered |

---

## 2. The `selecting` Phase — What Changed

**Before (v1):** `AuthResponse` carried `spawnPos` and you went straight to `EnterWorld`.

**Now (v2):**

```
AuthResponse{ ok, playerId, udpToken }      // NO spawnPos — it's resolved later
        │  state: authenticating → selecting
        ▼
selecting:  ListCharacters → CharacterList
            CreateCharacter → CreateCharacterResponse   (templateId + frozen stats)
            SelectCharacter → SelectCharacterResponse   (spawnPos + templateId)
        │  state: selecting → entering   (only after a valid Select)
        ▼
EnterWorld → WorldSnapshot (empty ack) → WorldSnapshot (real) → in-world
```

Rules you MUST follow:

- **`EnterWorld` before a `SelectCharacter` is rejected** — the session closes (protocol error). Do not send it early.
- `ListCharacters` / `CreateCharacter` / `SelectCharacter` are **only valid in `selecting`**. Sending them in any other state closes the session.
- **The world entity id is the CHARACTER id**, not the account id. Use `character.id` as the entity id for position/velocity/yaw.
- `AuthResponse` no longer carries `spawnPos`. The spawn comes from `SelectCharacterResponse.spawnPos` (its `.y` is the terrain-derived height, authoritative).

---

## 3. The Messages You Need (ids 13–18)

All over TCP (reliable channel). The `Character` message is:

```
Character { id, accountId, name, templateId, stats { hp, speed, atk, def }, createdAt }
```

| Id | Message | Direction | Purpose |
|----|---------|-----------|---------|
| 13 | `ListCharacters` | C → S | Ask for the account's characters. No fields. |
| 14 | `CharacterList` | S → C | `characters[]`. Empty when the account has none (not an error). |
| 15 | `CreateCharacter` | C → S | `templateId`, `name` (trimmed 3–16, `[a-zA-Z0-9_]`). |
| 16 | `CreateCharacterResponse` | S → C | `ok`, `character`, `errorMessage`. `ok=true` → `character` has the frozen stats + `templateId`. |
| 17 | `SelectCharacter` | C → S | `characterId`. Activates that character as your world entity. |
| 18 | `SelectCharacterResponse` | S → C | `ok`, `character`, `spawnPos`, `errorMessage`. `ok=true` → session is ready to `EnterWorld`. |

`EntityState` now carries `templateId` (field 5): it appears in `WorldSnapshot.entities`, `Snapshot.entities` and `SpawnEntity.state`. Use it to pick the prefab.

---

## 4. The Template Catalog

Templates are **server-side** (`data/templates.json`). The server exposes each character's `templateId`; the visual mapping lives in your client. The current seed catalog:

| `templateId` | Name (server) | baseStats (hp / speed / atk / def) | visualHint |
|--------------|---------------|-------------------------------------|------------|
| `warrior` | Guerrero | 120 / 3 / 10 / 8 | `warrior_model` |
| `mage` | Mago | 80 / 4 / 14 / 4 | `mage_model` |
| `ranger` | Explorador | 90 / 5 / 9 / 5 | `ranger_model` |

- **`templateId` is the contract.** Map it to a prefab in your client. If you don't recognize an id, fall back to a default prefab (do not crash).
- **Stats are frozen at creation.** `Character.stats` is a snapshot of the template's base stats — do not re-derive them client-side; just read them.
- The catalog is file-based today; a future DB swap changes nothing for you — the wire and `templateId` stay identical.

---

## 5. Error Handling (they do NOT close the session)

The character-management rejections return `ok:false` + `errorMessage` and the session **stays open** in `selecting` — you can correct and retry:

| Condition | Response | What to do |
|-----------|----------|------------|
| Bad/invalid `templateId` | `CreateCharacterResponse{ ok:false, "template not found" }` | Show a template error; don't proceed |
| Invalid name (empty / length / charset) | `CreateCharacterResponse{ ok:false, <reason> }` | Show a name validation error |
| Duplicate name (per account) | `CreateCharacterResponse{ ok:false, "name already used in account" }` | Show "name already taken" |
| Character not found | `SelectCharacterResponse{ ok:false, "character not found" }` | Refresh the list |
| Character not owned by you | `SelectCharacterResponse{ ok:false, "character not owned by account" }` | Refresh the list (should not happen) |

---

## 6. Unity Implementation Checklist

- [ ] **`World.cs` v2 (lockstep)**: replace the client `World.cs` with `proto/v1/gen/csharp/World.cs` **in the same window** as the server v2. It contains `Vec3`, ids 13–18 and `EntityState.templateId`.
- [ ] **No protoc**: the bindings are committed. Never run protoc on the client side.
- [ ] **`selecting` UI**: after `AuthResponse`, go to a character-selection screen (list + create), not straight to the world.
- [ ] **`ListCharacters`** on entering the selection screen; render `Character.name` (+ `templateId`/stats as needed).
- [ ] **`CreateCharacter`**: send `templateId` + `name`; handle `ok:false` errors (see §5) without closing.
- [ ] **`SelectCharacter`**: send the chosen `characterId`; on `ok:true`, store `spawnPos` and `templateId`.
- [ ] **`EnterWorld` only after a successful `SelectCharacter`** — never before.
- [ ] **Prefab by `templateId`**: use it from `SelectCharacterResponse.character.templateId`, `WorldSnapshot.entities[i].templateId` and `SpawnEntity.state.templateId` to instantiate the visual. Fallback prefab for unknown ids.
- [ ] **Treat `spawnPos.y` / `pos.y` as authoritative terrain height** (never generate or correct it).
- [ ] **AOT-safe**: only the generated `Mmo.V1` classes; no `DynamicMessage` / `Any` / `JsonParser`.
- [ ] **Test locally**: `go run ./cmd/server` or `docker compose up`.

---

## 7. Explicitly Out of Scope

- **UI/UX of the character screen** — this sheet only specifies the wire + prefab selection. The look/flow is yours.
- **Editing/deleting characters** — not in the contract (v2).
- **Server-side template authoring** — templates live in `data/templates.json` (server team).
- **Terrain heightmap** — see `docs/UNITY_HEIGHTMAP_HANDOFF.md`.
