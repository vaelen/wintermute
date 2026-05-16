# Milestone 08 — Player-tier scripting

## Goal

Players can attach Lua scripts to objects and rooms they own (or have been granted scripting permission on). Player scripts run in a sandboxed Lua VM with CPU/memory budgets, a stripped standard library, and an ACL-restricted view of the world API. Buggy or malicious player code cannot crash the server, exhaust resources, or affect other players' content.

## Dependencies

- M5 (admin scripting): the Lua VM, world API, and tool registry are reused; the sandbox is a different *configuration* of the same machinery.

## Scope

- `internal/script/sandbox`: a stripped, instrumented gopher-lua VM configuration. Separate pool from the admin pool.
- Standard library: remove `os`, `io`, `debug`, `package`, raw `loadstring`/`load`/`loadfile`/`dofile`. Provide bounded replacements for risky stdlib pieces.
- CPU budget: instruction-counter via `lua.SetHook(MaskCount, …)` — abort with a clear error when exceeded.
- Memory budget: track allocation counts (gopher-lua exposes hooks); abort when exceeded.
- ACL-restricted world API: a separate adapter that wraps `internal/world/api` and enforces, for each call, that the caller has the required permission on the target.
- ACL data model: `acls` table for delegated permissions on rooms, objects, files, boards.
- Player command surface: `@script <object>` to attach, `@unscript`, `@scripts` to list, `@grant`/`@revoke` to delegate permissions.
- Player-authored event handlers: scripts can attach to events on owned objects/rooms — e.g. "when someone enters my room, broadcast X." Limited event surface from M7's `events.Bus`.

## Out of scope

- A MUSH-flavored softcode DSL on top of Lua. Tempting but defer; can be added as a thin pre-parser later.
- Scripted NPC personae authored by players. The line between "scripted object" and "NPC with persona" is meaningful — only admins create NPCs in M8.
- Code editor improvements. The M5 `@edit` is the editor.

## Architecture

### Packages introduced

- `internal/script/sandbox` — sandboxed VM configuration and helpers.
- `internal/world/api/scoped` — an `internal/world/api` wrapper that performs ACL checks before delegating.

### Sandboxed VM creation

```go
// internal/script/sandbox/vm.go
type Config struct {
    InstructionBudget int       // hook fires every N instructions; abort when reached
    AllocBudget       int64     // bytes
    WallClock         time.Duration
}

func NewVM(cfg Config, owner *auth.Account, api *scoped.API) *lua.LState
```

