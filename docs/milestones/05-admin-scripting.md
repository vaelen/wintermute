# Milestone 05 — Admin scripting

## Goal

Admins can write and store Lua scripts that create rooms, spawn NPCs, register NPC tools, wire ACLs, and otherwise build the world from inside the game. The Lua VM has full access to the admin world API. The tool registry exists and admins can invoke tools by hand to validate them; NPCs invoking tools autonomously is wired in M7.

## Dependencies

- M2 (world layer): everything operates on rooms and objects.
- Can run in parallel with M4 (memory) — separate package, no shared mutable state.

## Scope

- `internal/script/lua` package wrapping `gopher-lua`. VM pool (warm VMs to amortize startup; pool size configurable, default 4).
- Admin world API surface exposed to Lua: rooms (CRUD), exits, objects, doors, NPCs (create, configure, set persona), MOTD, system broadcast, kick/disconnect, account access-level change.
- Doors as first-class objects: the bare `exits` table from M2 is promoted to a `kind='door'` flavor of `objects` with per-door direction, destination, and configurable leave/arrive message templates (e.g. a "ladder" door rendering `"X climbs up the ladder."` instead of the default `"X leaves up."`). Doors are created and edited with the same tooling as other objects. Lockability, examinability, and door-scoped scripts are stubbed out in the schema and API but not exercised until later milestones.
- Tool registry: admin scripts call `tool.register(name, fn)` to register a Lua function. NPC config references tools by name. M5 ships the registry + a manual-invocation command; autonomous tool calls land in M7.
- Persistent script storage: `scripts` table. In-world editor command `@edit <script>` (line-based; modeled on `ed`/`MUSH @decompile`).
- Admin command surface: `@create-room`, `@dig` (create room + door pair), `@create-door`, `@door-msg` (set a door's leave/arrive templates), `@create-npc`, `@persona`, `@script`, `@edit`, `@run`, `@tools`, `@invoke`, `@reload-scripts`, `@boot` (force disconnect a session).
- ACL plumbing groundwork: `permissions` is a column on `rooms`/`objects` capturing the owner-set permission bitmask. The full ACL table for delegated permissions lands in M8 (player tier).

## Out of scope

- Player-facing scripting (M8).
- Lua sandboxing (M8).
- NPCs calling tools (M7).
- A real text editor (line editor is fine for M5; a screen editor is a much later concern).

## Architecture

### Packages introduced

- `internal/script/lua` — gopher-lua glue. VM pool, world API binding, error handling.
- `internal/world/api` — the Go-side world API consumed by Lua. Stable, intentionally narrow. Reused by M8's sandbox layer.

### VM pool

```go
// internal/script/lua/pool.go
type Pool struct {
    free chan *lua.LState
    new  func() *lua.LState
    size int
}

func (p *Pool) Get() *lua.LState
func (p *Pool) Put(L *lua.LState)
```

Each VM is created with the full admin API pre-loaded as a `wintermute` global table. VMs are pooled and reused; we explicitly *do not* run hostile code in M5, so we don't need to reset VM state aggressively between uses. (M8 will introduce a separate pool with stricter reset rules for player code.)

### World API surface (Lua)

Roughly:

```lua
-- Rooms
local room = wintermute.room.create({slug="bar", name="The Sprawl Bar", description="..."})
wintermute.room.set_description(room, "Smoky and dim.")
wintermute.room.delete(room)
local r = wintermute.room.find("bar")
wintermute.exit.create(from_room, "n", to_room)
wintermute.exit.delete(from_room, "n")

-- Objects
local obj = wintermute.object.create({slug="keycard", name="ICE keycard", kind="item"})
wintermute.object.move(obj, room)              -- to a room
wintermute.object.move(obj, holder_obj)        -- into another object's inventory
wintermute.object.delete(obj)

-- Doors (a kind='door' object that lives in a room and links it to another)
local ladder = wintermute.door.create({
    slug        = "lobby-ladder-up",
    name        = "a rusted ladder",
    from        = lobby_room,
    direction   = "up",
    to          = roof_room,
    leave_msg   = "{actor} climbs up the ladder.",   -- broadcast in `from`
    arrive_msg  = "{actor} climbs up from below.",   -- broadcast in `to`
})
wintermute.door.set_messages(ladder, { leave_msg = "...", arrive_msg = "..." })
wintermute.door.delete(ladder)

-- NPCs (extends objects)
local npc = wintermute.npc.create({slug="bartender", name="the bartender", room=room,
                                   persona="...", backend="ollama",
                                   model="llama3.2:3b", gate_model="llama3.2:1b"})
wintermute.npc.set_persona(npc, "...")
wintermute.npc.set_tools(npc, {"sell_drink","tell_story"})  -- list of registered tool names

-- Accounts
wintermute.account.set_level(account, "builder")
wintermute.account.boot(account, "Maintenance in 5 minutes")  -- forced disconnect

-- Broadcast / system
wintermute.system.broadcast("The server will restart in 5 minutes.")
wintermute.system.motd("Welcome to the Sprawl.")
wintermute.system.log("anything")             -- writes to slog + world_log

-- Tools (registered Lua functions exposed to NPCs in M7)
wintermute.tool.register("sell_drink", function(args)
    -- args is a Lua table with NPC and player handles, plus model arguments
    local price = args.price or 5
    -- ... mutate world ...
    return { ok = true, message = "Pours a synthahol." }
end, {
    description = "Sell a drink to a player",
    schema = { type="object", properties={ drink={type="string"}, price={type="number"} } },
})
wintermute.tool.unregister("sell_drink")
local list = wintermute.tool.list()
```

All API functions raise Lua errors on failure with a stable message format `"wintermute: <code>: <details>"`. In particular, the strict `create` functions raise `wintermute: duplicate_slug: <slug>` when the slug is already taken; use the `ensure` siblings below for idempotent creation.

### Idempotent creation for init scripts

Init scripts re-run on every boot, so the strict slug-uniqueness constraint would make `wintermute.room.create({slug="bar", ...})` fail on the second startup. To keep init scripts terse and re-runnable, every creator has an `ensure` sibling:

```lua
local room, created = wintermute.room.ensure({
    slug = "bar", name = "The Sprawl Bar", description = "...",
})

local obj = wintermute.object.ensure({
    slug = "keycard", name = "ICE keycard", kind = "item",
})

local door = wintermute.door.ensure({
    slug      = "bar-roof-up",
    from      = bar, direction = "up", to = roof,
    leave_msg = "{actor} climbs up the ladder.",
})

local npc = wintermute.npc.ensure({
    slug    = "bartender", name = "the bartender",
    persona = "...", backend = "ollama", model = "llama3.2:3b",
})
```

`ensure` performs the create on first run; on subsequent runs with the same slug it updates the descriptive fields supplied in the table (name, description, persona, model, door message templates, etc.) and returns the existing handle plus a `created` boolean.

`ensure` deliberately does *not* touch live world state beyond those descriptive fields: it will not move objects to a different room, change ownership, or evict players. Re-locations stay behind explicit setters (`object.move`, `account.set_level`) so a reboot doesn't clobber runtime activity.

The one-off admin commands (`@create-room`, `@create-npc`, `@create-door`) use the strict `create` form, so a typo at the prompt surfaces as `wintermute: duplicate_slug: <slug>` rather than silently mutating an existing entity.

### Doors as first-class objects

In M2, an exit is a bare `(from_room, direction, to_room)` row in the `exits` table. M5 promotes exits to objects of `kind='door'`:

- The `objects.kind` CHECK constraint gains `'door'`.
- A new `doors` extension table holds the door-specific fields, joined 1:1 to `objects.id`: source room, direction, destination room, leave/arrive message templates, and reserved nullable columns for the as-yet-unbuilt features — `lock_state`, `key_object_id`, `script_slug`.
- The legacy `exits` table is migrated into `doors`: each existing row produces one door with default message templates (`"{actor} leaves {direction}."` / `"{actor} arrives."`, matching M2's current strings). The `exits` table is then dropped.
- `world.Move` resolves a direction by looking up the door object instead of the exit row, then uses the door's templates for the broadcasts. If a door has no override, the defaults are substituted in.

Templates support a minimal placeholder set: `{actor}` (player or NPC display name) and `{direction}` (the door's `direction` field, useful for the generic leave message). The substitution happens in `internal/world/render`, alongside the other broadcast formatting.

Doors are created and managed through `wintermute.door.*` in Lua (see above) and via the new `@create-door` and `@door-msg` admin commands. They show up in the room's object listing only if explicitly described — the default render treats them as exits (listed under "Obvious exits:" the same way today's exit slugs are), to avoid duplicating every door in both the exits line and the objects line.

The data model has room for future affordances (lockability, `@examine`-able descriptions, door-scoped Lua scripts) without further schema changes; only the surface API grows in later milestones.

### Persistent scripts

```sql
CREATE TABLE scripts (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    owner_id     INTEGER NOT NULL REFERENCES accounts(id),
    source       TEXT NOT NULL,
    updated_at   INTEGER NOT NULL,
    -- M8 adds: scope ('admin'|'player'), allowed_caps TEXT
);
```

On startup, every script with `slug` starting with `init.` runs in alphabetical order (`init.0001-world`, `init.0010-npcs`, etc.). Other scripts run on demand via `@run <slug>`.

The seed world's SQL fixture from M2 can be replaced incrementally with Lua init scripts, but **don't** rip out the SQL seed in this milestone — keep both paths to avoid bootstrap problems.

### Tool registry

Registry is a process-wide map `name → ToolDef + Lua callback closure`. It's not persisted; scripts re-register tools on every startup (admin scripts run at boot).

```go
// internal/script/lua/tools.go
type ToolEntry struct {
    Name       string
    Def        llm.ToolDef
    Invoke     func(ctx, args map[string]any) (map[string]any, error)  // wraps the Lua callback
}

type ToolRegistry struct { /* map + RWMutex */ }
```

Used by:
- `@invoke <tool> {json}` admin command (manual exercise).
- M7's autonomous loop.

### Command additions

- `@create-room <slug> "<name>"` — convenience for `wintermute.room.create`.
- `@dig <direction> <room-slug>` — creates a new room and a two-way exit pair from the current room.
- `@create-npc <slug> "<name>" "<persona>"` — defaults backend to `"ollama"`, models to config defaults.
- `@persona <npc> "<new persona>"`.
- `@script <slug>` — show script source.
- `@edit <slug>` — line-based editor (commands `i`, `a`, `d`, `p`, `r`, `w`, `q`); minimal but workable.
- `@run <slug>` — execute (re-execute) a script.
- `@tools` — list registered tools.
- `@invoke <tool> <json>` — admin executes a tool with a JSON argument string.
- `@reload-scripts` — re-run all `init.*` scripts.
- `@boot <player>` — disconnect a session.

All `@`-prefixed commands check `account.access_level == 'admin'` (or `'builder'` for build-only commands) and refuse for regular players.

## Schema changes

`internal/store/migrations/0007_scripts.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE scripts (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    owner_id     INTEGER NOT NULL REFERENCES accounts(id),
    source       TEXT NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX idx_scripts_owner ON scripts(owner_id);
```

`internal/store/migrations/0008_permissions.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

ALTER TABLE rooms   ADD COLUMN permissions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE objects ADD COLUMN permissions INTEGER NOT NULL DEFAULT 0;
```

Bit layout TBD; reserve enough bits for read/write/script/delegate. Document in `world/api`.

`internal/store/migrations/0009_doors.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- Doors are objects of kind='door' with a 1:1 extension row carrying
-- direction, destination, message templates, and stubs for later features.

-- SQLite can't easily ALTER a CHECK constraint, so we rewrite objects.
-- The migration runs inside a transaction; foreign keys are deferred.
CREATE TABLE objects_new (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    short_desc   TEXT NOT NULL DEFAULT '',
    long_desc    TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL CHECK (kind IN ('item','player','npc','door')),
    owner_id     INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    permissions  INTEGER NOT NULL DEFAULT 0
);
INSERT INTO objects_new SELECT * FROM objects;
DROP TABLE objects;
ALTER TABLE objects_new RENAME TO objects;
CREATE INDEX idx_objects_account ON objects(account_id);

CREATE TABLE doors (
    object_id     INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    from_room     INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    direction     TEXT    NOT NULL,
    to_room       INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    leave_msg     TEXT    NOT NULL DEFAULT '{actor} leaves {direction}.',
    arrive_msg    TEXT    NOT NULL DEFAULT '{actor} arrives.',
    -- Stubs for later milestones; nullable, ignored by M5 logic.
    lock_state    TEXT,
    key_object_id INTEGER REFERENCES objects(id) ON DELETE SET NULL,
    script_slug   TEXT    REFERENCES scripts(slug) ON DELETE SET NULL,
    UNIQUE(from_room, direction)
);
CREATE INDEX idx_doors_from ON doors(from_room);
CREATE INDEX idx_doors_to   ON doors(to_room);

-- Migrate existing exits into doors. Each exit becomes a door object
-- whose slug is derived from the rooms and direction.
INSERT INTO objects (slug, name, kind, owner_id, permissions)
    SELECT 'door-' || from_room || '-' || direction || '-' || to_room,
           direction, 'door', NULL, 0
      FROM exits;

INSERT INTO doors (object_id, from_room, direction, to_room)
    SELECT o.id, e.from_room, e.direction, e.to_room
      FROM exits e
      JOIN objects o
        ON o.slug = 'door-' || e.from_room || '-' || e.direction || '-' || e.to_room;

DROP TABLE exits;
```

The migration also accounts for the M2 → M5 ordering: a fresh DB built up through M5 ends with `doors` and no `exits` table. The M2 seed fixture continues to insert into `exits`; the seed runs *before* migration 0009, so the rows are still picked up by the migration and converted. M5's Lua `init.*` scripts can target `doors` directly via `wintermute.door.create`.

## Implementation tasks

1. Add migrations `0007_scripts.sql`, `0008_permissions.sql`, and `0009_doors.sql`.
2. Implement `internal/world/api` — pure Go layer over `internal/world` and `internal/npc`. Stable error codes. No Lua imports.
3. Promote exits to doors in `internal/world`: add a `Door` type carrying direction, source/destination rooms, and message templates; load `doors` into the world cache at startup; replace `Room.Exits map[string]RoomID` with a direction → door-id lookup. Update `Move` to resolve the door, run template substitution (`{actor}`, `{direction}`) via `internal/world/render`, and broadcast the resulting strings.
4. Implement `internal/script/lua` with the VM pool.
5. Bind the world API into the Lua state as `wintermute.*`, including `wintermute.door.create/set_messages/delete` and the `ensure` siblings for `room`, `object`, `door`, and `npc`. Each binding is a small `func(L *lua.LState) int` adapter. `ensure` is implemented in `internal/world/api` as a single transaction (lookup-by-slug → insert or descriptive-field update), not as a Lua-side `find`+`create` composition, so it remains race-free.
6. Implement the tool registry types and the binding for `wintermute.tool.register/unregister/list`.
7. Implement the `@`-commands in the M2 command parser, restricted by access level. Include `@create-door <slug> <dir> <to-room>` and `@door-msg <slug> leave|arrive "<template>"`. `@dig` is rewritten to create a door pair (one in each direction) rather than two exit rows.
8. Implement `@edit` line editor as a session sub-mode (replace the normal line handler while editing; restore on `q`).
9. On startup, after world load, run all `init.*` scripts in slug order.
10. Add structured slog fields for script execution: `script`, `tool`, duration, error.
11. Unit tests:
    - World API surface (create/move/delete a room from Go directly).
    - Lua binding round-trips (create a room from Lua, fetch from Go, verify).
    - Tool registration with schema validation.
    - `create` vs. `ensure` semantics: `room.create({slug="x", ...})` on an existing slug raises `wintermute: duplicate_slug: x`; `room.ensure({slug="x", name="new"})` called twice succeeds, returns the same id both times, returns `created=true` then `created=false`, and the second call propagates the updated `name` while leaving non-supplied fields untouched. Equivalent coverage for `object`, `door`, and `npc`.
    - Door template substitution: `{actor}` and `{direction}` resolve correctly; missing placeholders are left as literal text; default templates match the M2 strings.
    - Migration `0009_doors.sql` against a database populated by the M2 seed: every former `exits` row appears as a `doors` row with default templates and the `exits` table is gone.
12. Integration test: log in as admin, create a room via `@create-room`, `@dig` north to it, walk there, see it.
13. Integration test: create a custom-template door (ladder) via `@create-door` + `@door-msg`, walk through it, verify both rooms see the configured messages instead of the defaults.
14. Integration test: write a script via `@edit` that creates an NPC; `@run` it; the NPC appears and responds in the next M3-style conversation.
15. Manual exercise: replace one room from the M2 seed fixture with a script equivalent; verify boot still works (parallel paths).

## Testing

- `go test ./internal/script/... ./internal/world/api/...`
- Integration scenarios driven through the M1 telnet integration harness.
- Manual: live edit a room description and re-run the script.

## Acceptance criteria

1. An admin can `@create-room`, `@dig`, `@create-npc`, and `@persona` purely from inside the game.
2. The created NPC behaves like the M3 bartender — including using the M4 memory plumbing.
3. After migrating an existing M2 database through 0009, every former exit is reachable as a door and walking through it produces the same default broadcast strings as before. The `exits` table no longer exists.
4. An admin can create a door whose `leave_msg` is `"{actor} climbs up the ladder."`; a player walking that direction triggers exactly that broadcast in the source room (with the player's name substituted), and the matching `arrive_msg` in the destination room.
5. Scripts persist across restart. `init.*` scripts run automatically at boot.
6. Re-running an `init.*` script (manually via `@reload-scripts` or implicitly on restart) is a no-op when it uses `ensure`: no duplicate rooms/objects/NPCs are created and no errors are raised, and edits to descriptive fields in the script propagate to the existing entities. The strict `create` form continues to raise `wintermute: duplicate_slug: <slug>` on conflict.
7. `@tools` lists at least one tool registered by a startup script. `@invoke <tool> '{"...":"..."}'` runs it and prints its return.
8. A non-admin attempting an `@`-command sees a clear refusal and the action is not performed.
9. The VM pool reuses VMs (verified via a debug stat exposed on an admin command).
10. All new files carry the MIT header.

## Risks & open questions

- **Lua error surface**: gopher-lua returns Go errors and Lua errors somewhat distinctly. Wrap both in a single `script.Error` type with a `Source` field (`go`/`lua`) and a stable message format.
- **VM pool state leakage**: even admin code can leave globals lying around. Reset the `wintermute.*` table to a known shape on `Put` (re-bind from scratch), and clear non-stdlib globals. This is cheap and avoids debugging weirdness later.
- **Tool argument schemas**: M3 defined `ToolDef.Schema` as `map[string]any` (JSON schema). Decide whether to validate the args server-side before invoking the Lua function — yes, with a tiny in-house validator (just type and required-fields), to avoid pulling in a full JSON-Schema dep.
- **Line editor UX**: line editors are widely hated but cheap. The MUSH community is used to `@edit`. Provide a `paste` mode that accepts a heredoc terminated by a `.` on its own line, which is what most actual users will use.
- **Migration ordering**: M4's `0006_memory.sql` came before M5's `0007_scripts.sql`. Migrations are forward-only and numbered; running M5 against a DB built up to M2 must apply 0003 through 0008 cleanly. The test harness should cover the "fresh DB to current" path.
- **The seed world transition**: keeping both the SQL seed fixture and the Lua `init.*` scripts is intentional duplication for M5. Pick one in M7 (or whenever a milestone has spare time) and remove the other. Don't do it in M5.
