# Milestone 05 — Admin scripting

## Goal

Admins can write and store Lua scripts that create rooms, spawn NPCs, register NPC tools, wire ACLs, and otherwise build the world from inside the game. The Lua VM has full access to the admin world API. The tool registry exists and admins can invoke tools by hand to validate them; NPCs invoking tools autonomously is wired in M7.

## Dependencies

- M2 (world layer): everything operates on rooms and objects.
- Can run in parallel with M4 (memory) — separate package, no shared mutable state.

## Scope

- `internal/script/lua` package wrapping `gopher-lua`. VM pool (warm VMs to amortize startup; pool size configurable, default 4).
- Admin world API surface exposed to Lua: rooms (CRUD), exits, objects, NPCs (create, configure, set persona), MOTD, system broadcast, kick/disconnect, account access-level change.
- Tool registry: admin scripts call `tool.register(name, fn)` to register a Lua function. NPC config references tools by name. M5 ships the registry + a manual-invocation command; autonomous tool calls land in M7.
- Persistent script storage: `scripts` table. In-world editor command `@edit <script>` (line-based; modeled on `ed`/`MUSH @decompile`).
- Admin command surface: `@create-room`, `@dig` (create room + exit pair), `@create-npc`, `@persona`, `@script`, `@edit`, `@run`, `@tools`, `@invoke`, `@reload-scripts`, `@boot` (force disconnect a session).
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

All API functions raise Lua errors on failure with a stable message format `"wintermute: <code>: <details>"`.

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

## Implementation tasks

1. Add migrations `0007_scripts.sql` and `0008_permissions.sql`.
2. Implement `internal/world/api` — pure Go layer over `internal/world` and `internal/npc`. Stable error codes. No Lua imports.
3. Implement `internal/script/lua` with the VM pool.
4. Bind the world API into the Lua state as `wintermute.*`. Each binding is a small `func(L *lua.LState) int` adapter.
5. Implement the tool registry types and the binding for `wintermute.tool.register/unregister/list`.
6. Implement the `@`-commands in the M2 command parser, restricted by access level.
7. Implement `@edit` line editor as a session sub-mode (replace the normal line handler while editing; restore on `q`).
8. On startup, after world load, run all `init.*` scripts in slug order.
9. Add structured slog fields for script execution: `script`, `tool`, duration, error.
10. Unit tests:
    - World API surface (create/move/delete a room from Go directly).
    - Lua binding round-trips (create a room from Lua, fetch from Go, verify).
    - Tool registration with schema validation.
11. Integration test: log in as admin, create a room via `@create-room`, `@dig` north to it, walk there, see it.
12. Integration test: write a script via `@edit` that creates an NPC; `@run` it; the NPC appears and responds in the next M3-style conversation.
13. Manual exercise: replace one room from the M2 seed fixture with a script equivalent; verify boot still works (parallel paths).

## Testing

- `go test ./internal/script/... ./internal/world/api/...`
- Integration scenarios driven through the M1 telnet integration harness.
- Manual: live edit a room description and re-run the script.

## Acceptance criteria

1. An admin can `@create-room`, `@dig`, `@create-npc`, and `@persona` purely from inside the game.
2. The created NPC behaves like the M3 bartender — including using the M4 memory plumbing.
3. Scripts persist across restart. `init.*` scripts run automatically at boot.
4. `@tools` lists at least one tool registered by a startup script. `@invoke <tool> '{"...":"..."}'` runs it and prints its return.
5. A non-admin attempting an `@`-command sees a clear refusal and the action is not performed.
6. The VM pool reuses VMs (verified via a debug stat exposed on an admin command).
7. All new files carry the MIT header.

## Risks & open questions

- **Lua error surface**: gopher-lua returns Go errors and Lua errors somewhat distinctly. Wrap both in a single `script.Error` type with a `Source` field (`go`/`lua`) and a stable message format.
- **VM pool state leakage**: even admin code can leave globals lying around. Reset the `wintermute.*` table to a known shape on `Put` (re-bind from scratch), and clear non-stdlib globals. This is cheap and avoids debugging weirdness later.
- **Tool argument schemas**: M3 defined `ToolDef.Schema` as `map[string]any` (JSON schema). Decide whether to validate the args server-side before invoking the Lua function — yes, with a tiny in-house validator (just type and required-fields), to avoid pulling in a full JSON-Schema dep.
- **Line editor UX**: line editors are widely hated but cheap. The MUSH community is used to `@edit`. Provide a `paste` mode that accepts a heredoc terminated by a `.` on its own line, which is what most actual users will use.
- **Migration ordering**: M4's `0006_memory.sql` came before M5's `0007_scripts.sql`. Migrations are forward-only and numbered; running M5 against a DB built up to M2 must apply 0003 through 0008 cleanly. The test harness should cover the "fresh DB to current" path.
- **The seed world transition**: keeping both the SQL seed fixture and the Lua `init.*` scripts is intentional duplication for M5. Pick one in M7 (or whenever a milestone has spare time) and remove the other. Don't do it in M5.
