# Milestone 02 — World layer

## Goal

A logged-in player can move through a connected graph of rooms, see who and what is in each room, pick up and drop objects, and communicate with other players in the same room via `say` and `emote`. World state is persistent across restarts.

## Dependencies

- M1 (connection layer): login, sessions, DB writer.

## Scope

- World schema: rooms, exits, objects, object locations (a unified location model used for both players and items), room descriptions.
- In-memory world cache populated at startup; mutations go through the M1 writer goroutine.
- Commands: `look [object]`, directional moves (`n`/`s`/`e`/`w`/`u`/`d` plus aliases and `go <direction>`), `say`, `emote` / `:`, `who`, `inventory` / `i`, `get` / `take`, `drop`.
- Command parser (table-driven, minimal — substring matching deferred).
- Per-room event broadcast (player-visible messages).
- Persistent player location: write on every move, restore on login.
- A seed world: at least three connected rooms and a handful of objects, loaded from a SQL fixture so a fresh DB is immediately playable.

## Out of scope

- NPCs (M3).
- Scripted descriptions or rooms (M5).
- ACLs beyond a simple owner column (full ACL plumbing lands in M5/M8).
- Combat, stats, levelling — not part of the MUSH-flavored design at all.

## Architecture

### Packages introduced

- `internal/world` — the world cache and mutation API. Owns the in-memory representation and brokers all changes through the DB writer.
- `internal/world/cmd` — command parser and dispatcher invoked from the M1 session loop after login.
- `internal/world/render` — formatting helpers (room descriptions, who-list, inventory). Pure functions; easy to unit-test.

### Key types

```go
// internal/world
type RoomID int64
type ObjectID int64

type Room struct {
    ID          RoomID
    Slug        string
    Name        string
    Description string
    Exits       map[string]RoomID  // "n" -> roomID
    OwnerID     int64              // accounts.id
}

type Object struct {
    ID       ObjectID
    Slug     string
    Name     string
    ShortDesc string
    LongDesc  string
    Kind     string            // "item" | "player" | "npc" (npc unused until M3)
    OwnerID  int64
}

type Location struct {
    ObjectID ObjectID
    RoomID   RoomID
    HolderID *ObjectID        // if held by another object (inventory)
}

type World struct { /* cache + sync.RWMutex */ }

func (w *World) Look(s *Session, target string) string
func (w *World) Move(s *Session, dir string) error
func (w *World) Say(s *Session, text string) error
func (w *World) Emote(s *Session, text string) error
func (w *World) Take(s *Session, target string) error
func (w *World) Drop(s *Session, target string) error
```

### Concurrency model

- The cache holds the canonical view in memory.
- Reads acquire `RLock`.
- Writes:
  1. Acquire `Lock`.
  2. Mutate cache optimistically.
  3. Send the write through the M1 writer channel.
  4. On DB error, roll back the cache mutation and surface the error.
- The writer channel batches together related changes (e.g. removing a player from one room and adding to another) in a single transaction. The world layer's API hides this; callers don't think about transactions.

### Event broadcast

- Each room has a slice of subscribers (sessions currently in the room). A subscriber is added on `Move`/`Login` and removed on `Move`/`Logout`.
- `Broadcast(room, excluding, message)` walks subscribers and writes to each one's session.
- This subscriber list is the **proto-event-bus** for M7. Keep the API narrow so it can be replaced with a real pub/sub in M7 without leaking through the world API.

## Schema changes

