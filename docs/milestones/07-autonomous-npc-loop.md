# Milestone 07 — Semi-autonomous NPC loop

## Goal

NPCs observe what happens around them, decide whether to act, and can invoke admin-registered tools to change the world. They can hold goals and respond on a timer. The cost of having dozens of NPCs idling around is kept in check by a two-tier model (a tiny gate model decides "react or skip?") and a hard per-NPC token budget.

## Dependencies

- M4 (NPC memory): autonomous NPCs need long-term recall.
- M5 (admin scripting): tool registry; tools are how NPCs affect the world.

## Scope

- `internal/world/events`: a real per-room event bus (replaces the M2 subscriber slices).
- Per-NPC tick goroutine: subscribes to its room, accumulates observations, fires on a debounce or a schedule.
- Two-tier routing: gate model returns a single yes/no, then optionally the response model is called.
- Tool execution: tool calls returned by the response model are dispatched into the M5 tool registry; results are fed back to the LLM for a final response.
- Budget enforcement: per-NPC token counters with minute/hour/day windows, graceful degradation when budgets are exhausted.
- Scheduled goals: a small in-memory queue of `(npc_id, fire_at, goal)` records that admin scripts can populate.
- Memory decay job: a daily background job that decays `salience` on memories, balancing M4's salience-bump growth.

## Out of scope

- NPCs moving between rooms autonomously. (Useful, but adds room-graph navigation logic; deferred.)
- Cross-NPC coordination ("the guard tells the shopkeeper to close the shop"). NPCs can call tools but tools don't currently target other NPCs in a structured way; this can grow organically through tool design.
- Multi-step planning. Each tick is a single LLM call (plus tool round-trips); long-horizon planning isn't modeled.
- Player-tier scripting (M8).

## Architecture

### Event bus

```go
// internal/world/events/bus.go
type Event struct {
    Kind   string         // "say","emote","arrive","depart","tick","take","drop","tool"
    RoomID world.RoomID
    Actor  world.ObjectID // who caused this
    Target world.ObjectID // optional
    Text   string         // for say/emote
    At     time.Time
    Extra  map[string]any
}

type Bus interface {
    Publish(e Event)
    Subscribe(room world.RoomID) (sub <-chan Event, cancel func())
}
```

- Per-room channel fan-out. Each subscriber gets a buffered channel (capacity ~32); overflow drops oldest.
- The M2 broadcast machinery is reimplemented in terms of `Bus`. Player sessions subscribe to their current room; NPCs subscribe to their current room.
- `Publish` is non-blocking. Slow subscribers lose events; this is intentional.

### NPC tick goroutine

One goroutine per loaded NPC.

```go
// internal/npc/loop/loop.go
type Loop struct {
    NPC        *npc.NPC
    bus        events.Bus
    sub        <-chan events.Event
    cancel     func()
    debounce   time.Duration
    observations []events.Event
    nextSchedFire time.Time
}

func (l *Loop) Run(ctx)
```

Main loop, in pseudocode:

```
for {
    select {
    case e := <-sub:
        observations = append(observations, e)
        reset debounce timer
    case <-debounceFired:
        tick(observations)
        observations = nil
    case <-timeUntil(nextSchedFire):
        tick([]Event{ {Kind:"sched", Text: goalDescription} })
    case <-ctx.Done():
        return
    }
}
```

### `tick(observations)` flow

1. **Budget check** — if exhausted, broadcast a canned degraded line or stay silent; return.
2. **Gate model call** — small Ollama model (e.g. `llama3.2:1b`) with a fixed prompt:
   ```
   You are a relevance filter for an NPC named <name>.
   Persona: <one-line persona>
   Recent events:
   - <event 1>
   - <event 2>
   Should <name> respond or act now? Answer YES or NO.
   ```
   Parse first token. Anything other than `YES` is treated as NO.
   Cost: ~1 token in/out per tick on an idle room.