VM setup steps:
1. `lua.NewState(lua.Options{SkipOpenLibs: true})` — opt-in stdlib only.
2. Open whitelisted libraries: `base` (minus a few names), `string`, `table`, `math`. **Skip** `os`, `io`, `debug`, `package`, `channel`.
3. From the `base` lib, set to `nil`: `loadstring`, `load`, `loadfile`, `dofile`, `getfenv`, `setfenv`, `rawget`, `rawset` (debatable — leave on for now), `collectgarbage`, `print` (replaced with sandbox-safe variant that writes to the player's session).
4. Replace `string.rep` with a wrapper that caps the repeat count.
5. Replace `string.format` and `string.gsub` with wrappers that cap output size.
6. Install instruction-count hook (`SetHook(MaskCount, count=10000)`); when fired, decrement remaining budget and abort if exhausted.
7. Bind a `wintermute` table backed by the *scoped* API.
8. Run the player's code with a wall-clock context (`lua.LState.SetContext(ctx)`).

### Scoped world API

`internal/world/api/scoped` wraps each admin API call with an ACL check:

```go
func (s *API) RoomSetDescription(ctx, account *auth.Account, room world.RoomID, desc string) error {
    if !s.HasPerm(account, "room", int64(room), PermWrite) {
        return ErrForbidden
    }
    return s.inner.RoomSetDescription(ctx, room, desc)
}
```

Surface available to players (subset of admin API):

- Read: own and accessible rooms/objects/files (full view of state).
- Write: own rooms (description, name), own objects (description, name, location *within owned space*).
- Event handlers: register handlers on owned objects/rooms.
- Broadcast: `wintermute.room.tell(text)` — sends text into the room the scripted object is in; rate-limited.
- No NPC create/edit, no tool registration, no system broadcast, no account management.

### Event handler registration

```lua
-- Inside a player script attached to a room
wintermute.on_arrive(function(who)
    wintermute.room.tell(who.name .. " walks in, looking dazed.")
end)
wintermute.on_say(function(speaker, text)
    if text:find("password") then
        wintermute.room.tell("(The walls listen quietly.)")
    end
end)
```

- Handlers are stored per-script and re-registered on every script execution.
- A handler invocation is itself bounded: each handler call gets a fresh, fast budget (e.g. 50k instructions, 100 ms wall-clock).
- An exception in a handler is logged but doesn't disable the script — until repeated failures (e.g. 5 consecutive aborts) trigger an automatic "the room mutters in static" and the script is suspended until edited.

### ACLs

```sql
CREATE TABLE acls (
    id           INTEGER PRIMARY KEY,
    target_type  TEXT NOT NULL,            -- 'room','object','file','board'
    target_id    INTEGER NOT NULL,
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    perms        INTEGER NOT NULL,         -- bitmask
    granted_by   INTEGER REFERENCES accounts(id),
    granted_at   INTEGER NOT NULL,
    UNIQUE(target_type, target_id, account_id)
);

CREATE INDEX idx_acls_target ON acls(target_type, target_id);
CREATE INDEX idx_acls_account ON acls(account_id);
```

Permission bits (initial):

```go
const (
    PermRead     = 1 << iota   // see in look/inventory
    PermWrite                  // edit name/description
    PermScript                 // attach a script
    PermDelegate               // grant permissions to others
)
```

Owners implicitly have all bits. Admins implicitly have all bits on everything.

### Player commands

- `@scripts` — list player's scripts.
- `@script <object>` — show the script attached to the object.
- `@edit-script <object>` — paste mode to set the script (reuses M5's paste helper).
- `@unscript <object>` — remove the attached script.
- `@grant <player> <permission> <target>` — e.g. `@grant alice script room:my-bar`.
- `@revoke <player> <permission> <target>`.
- `@perms <target>` — show ACLs.
- `@suspended` — list suspended scripts (autoflagged by repeated failures).
- `@resume <object>` — resume a suspended script (re-runs it; if it aborts again, stays suspended).

## Schema changes

`internal/store/migrations/0016_acls.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE acls (
    id           INTEGER PRIMARY KEY,
    target_type  TEXT NOT NULL,
    target_id    INTEGER NOT NULL,
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    perms        INTEGER NOT NULL,
    granted_by   INTEGER REFERENCES accounts(id),
    granted_at   INTEGER NOT NULL,
    UNIQUE(target_type, target_id, account_id)
);

CREATE INDEX idx_acls_target  ON acls(target_type, target_id);
CREATE INDEX idx_acls_account ON acls(account_id);
```

`internal/store/migrations/0017_player_scripts.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

ALTER TABLE scripts ADD COLUMN scope        TEXT NOT NULL DEFAULT 'admin'
                                                 CHECK (scope IN ('admin','player'));
ALTER TABLE scripts ADD COLUMN attached_to  INTEGER REFERENCES objects(id) ON DELETE CASCADE;
ALTER TABLE scripts ADD COLUMN suspended    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE scripts ADD COLUMN fail_streak  INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_scripts_attached ON scripts(attached_to);
```

## Implementation tasks

1. Add migrations 0014 and 0015.
2. Implement `internal/world/api/scoped`: every admin API method gets a permission-check wrapper. Express the permission requirements as a table for clarity.
3. Implement `internal/script/sandbox`: VM creation, stdlib stripping, instruction/memory budgets.
4. Implement the `wintermute` table for player scope — narrower than admin scope.
5. Implement event handler registration and dispatch for player scripts (subscribe to `events.Bus` on attached room or the room containing the attached object).
6. Implement the player commands `@script`, `@edit-script`, `@unscript`, `@scripts`, `@grant`, `@revoke`, `@perms`, `@suspended`, `@resume`.
7. Implement script suspension on repeated failures (configurable threshold, default 5).
8. Update M5's startup script runner to skip `scope='player'` scripts at boot — they only run reactively via event handlers.
9. Unit tests:
    - Stdlib stripping (an `os.execute` script aborts with a clear error).
    - Instruction-budget abort.
    - Memory-budget abort.
    - Permission denial paths.
10. Integration test: two players, one grants `script` perm on a room to the other, the grantee attaches a script, the granter triggers an event, the handler runs.
11. Integration test: malicious script attempts (infinite loop, huge string allocation, attempted privilege escalation by attempting to call admin APIs) — all are aborted with informative errors and the script is suspended after 5 failures.

## Testing

- `go test ./internal/script/sandbox/... ./internal/world/api/scoped/...`
- Integration scenarios driven through M1 telnet.
- Manual: attach a script to a room you own, exercise it from a second account.

## Acceptance criteria

1. `os.execute("rm -rf /")` from a player script aborts immediately with an error like `wintermute: forbidden: os.execute`.
2. `while true do end` aborts within the configured instruction budget (default ≤ 100ms wall-clock equivalent).
3. A player cannot edit a room they don't own and haven't been granted write permission on; the attempt errors cleanly.
4. A player granted `script` permission on another player's room can attach a handler that fires when someone says something there.
5. A handler that errors 5 times consecutively gets suspended; `@suspended` lists it; `@resume` re-enables it.
6. Player scripts cannot pull NPCs, mail, or other admin surface APIs — those bindings are absent from the sandboxed `wintermute` table.
7. The admin VM pool from M5 continues to work unchanged.
8. All new files carry the MIT header.

## Risks & open questions

- **gopher-lua sandbox depth**: the project has many years of users sandboxing it for similar purposes; cross-check that no recent CVEs exist before deployment. Treat the sandbox as best-effort defense in depth; don't expose the engine to wholly untrusted networks.
- **Hook overhead**: `SetHook` with a low count adds ~5–15% CPU overhead on tight loops. Default count of 10000 instructions is a reasonable starting point; profile and tune.
- **Memory budget granularity**: gopher-lua tracks allocations but the hook firing pattern is coarse. Combine with a wall-clock context cap as a hard backstop.
- **`string` library bounded replacements**: easy to miss something (`string.find` with pathological patterns can be slow). Audit the surface; consider a regex-time budget or just disabling `string.find`'s pattern-matching arguments and providing a separate `wintermute.find` function with a built-in cap.
- **Event handler back-pressure**: if a player's handler is slow, it slows the room. Solution: dispatch handlers in dedicated goroutines with the per-invocation budget; if the goroutine is still running on the next event, drop the event for that handler and increment a `dropped` counter.
- **ACL inheritance**: do permissions on a room imply permissions on objects within it? Decide explicitly. Initial answer: no — keep ACLs per-target for simplicity. Owners get inheritance by being the owner.
- **Cross-script communication**: not provided. Two scripts on adjacent objects can't share data except via the world (e.g. setting an attribute on an object). That's a feature, not a bug — keep the surface narrow.