`internal/store/migrations/0002_world.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE rooms (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    owner_id     INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
);

CREATE TABLE exits (
    id           INTEGER PRIMARY KEY,
    from_room    INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    direction    TEXT NOT NULL,            -- "n","s","e","w","u","d","in","out", or custom
    to_room      INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    UNIQUE(from_room, direction)
);

CREATE TABLE objects (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    short_desc   TEXT NOT NULL DEFAULT '',
    long_desc    TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL CHECK (kind IN ('item','player','npc')),
    owner_id     INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE  -- non-null iff kind='player'
);

CREATE TABLE object_locations (
    object_id    INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    room_id      INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    holder_id    INTEGER REFERENCES objects(id) ON DELETE SET NULL,
    -- exactly one of room_id and holder_id is non-null
    CHECK ((room_id IS NULL) <> (holder_id IS NULL))
);

CREATE INDEX idx_object_locations_room   ON object_locations(room_id);
CREATE INDEX idx_object_locations_holder ON object_locations(holder_id);
CREATE INDEX idx_objects_account         ON objects(account_id);
```

`internal/store/migrations/0003_seed_world.sql` creates a starter region: a few rooms (e.g. "The Lobby", "Maintenance Corridor", "Server Room"), one-way and two-way exits, a couple of objects.

When a new account is created (M1 `auth.Create`), an `objects` row of `kind='player'` is also created and located in the lobby. This wiring crosses the M1/M2 line; do it in M2 by extending the `auth` package with a hook.

## Implementation tasks

1. Add migrations `0002_world.sql` and `0003_seed_world.sql`.
2. Implement `internal/world` cache: types, `Load(ctx, db)` to populate from SQL, `RLock`/`Lock`.
3. Implement `Look`, `Move`, `Take`, `Drop`, `Say`, `Emote`, `Who`, `Inventory`.
4. Implement room subscriber bookkeeping and `Broadcast`.
5. Implement `internal/world/cmd`: parser and dispatch table, integrated into the M1 session command loop, replacing the placeholder void.
6. Extend `internal/auth`: on `Create`, also insert into `objects` (kind=player) and `object_locations` (in the lobby).
7. On `Login`, re-attach the player object's session to its current room's subscriber list.
8. On disconnect, remove the player from its room's subscriber list. Leave the player object in place — disconnected players are "asleep" and visible to others (a classic MUSH idiom).
9. Add a `who` command that lists currently-attached sessions.
10. Add a render helper that produces the canonical room view: name, description, exits, players present (with "(asleep)" tag), items.
11. Unit tests for command parsing, render helpers, and basic mutations against an in-memory SQLite.
12. Integration test: two simulated sessions in the same room, one `say`s, the other sees it.
13. Integration test: player moves between rooms, room view updates correctly, persistence survives a restart.

## Testing

- `go test ./internal/world/...`
- Integration scenario: spin server, create 2 accounts, both log in, both move around, exchange `say`, log out, log back in to same room. Assert each step.
- Manual: walk the seed world through a real telnet session.

## Acceptance criteria

1. After M1+M2, a fresh DB allows a new account to log in and immediately `look`, see exits, `move`, `say`, etc.
2. Two players in the same room see each other's `say` and `emote` output.
3. `inventory` lists objects held by the player; `get`/`drop` move objects between room and inventory.
4. After a server restart, every player is in the room they were last in.
5. The seed world is reachable: starting room has at least one exit, and at least one other room has an item to `get`.
6. All new source files carry the MIT header.

## Risks & open questions

- **Object naming for commands**: `get keycard` vs. multiple items named "keycard" in a room. Use a simple disambiguation: exact slug match → exact name match → unique substring match → "which one?" prompt. Substring match deferred if it adds too much code; minimum viable is exact-name unique match.
- **Custom exit directions** ("in"/"out"/"portal") — schema supports them; parser must too. For M2, support only the six cardinal/vertical directions plus `in`/`out`. Anything else lands in M5 (Lua API for room building).
- **Disconnected-player visibility**: classic MUSH leaves the body in place. Confirm that's the intended design (currently assumed yes).
- **Concurrency edge case**: two players simultaneously `get` the same item. The optimistic cache mutate + DB write pattern can race. Mitigation: hold the world `Lock` (not `RLock`) across the cache check + DB write for object-moving operations. Performance cost is negligible at this scale.
- **Seed data location**: SQL fixture is fine for M2. M5 will replace it with Lua creation scripts.