3. **If NO**, append observations to short-term memory anyway (no LLM cost), return.
4. **Retrieve memories** — from M4's long-term store using current observation buffer as the query.
5. **Compose context** — persona + retrieved memories + short-term history + current observations.
6. **Response model call** — with the NPC's registered tools as `ToolDef`s.
7. **Handle tool calls** — for each tool call in the response:
   a. Look up in the M5 tool registry.
   b. Validate args against the tool's schema.
   c. Invoke; capture return value.
   d. Append a `RoleTool` message with the result.
   e. Call the response model again with the appended context. (Loop up to a small bound, e.g. 3 round-trips, to prevent infinite tool calling.)
8. **Broadcast final response** — if there is text content, broadcast as `<npc> says, "<text>"` (or emote variant if the model uses the emote tool).
9. **Update short-term** with the NPC's own turn.
10. **Update budgets** with tokens consumed.

### Two-tier model configuration

Each NPC has both `chat_model` and `gate_model` columns (M3 introduced these). Defaults come from the LLM config block. The gate model can be omitted to disable gating (i.e. always call the response model) — useful for VIP NPCs.

### Budgets

```go
// internal/llm/budget/budget.go
type Window struct { Limit int; Used int; StartedAt time.Time; Duration time.Duration }

type NPCBudget struct {
    Minute Window
    Hour   Window
    Day    Window
}

func (b *NPCBudget) Allow(estimateTokens int) bool
func (b *NPCBudget) Record(actualIn, actualOut int)
```

Persisted to a `npc_budgets` table (current windows only — daily roll-up rows can be archived if desired). Reloaded on startup.

When `Allow` returns false:
- Skip the response model call. Optionally emit a "lost in thought" canned line at a 5% chance to keep the world feeling alive.
- Log a structured warning.

### Scheduled goals

```sql
CREATE TABLE npc_goals (
    id        INTEGER PRIMARY KEY,
    npc_id    INTEGER NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    fire_at   INTEGER NOT NULL,
    goal      TEXT NOT NULL,
    recurring TEXT,   -- nullable; cron-like
    created_at INTEGER NOT NULL
);
```

Admin Lua API:

```lua
wintermute.npc.schedule(npc, { fire_at = os.time() + 3600, goal = "Open the bar." })
wintermute.npc.schedule(npc, { recurring = "0 6 * * *", goal = "Cook breakfast." })  -- cron
```

A single scheduler goroutine wakes on the earliest `fire_at` and dispatches a synthetic event into the relevant NPC loop. Goals are stored as plain-text instructions injected into the LLM context as a system message for that tick.

### Memory salience decay

A daily job runs `UPDATE npc_memories SET salience = salience * 0.95;`. Memories with salience below a floor (e.g. 0.1) are eligible for deletion to keep the table bounded; deletion is gated by an admin command (`@gc-memories`) rather than automatic.

## Schema changes

`internal/store/migrations/0024_npc_budgets.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE npc_budgets (
    npc_id              INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    minute_limit        INTEGER NOT NULL DEFAULT 5000,
    minute_used         INTEGER NOT NULL DEFAULT 0,
    minute_started_at   INTEGER NOT NULL,
    hour_limit          INTEGER NOT NULL DEFAULT 100000,
    hour_used           INTEGER NOT NULL DEFAULT 0,
    hour_started_at     INTEGER NOT NULL,
    day_limit           INTEGER NOT NULL DEFAULT 1000000,
    day_used            INTEGER NOT NULL DEFAULT 0,
    day_started_at      INTEGER NOT NULL
);
```

`internal/store/migrations/0025_npc_goals.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE npc_goals (
    id          INTEGER PRIMARY KEY,
    npc_id      INTEGER NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    fire_at     INTEGER NOT NULL,
    goal        TEXT NOT NULL,
    recurring   TEXT,
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_npc_goals_fire ON npc_goals(fire_at);
```

## Implementation tasks

1. Add migrations 0024 and 0025.
2. Implement `internal/world/events`. Replace M2's subscriber slices with `events.Bus`. Verify M2 integration tests still pass.
3. Implement `internal/npc/loop`: subscription, observation buffer, debounce, gate→response flow.
4. Implement `internal/llm/budget`. Wire into the loop and into `npc.HandleSay` from M3 (so addressed responses also consume budget).
5. Implement tool-call dispatch: parse `ToolCalls` from response, validate args, invoke via the M5 registry, append `RoleTool` messages, loop with bounded depth.
6. Implement the scheduler goroutine and `wintermute.npc.schedule` Lua binding.
7. Implement the daily salience decay job.
8. Replace the M3-style `HandleSay` direct LLM call with: publish a `say` event to the room. The NPC loop will pick it up. For private addressing (the M3 "single NPC in the room" case), the same flow still applies — the gate model handles it.
9. Add `@npc-debug <npc>` admin command that prints recent observations, current budgets, last tool calls — invaluable for tuning.
10. Add config knobs:
    - default gate model, response model, debounce duration, max tool-call depth.
    - per-NPC overrides via `npc_config` columns (already exist from M3).
11. Unit tests:
    - Event bus: publish/subscribe, fan-out, overflow.
    - Loop: deterministic with fake backend; gate YES and NO paths; tool-call round-trip; depth bound; budget exhaustion path.
12. Integration test: with the fake backend, set up two NPCs in adjacent rooms, fire a series of events, assert correct cost accounting and per-room isolation.
13. Manual exercise: with real Ollama, watch two players interact while a bartender NPC chimes in based on overheard conversation.

## Testing

- `go test -tags test ./internal/world/events/... ./internal/npc/loop/... ./internal/llm/budget/...`
- Integration scenarios driven through M1 telnet + fake backend.
- Manual: with real Ollama and `slog` debug logging on, verify gate decisions look reasonable.

## Acceptance criteria

1. An NPC in a room with no players consumes only gate-model tokens (~1 token per debounce window). Verify via `npc_budgets`.
2. An NPC in a busy room responds in a way that respects its persona, retrieves memories, and uses registered tools when appropriate.
3. A registered "sell_drink" tool, invoked by the NPC in response to "I'll have a beer", actually mutates the world (e.g. moves a drink object to the player) and the NPC's response mentions the outcome.
4. With `minute_limit = 100`, a flood of player messages eventually triggers the degraded path; once the minute window expires, normal behavior resumes.
5. A scheduled goal ("Open the bar at 06:00") fires on time and produces a contextually appropriate action.
6. Daily salience decay runs without locking out player traffic and is reflected in `npc_memories.salience`.
7. The original M3 addressed-only behavior still works — addressing the bartender by name still elicits a response.
8. All new files carry the MIT header.

## Risks & open questions

- **Gate model accuracy**: tiny models can give noisy YES/NO. Mitigations: (a) use a temperature of 0; (b) log gate decisions for offline review; (c) allow an admin to bypass the gate per-NPC (`gate_model = ""` means always call response model).
- **Tool-call format compatibility**: Ollama's tool-calling support varies by model. Document the supported models in `plan.md` and gate features per model capability if needed.
- **Event ordering across NPCs**: if NPC A's tool call emits an event, NPC B in the same room observes it on the next debounce. This can produce cascades. Mitigations: a small per-NPC cooldown after each tick (e.g. 2 seconds minimum between ticks).
- **Tool-call infinite loops**: bounded depth (3) prevents this. Tools should be idempotent; document this in the M5 tool registration API.
- **Budget windows roll-over**: rolling windows can be expensive to track precisely. Use simple fixed windows (current minute, current hour, current day) — re-evaluated at first event in a new window. Imprecise at the edges; acceptable.
- **Scheduler timing accuracy**: a single goroutine with `time.AfterFunc` is fine for minute-resolution; if sub-second precision is ever needed (it shouldn't be), revisit.
- **Privacy**: an autonomous NPC in a room overhears everything. Document this in-world so players know.
