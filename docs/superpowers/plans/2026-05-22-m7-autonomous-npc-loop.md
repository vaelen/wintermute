# M7 — Semi-Autonomous NPC Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the M7 semi-autonomous NPC loop: a per-room event bus, per-NPC tick goroutines that gate-then-respond via a two-tier model, tool-call dispatch into the M5 registry, per-NPC token budgets, scheduled goals, and a daily memory-salience decay job.

**Architecture:** A new `internal/world/events.Bus` provides per-room fan-out of structured events. Mutations in `internal/world` continue rendering their human-readable strings (player UX unchanged) but **additionally** publish a structured `Event` to the bus. Each NPC gets a tick goroutine in `internal/npc/loop` that subscribes to its room, buffers observations until a debounce window expires, calls a cheap gate model to decide whether to react, and on YES calls a response model with the M5 tool registry attached. Tool calls dispatch through `script/lua.ToolRegistry.Invoke`, which already serialises on the Lua pool's exec mutex. Token usage flows through a new `internal/llm/budget` manager with minute/hour/day windows persisted to `npc_budgets`. A separate scheduler goroutine reads `npc_goals` and injects synthetic events. M3's `Registry.HandleSay` becomes a thin publisher: it publishes `Say` events to the bus and the per-NPC loops own all dispatch logic.

**Tech Stack:** Go 1.23+, `modernc.org/sqlite` (pure-Go), `log/slog`, `gopher-lua` (already in use), `internal/llm/fake` for deterministic test responses. New migrations use slots **0024** and **0025** (the milestone doc says 0014/0015 — that's stale; those slots are taken).

---

## File structure

### Created

| Path | Responsibility |
|---|---|
| `internal/store/migrations/0024_npc_budgets.sql` | `npc_budgets` schema |
| `internal/store/migrations/0025_npc_goals.sql` | `npc_goals` schema + index |
| `internal/world/events/bus.go` | `Bus` interface, `Event` type, `Kind` constants |
| `internal/world/events/membus.go` | In-memory per-room channel-fanout `Bus` |
| `internal/world/events/membus_test.go` | publish/subscribe/overflow/cancel tests |
| `internal/llm/budget/budget.go` | `Window`, `NPCBudget`, `Manager` (in-memory + persist) |
| `internal/llm/budget/budget_test.go` | window math, allow/record, rollover |
| `internal/npc/loop/loop.go` | `Loop` struct, `Run` main select, debounce |
| `internal/npc/loop/dispatch.go` | gate→response→tool-call flow |
| `internal/npc/loop/loop_test.go` | gate YES/NO, tool round-trip, depth bound, budget exhaustion |
| `internal/npc/loop/manager.go` | Per-NPC `Loop` lifecycle managed by registry |
| `internal/npc/schedule/scheduler.go` | `npc_goals` reader + dispatch timer |
| `internal/npc/schedule/scheduler_test.go` | one-shot and daily recurring fire |
| `internal/npc/memory/decay.go` | Daily salience-decay goroutine |
| `internal/npc/memory/decay_test.go` | decay arithmetic, idempotency |
| `internal/integration/m7_npc_loop_test.go` | two-NPC adjacent-room scenario |

### Modified

| Path | Change |
|---|---|
| `internal/world/world.go` | Add `bus events.Bus` field; constructor accepts bus; observers preserved |
| `internal/world/mutations.go` | After existing string-broadcast in each mutation, also `bus.Publish(...)` a structured event |
| `internal/npc/registry.go` | Replace `dispatch` with bus publish; start/stop per-NPC `Loop`; thread `budget.Manager`, `events.Bus`, `*lua.ToolRegistry` into Loop construction |
| `internal/npc/npc.go` | Add `ToolNames []string` (NPCs may opt into specific tools); read column from `npc_config` |
| `internal/script/lua/npc_schedule.go` *(new in lua package)* | `wintermute.npc.schedule` Lua binding |
| `internal/world/cmd/admin.go` *(or wherever admin cmds live)* | `@npc-debug` and `@gc-memories` admin commands |
| `internal/config/config.go` | `[npc.loop]` block (debounce, max_tool_depth, default budget limits, default gate model) |
| `cmd/wintermute/main.go` | Wire bus into world.Load; construct `budget.Manager`; pass `*lua.ToolRegistry`; start scheduler + decay goroutines; tracked shutdown |
| `docs/milestones/07-autonomous-npc-loop.md` | Fix the migration numbers (0014→0024, 0015→0025) — doc/code consistency |

### Not touched

- `internal/llm/ollama` does not need M7 changes: tool-calling support is in `ToolDef`/`ToolCall` already; Ollama's tool-call surface is honoured by the existing Chat path. If a deployed model doesn't support tools, the loop's tool-call branch simply never fires.
- `internal/llm/fake` gains an optional tool-call hook **inside its existing test file scope** (extended in Task 9), not a new package.

---

## Decisions locked in this plan

1. **Bus additive, not replacement.** The milestone doc says "M2 broadcast machinery is reimplemented in terms of Bus." Doing a full presence→bus rewrite alongside the new code in one milestone is risky. We add bus publishes alongside the existing string broadcasts in this milestone. The bus is the authoritative path for NPCs and scripts; the string-rendering path stays untouched for player sessions. A later cleanup PR (not part of M7) can fold player rendering into bus subscribers without behavioural change.

2. **HandleSay becomes a publisher.** `Registry.HandleSay` no longer dispatches LLM calls directly. It publishes a `say` event to the bus and the per-NPC `Loop` (already subscribed to the room) absorbs it through its debounce. Existing tests that drive HandleSay continue to work because the call is still synchronous from their perspective; assertions on the eventual broadcast remain unchanged (the Loop fires the same `world.NPCSay`).

3. **Recurring goals: `"daily"` and `"hourly"` only.** Cron-format strings (`"0 6 * * *"`) are documented as future. M7 honours one-shot and the two named recurrences. Anything else logged-and-ignored, returning the goal as one-shot.

4. **Budget windows are fixed (not rolling).** Per the milestone doc's "Risks & open questions". A new minute/hour/day window starts at the timestamp of the first event in it. Imprecise at edges; acceptable.

5. **Tool registry is shared.** NPCs reference tools by name (via `npc_config.tools`, comma-separated). The Lua pool's `*ToolRegistry` is the single source of truth; the Loop holds a pointer to it but does not own it.

6. **Memory decay is a single goroutine** ticking once per 24 hours, started in `main.go` after world load. It writes via `db.Write` so it does not race the single-writer goroutine. `@gc-memories` is a separate admin command that *deletes* memories whose salience is below the floor; never automatic.

---

### Task 1: Migrations 0024 (npc_budgets) and 0025 (npc_goals); doc fix

**Files:**
- Create: `internal/store/migrations/0024_npc_budgets.sql`
- Create: `internal/store/migrations/0025_npc_goals.sql`
- Modify: `docs/milestones/07-autonomous-npc-loop.md` (replace `0014`/`0015` references with `0024`/`0025`)

- [ ] **Step 1: Write 0024_npc_budgets.sql**

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

- [ ] **Step 2: Write 0025_npc_goals.sql**

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

- [ ] **Step 3: Add `npc_config.tools` column via migration 0024**

Append to `0024_npc_budgets.sql`:

```sql
ALTER TABLE npc_config ADD COLUMN tools TEXT NOT NULL DEFAULT '';
```

Stored as comma-separated tool names. Empty default means "no tools".

- [ ] **Step 4: Update milestone doc**

In `docs/milestones/07-autonomous-npc-loop.md`:

- Replace `0014_budgets.sql` with `0024_npc_budgets.sql`
- Replace `0015_goals.sql` with `0025_npc_goals.sql`
- Replace task item "Add migrations 0012 and 0013" with "Add migrations 0024 and 0025".

- [ ] **Step 5: Run migrations test**

Run: `go test ./internal/store/...`
Expected: PASS. The migrations runner picks up new files via `//go:embed`; existing tests open a fresh DB and apply everything.

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/0024_npc_budgets.sql internal/store/migrations/0025_npc_goals.sql docs/milestones/07-autonomous-npc-loop.md
git commit -m "M7: migrations 0024 (npc_budgets, npc_config.tools) and 0025 (npc_goals)"
```

---

### Task 2: Event bus — types and in-memory implementation

**Files:**
- Create: `internal/world/events/bus.go`
- Create: `internal/world/events/membus.go`
- Create: `internal/world/events/membus_test.go`
- Modify: `internal/world/events/doc.go` (drop the stub once `bus.go` defines the package comment)

- [ ] **Step 1: Write bus.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package events is the per-room publish/subscribe bus that NPCs and
// player scripts subscribe to. M2's string-broadcast machinery in
// internal/world publishes a structured Event alongside its existing
// formatted line; subscribers consume the structured form.
package events

import (
	"time"

	"github.com/vaelen/wintermute/internal/world"
)

// Kind enumerates the structured event kinds an NPC loop or admin script
// may observe. New kinds are added without breaking subscribers: unknown
// kinds are simply ignored by handlers that don't care about them.
type Kind string

const (
	KindSay    Kind = "say"
	KindEmote  Kind = "emote"
	KindArrive Kind = "arrive"
	KindDepart Kind = "depart"
	KindTake   Kind = "take"
	KindDrop   Kind = "drop"
	KindAttach Kind = "attach"
	KindDetach Kind = "detach"
	KindTool   Kind = "tool"
	KindSched  Kind = "sched"
)

// Event is the structured form of a per-room occurrence.
type Event struct {
	Kind   Kind
	RoomID world.RoomID
	Actor  world.ObjectID
	Target world.ObjectID
	Text   string
	At     time.Time
	Extra  map[string]any
}

// Bus is the per-room publish/subscribe surface.
type Bus interface {
	Publish(e Event)
	Subscribe(room world.RoomID) (<-chan Event, func())
	Close()
}
```

- [ ] **Step 2: Write membus.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package events

import (
	"sync"

	"github.com/vaelen/wintermute/internal/world"
)

const defaultSubBuffer = 32

// MemBus is the in-process Bus. Each subscriber gets a buffered channel;
// when a slow subscriber's channel is full, Publish drops the oldest
// event from that subscriber and enqueues the new one. Publish is non-
// blocking and safe to call from any goroutine, including from inside
// world.mu critical sections.
type MemBus struct {
	mu     sync.Mutex
	subs   map[world.RoomID]map[int64]chan Event
	nextID int64
}

func NewMemBus() *MemBus {
	return &MemBus{subs: map[world.RoomID]map[int64]chan Event{}}
}

func (b *MemBus) Publish(e Event) {
	b.mu.Lock()
	room := b.subs[e.RoomID]
	chans := make([]chan Event, 0, len(room))
	for _, ch := range room {
		chans = append(chans, ch)
	}
	b.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- e:
		default:
			// Channel full: drop the oldest then enqueue. Two non-
			// blocking ops; if either fails we drop the new event.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}

func (b *MemBus) Subscribe(room world.RoomID) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[room] == nil {
		b.subs[room] = map[int64]chan Event{}
	}
	id := b.nextID
	b.nextID++
	ch := make(chan Event, defaultSubBuffer)
	b.subs[room][id] = ch
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if room := b.subs[room]; room != nil {
			if c, ok := room[id]; ok {
				close(c)
				delete(room, id)
			}
		}
	}
	return ch, cancel
}

func (b *MemBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, room := range b.subs {
		for _, ch := range room {
			close(ch)
		}
	}
	b.subs = nil
}
```

- [ ] **Step 3: Write membus_test.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package events

import (
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/world"
)

func TestMemBus_PublishToSubscribers(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch, cancel := b.Subscribe(world.RoomID(1))
	defer cancel()
	b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "hello"})
	select {
	case e := <-ch:
		if e.Text != "hello" || e.Kind != KindSay {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
}

func TestMemBus_PerRoomIsolation(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch1, c1 := b.Subscribe(world.RoomID(1))
	defer c1()
	ch2, c2 := b.Subscribe(world.RoomID(2))
	defer c2()
	b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "r1"})
	select {
	case <-ch2:
		t.Fatal("room 2 received room 1 event")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case e := <-ch1:
		if e.Text != "r1" {
			t.Fatalf("expected r1, got %q", e.Text)
		}
	default:
		t.Fatal("room 1 did not receive event")
	}
}

func TestMemBus_OverflowDropsOldest(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch, cancel := b.Subscribe(world.RoomID(1))
	defer cancel()
	for i := 0; i < defaultSubBuffer+10; i++ {
		b.Publish(Event{Kind: KindSay, RoomID: 1, Text: "x"})
	}
	count := 0
loop:
	for {
		select {
		case <-ch:
			count++
		default:
			break loop
		}
	}
	if count == 0 || count > defaultSubBuffer+1 {
		t.Fatalf("received %d events; want between 1 and %d", count, defaultSubBuffer+1)
	}
}

func TestMemBus_CancelRemovesSubscriber(t *testing.T) {
	b := NewMemBus()
	defer b.Close()
	ch, cancel := b.Subscribe(world.RoomID(1))
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
}
```

- [ ] **Step 4: Delete `internal/world/events/doc.go`**

`bus.go` now carries the package comment. The stub file is redundant.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/world/events/...`
Expected: 4 tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/world/events/
git commit -m "M7: per-room events.Bus with in-memory channel fan-out"
```

---

### Task 3: Wire Bus into World; publish events from mutations

**Files:**
- Modify: `internal/world/world.go` (add `bus events.Bus` field + accessor)
- Modify: `internal/world/mutations.go` (publish events after each broadcast)
- Modify: `cmd/wintermute/main.go` (construct bus, pass to world.Load)

- [ ] **Step 1: Add Bus to World**

In `internal/world/world.go`, add to imports:
```go
"github.com/vaelen/wintermute/internal/world/events"
```

Add a field on `World`:
```go
bus events.Bus
```

Add a parameter to `Load` (after `logger`):
```go
func Load(ctx context.Context, db *store.DB, logger *slog.Logger, bus events.Bus) (*World, error) {
```

In the struct literal:
```go
bus: bus,
```

Add an accessor (low in the file):
```go
// Bus returns the per-room event bus the world publishes structured
// events to. Returns nil if no bus was supplied to Load (older tests).
func (w *World) Bus() events.Bus { return w.bus }
```

**Wait — circular import risk.** `events` imports `world` (for `RoomID`, `ObjectID`). If `world` imports `events`, that's a cycle. **Resolution:** move `RoomID` and `ObjectID` definitions to a tiny new package `internal/world/ids`, OR define a duplicate type in events. Cleanest: define `events.RoomID = int64` and `events.ObjectID = int64` (literal aliases) so events doesn't import world. Adjust bus.go and membus.go accordingly before this step.

- [ ] **Step 1a: Fix circular import — alias types in events**

Edit `internal/world/events/bus.go`:

Remove the `internal/world` import. Replace `world.RoomID` and `world.ObjectID` with:

```go
type RoomID = int64
type ObjectID = int64
```

(Use type aliases, not new types, so call sites can pass `world.RoomID` directly without conversion.)

Edit `internal/world/events/membus.go`: remove the world import; remove the `world.` prefixes.

Edit `internal/world/events/membus_test.go`: remove the world import; replace `world.RoomID(1)` with `RoomID(1)`.

Confirm: `grep -rn '"github.com/vaelen/wintermute/internal/world"' internal/world/events/` returns nothing.

- [ ] **Step 1b: Verify type aliases bridge correctly**

In `internal/world/types.go`, confirm `RoomID` and `ObjectID` are `int64`-based:

```bash
grep -nE 'type (RoomID|ObjectID)' internal/world/types.go
```

If they are `type RoomID int64` (named type, not alias), Go will require an explicit cast at the boundary. Either:
- Change events to use `int64` directly and have mutations cast: `RoomID: int64(loc.RoomID)`, OR
- Use `type RoomID = int64` aliases on both sides.

**Choose: cast at boundary.** Keep `events.RoomID = int64`, `events.ObjectID = int64` (both as aliases). Then `world.RoomID(x)` (a named `int64`) converts cleanly to `int64` with one cast at each publish site.

- [ ] **Step 2: Publish from Say**

In `internal/world/mutations.go`, after `flush(pending)` in `Say` (around line 344), and AFTER any `observer` call but before `return nil`:

```go
if w.bus != nil {
    w.bus.Publish(events.Event{
        Kind:   events.KindSay,
        RoomID: int64(roomID),
        Actor:  int64(speakerID),
        Text:   text,
        At:     time.Now(),
    })
}
```

Add `"time"` and `"github.com/vaelen/wintermute/internal/world/events"` to imports if not present.

- [ ] **Step 3: Publish from NPCSay, Emote, Take, Drop, Move (arrive+depart), Attach, Detach**

For each function in `mutations.go`, after the existing `flush(...)` call(s), capture room ids and other relevant ids BEFORE releasing the lock (most already do), then publish post-flush:

```go
// NPCSay: KindSay with Actor = npcID, Text = text
// Emote:  KindEmote with Actor = playerID, Text = text
// Take:   KindTake with Actor = playerID, Target = objID, Text = obj.Name
// Drop:   KindDrop with Actor = playerID, Target = objID, Text = obj.Name
// Move:   two events — KindDepart in from-room, KindArrive in to-room (Actor = playerID, Text = direction)
// Attach: KindAttach with Actor = playerID
// Detach: KindDetach with Actor = playerID
```

Each publish is wrapped in `if w.bus != nil { ... }`.

The full text of each Publish call follows the Say example; only Kind/Actor/Target/Text differ. **Do not** include `\r\n` in `Text` — that is presentation; the structured event carries the raw text.

- [ ] **Step 4: Update Load signature in cmd/wintermute/main.go**

Find the call to `world.Load`. Insert a bus argument:

```go
bus := events.NewMemBus()
defer bus.Close()
w, err := world.Load(ctx, db, logger, bus)
```

Pass `bus` into the registry constructor as well (Task 10 will use it).

Add `"github.com/vaelen/wintermute/internal/world/events"` to main.go imports.

- [ ] **Step 5: Update every test that calls world.Load**

Run:
```bash
grep -rn "world.Load(" internal/ cmd/ | grep -v "_test.go.*//"
```

For each call site, insert `events.NewMemBus()` (or `nil` for tests that don't care about events) as the new fourth argument. Tests that want to observe events get a real bus.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/world/...`
Expected: PASS. All existing world tests continue to work (bus is additive; nil bus = no publishes).

- [ ] **Step 7: Run full suite**

Run: `go test ./...`
Expected: PASS (with the same flaky auth test as baseline).

- [ ] **Step 8: Commit**

```bash
git add internal/world/ cmd/wintermute/main.go
git commit -m "M7: world publishes structured events to per-room bus"
```

---

### Task 4: Budget package — windows, allow/record, persistence

**Files:**
- Create: `internal/llm/budget/budget.go`
- Create: `internal/llm/budget/manager.go`
- Create: `internal/llm/budget/budget_test.go`
- Modify: `internal/llm/budget/doc.go` (delete; the new files carry the package comment)

- [ ] **Step 1: Write failing test for window math**

`internal/llm/budget/budget_test.go`:

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package budget

import (
	"testing"
	"time"
)

func TestWindow_AllowAndRecord(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := Window{Limit: 100, Used: 0, StartedAt: now, Duration: time.Minute}
	if !w.Allow(50, now.Add(5*time.Second)) {
		t.Fatal("should allow 50 under 100")
	}
	w.Record(50, now.Add(5*time.Second))
	if w.Used != 50 {
		t.Fatalf("Used=%d want 50", w.Used)
	}
	if w.Allow(60, now.Add(10*time.Second)) {
		t.Fatal("should deny 60 (would exceed)")
	}
}

func TestWindow_RolloverResetsUsed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := Window{Limit: 100, Used: 90, StartedAt: now, Duration: time.Minute}
	w.Record(5, now.Add(61*time.Second))
	if w.Used != 5 {
		t.Fatalf("Used after rollover=%d want 5", w.Used)
	}
	if !w.StartedAt.After(now) {
		t.Fatal("StartedAt should advance on rollover")
	}
}

func TestNPCBudget_AllowRequiresAllWindows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := NewNPCBudget(now, Defaults{Minute: 100, Hour: 1000, Day: 10000})
	b.Minute.Used = 95
	if b.Allow(10, now) {
		t.Fatal("minute window exhausted but Allow returned true")
	}
}
```

- [ ] **Step 2: Run test, see it fail**

Run: `go test ./internal/llm/budget/...`
Expected: FAIL — types `Window`, `NPCBudget`, `Defaults` not defined.

- [ ] **Step 3: Write budget.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package budget tracks per-NPC token consumption across fixed
// minute/hour/day windows. A request is Allow'd only if every window
// has room for its estimated cost; Record decrements available room
// after the LLM call returns the actual usage. Windows roll over
// lazily: the next Record or Allow that lands in a new window resets
// Used and advances StartedAt.
package budget

import "time"

type Window struct {
	Limit     int
	Used      int
	StartedAt time.Time
	Duration  time.Duration
}

// Allow reports whether estimate additional tokens can be accommodated
// in the window at time now (after lazy rollover).
func (w *Window) Allow(estimate int, now time.Time) bool {
	w.rollover(now)
	return w.Used+estimate <= w.Limit
}

// Record adds tokens to Used (after lazy rollover).
func (w *Window) Record(tokens int, now time.Time) {
	w.rollover(now)
	w.Used += tokens
}

func (w *Window) rollover(now time.Time) {
	if w.StartedAt.IsZero() || now.Sub(w.StartedAt) >= w.Duration {
		w.StartedAt = now
		w.Used = 0
	}
}

type Defaults struct {
	Minute, Hour, Day int
}

type NPCBudget struct {
	Minute Window
	Hour   Window
	Day    Window
}

func NewNPCBudget(now time.Time, d Defaults) *NPCBudget {
	return &NPCBudget{
		Minute: Window{Limit: d.Minute, StartedAt: now, Duration: time.Minute},
		Hour:   Window{Limit: d.Hour, StartedAt: now, Duration: time.Hour},
		Day:    Window{Limit: d.Day, StartedAt: now, Duration: 24 * time.Hour},
	}
}

func (b *NPCBudget) Allow(estimate int, now time.Time) bool {
	return b.Minute.Allow(estimate, now) &&
		b.Hour.Allow(estimate, now) &&
		b.Day.Allow(estimate, now)
}

func (b *NPCBudget) Record(usageIn, usageOut int, now time.Time) {
	total := usageIn + usageOut
	b.Minute.Record(total, now)
	b.Hour.Record(total, now)
	b.Day.Record(total, now)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/budget/...`
Expected: PASS.

- [ ] **Step 5: Write manager.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package budget

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// Manager holds the live per-NPC NPCBudget for every NPC that has at
// least one row in npc_budgets. Allow/Record are concurrency-safe.
// Persistence is best-effort: Record schedules a debounced flush to the
// DB; a missed flush at shutdown loses up to the last flushInterval of
// usage data, which is acceptable for a usage counter.
type Manager struct {
	db       *store.DB
	defaults Defaults
	mu       sync.Mutex
	budgets  map[world.ObjectID]*NPCBudget
	dirty    map[world.ObjectID]struct{}
}

func NewManager(db *store.DB, d Defaults) *Manager {
	return &Manager{
		db:       db,
		defaults: d,
		budgets:  map[world.ObjectID]*NPCBudget{},
		dirty:    map[world.ObjectID]struct{}{},
	}
}

// Load fetches every npc_budgets row into memory. Missing rows are
// not seeded here; the first Allow for a new NPC seeds in memory and
// flushes on the next debounce.
func (m *Manager) Load(ctx context.Context) error {
	rows, err := m.db.Read().QueryContext(ctx,
		`SELECT npc_id,
		        minute_limit, minute_used, minute_started_at,
		        hour_limit, hour_used, hour_started_at,
		        day_limit, day_used, day_started_at
		   FROM npc_budgets`)
	if err != nil {
		return fmt.Errorf("budget: load: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                                                  int64
			ml, mu, ms, hl, hu, hs, dl, du, ds                  int64
		)
		if err := rows.Scan(&id, &ml, &mu, &ms, &hl, &hu, &hs, &dl, &du, &ds); err != nil {
			return fmt.Errorf("budget: scan: %w", err)
		}
		b := &NPCBudget{
			Minute: Window{Limit: int(ml), Used: int(mu), StartedAt: time.Unix(ms, 0), Duration: time.Minute},
			Hour:   Window{Limit: int(hl), Used: int(hu), StartedAt: time.Unix(hs, 0), Duration: time.Hour},
			Day:    Window{Limit: int(dl), Used: int(du), StartedAt: time.Unix(ds, 0), Duration: 24 * time.Hour},
		}
		m.budgets[world.ObjectID(id)] = b
	}
	return rows.Err()
}

func (m *Manager) get(npcID world.ObjectID, now time.Time) *NPCBudget {
	if b, ok := m.budgets[npcID]; ok {
		return b
	}
	b := NewNPCBudget(now, m.defaults)
	m.budgets[npcID] = b
	return b
}

func (m *Manager) Allow(npcID world.ObjectID, estimate int) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.get(npcID, now).Allow(estimate, now)
}

func (m *Manager) Record(npcID world.ObjectID, usageIn, usageOut int) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.get(npcID, now).Record(usageIn, usageOut, now)
	m.dirty[npcID] = struct{}{}
}

// Flush writes every dirty budget to the DB and clears the dirty set.
// Called periodically by a flushLoop goroutine and once at shutdown.
func (m *Manager) Flush(ctx context.Context) error {
	m.mu.Lock()
	if len(m.dirty) == 0 {
		m.mu.Unlock()
		return nil
	}
	snap := make(map[world.ObjectID]NPCBudget, len(m.dirty))
	for id := range m.dirty {
		snap[id] = *m.budgets[id]
	}
	m.dirty = map[world.ObjectID]struct{}{}
	m.mu.Unlock()

	return m.db.Write(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO npc_budgets
			   (npc_id,
			    minute_limit, minute_used, minute_started_at,
			    hour_limit, hour_used, hour_started_at,
			    day_limit, day_used, day_started_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(npc_id) DO UPDATE SET
			    minute_limit=excluded.minute_limit,
			    minute_used=excluded.minute_used,
			    minute_started_at=excluded.minute_started_at,
			    hour_limit=excluded.hour_limit,
			    hour_used=excluded.hour_used,
			    hour_started_at=excluded.hour_started_at,
			    day_limit=excluded.day_limit,
			    day_used=excluded.day_used,
			    day_started_at=excluded.day_started_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, b := range snap {
			if _, err := stmt.ExecContext(ctx,
				int64(id),
				int64(b.Minute.Limit), int64(b.Minute.Used), b.Minute.StartedAt.Unix(),
				int64(b.Hour.Limit), int64(b.Hour.Used), b.Hour.StartedAt.Unix(),
				int64(b.Day.Limit), int64(b.Day.Used), b.Day.StartedAt.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunFlushLoop flushes dirty budgets at interval until ctx is done. On
// ctx done it performs one final Flush so the most recent windows are
// not lost. Errors are logged via slog by the caller via a wrapping
// goroutine; this function returns the last error from Flush.
func (m *Manager) RunFlushLoop(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := m.Flush(ctx); err != nil {
				return err
			}
		case <-ctx.Done():
			return m.Flush(context.Background())
		}
	}
}
```

- [ ] **Step 6: Delete `internal/llm/budget/doc.go`**

The new files carry the package comment.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/llm/budget/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/llm/budget/
git commit -m "M7: per-NPC token budgets with minute/hour/day windows"
```

---

### Task 5: NPC loop — skeleton with subscribe and debounce

**Files:**
- Create: `internal/npc/loop/loop.go`
- Create: `internal/npc/loop/loop_test.go`
- Modify: `internal/npc/loop/doc.go` (delete)

- [ ] **Step 1: Write failing test for debounce buffering**

`internal/npc/loop/loop_test.go`:

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/world/events"
)

func TestLoop_BuffersUntilDebounceExpires(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()

	got := make(chan []events.Event, 1)
	l := &Loop{
		RoomID:   1,
		Bus:      bus,
		Debounce: 50 * time.Millisecond,
		Tick: func(_ context.Context, obs []events.Event) {
			got <- obs
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	for i := 0; i < 3; i++ {
		bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "x"})
	}

	select {
	case obs := <-got:
		if len(obs) != 3 {
			t.Fatalf("want 3 buffered observations, got %d", len(obs))
		}
	case <-time.After(time.Second):
		t.Fatal("tick never fired")
	}
}

func TestLoop_StopsOnCtxDone(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()
	l := &Loop{RoomID: 1, Bus: bus, Debounce: 10 * time.Millisecond, Tick: func(context.Context, []events.Event) {}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit on ctx cancel")
	}
}
```

- [ ] **Step 2: Run test, see it fail**

Run: `go test ./internal/npc/loop/...`
Expected: FAIL — `Loop` and `Run` not defined.

- [ ] **Step 3: Write loop.go (skeleton only)**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package loop drives the per-NPC observation-and-act cycle: each NPC
// has one Loop goroutine that subscribes to its room's events.Bus,
// buffers observations until a debounce window expires, and calls
// Tick to decide whether (and how) to act.
package loop

import (
	"context"
	"time"

	"github.com/vaelen/wintermute/internal/world/events"
)

// Loop is one NPC's tick goroutine. Construct it with all required
// fields filled in and call Run from a goroutine.
type Loop struct {
	RoomID   events.RoomID
	Bus      events.Bus
	Debounce time.Duration
	Tick     func(ctx context.Context, observations []events.Event)
}

func (l *Loop) Run(ctx context.Context) {
	sub, cancel := l.Bus.Subscribe(l.RoomID)
	defer cancel()

	var (
		obs       []events.Event
		debounceT *time.Timer
	)
	armDebounce := func() {
		if debounceT == nil {
			debounceT = time.NewTimer(l.Debounce)
			return
		}
		if !debounceT.Stop() {
			select {
			case <-debounceT.C:
			default:
			}
		}
		debounceT.Reset(l.Debounce)
	}
	debounceC := func() <-chan time.Time {
		if debounceT == nil {
			return nil
		}
		return debounceT.C
	}

	for {
		select {
		case e, ok := <-sub:
			if !ok {
				return
			}
			obs = append(obs, e)
			armDebounce()
		case <-debounceC():
			if len(obs) > 0 {
				l.Tick(ctx, obs)
				obs = nil
			}
		case <-ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 4: Delete `internal/npc/loop/doc.go`**

- [ ] **Step 5: Run tests**

Run: `go test ./internal/npc/loop/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/npc/loop/
git commit -m "M7: NPC loop skeleton — subscribe + debounce observation buffer"
```

---

### Task 6: Loop — gate model decision

**Files:**
- Create: `internal/npc/loop/dispatch.go`
- Modify: `internal/npc/loop/loop.go` (add fields the dispatch flow needs)
- Modify: `internal/npc/loop/loop_test.go` (add gate YES/NO tests using fake backend)

- [ ] **Step 1: Extend Loop struct in loop.go**

Replace the `Loop` struct definition with:

```go
type Loop struct {
	RoomID   events.RoomID
	Bus      events.Bus
	Debounce time.Duration

	// NPC identity. Filled in by the manager (Task 10).
	NPCID     events.ObjectID
	NPCName   string
	Persona   string

	// LLM clients. May share an instance; the loop calls Chat with
	// different model overrides via ChatOpts.Model.
	LLM       llm.LLM
	ChatModel string
	GateModel string

	// Memory and budget. Both may be nil for tests; the loop short-
	// circuits the corresponding step when a dependency is missing.
	Memory  *memory.State
	Budget  *budget.Manager
	Tools   *lua.ToolRegistry
	Pool    *lua.Pool
	ToolNames []string
	MaxToolDepth int

	// World handle. Used to broadcast NPCSay/NPCEmote replies.
	World *world.World

	Logger *slog.Logger

	// Tick is the default tick handler. Production code uses
	// l.defaultTick (set in Run). Tests may override it.
	Tick func(ctx context.Context, observations []events.Event)
}
```

Add imports:
```go
"log/slog"

"github.com/vaelen/wintermute/internal/llm"
"github.com/vaelen/wintermute/internal/llm/budget"
"github.com/vaelen/wintermute/internal/npc/memory"
"github.com/vaelen/wintermute/internal/script/lua"
"github.com/vaelen/wintermute/internal/world"
```

In `Run`, before the main `for` loop:
```go
if l.Tick == nil {
    l.Tick = l.defaultTick
}
if l.Logger == nil {
    l.Logger = slog.Default()
}
```

- [ ] **Step 2: Write dispatch.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/world/events"
)

// defaultTick is the production-mode action: budget check, gate model,
// then optionally the response model with tool calls.
func (l *Loop) defaultTick(ctx context.Context, obs []events.Event) {
	if l.LLM == nil {
		return
	}

	if l.Budget != nil && !l.Budget.Allow(l.NPCID, gateBudgetEstimate) {
		l.Logger.Warn("npc loop: budget exhausted, skipping tick",
			"npc", l.NPCName)
		return
	}

	if l.GateModel != "" {
		if !l.gateAllows(ctx, obs) {
			l.recordObservationsToShortTerm(obs)
			return
		}
	}

	l.respond(ctx, obs)
}

// gateBudgetEstimate is the rough token cost of one gate call. Sized so
// a single gate decision can always fit even in a near-exhausted minute
// window — gating must never be skipped while the response model would
// fail-soft.
const gateBudgetEstimate = 32

// gatePrompt is the system message for the gate model. Kept short
// because the gate model is tiny (e.g. llama3.2:1b); the response is
// parsed as the first significant token.
const gatePrompt = `You are a relevance filter for the NPC %q.
Persona: %s
Recent events:
%s
Should %s respond or act now? Answer YES or NO. Answer with only one word.`

func (l *Loop) gateAllows(ctx context.Context, obs []events.Event) bool {
	prompt := fmt.Sprintf(gatePrompt,
		l.NPCName, l.Persona, renderObservations(obs), l.NPCName)
	resp, err := l.LLM.Chat(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
	}, nil, llm.ChatOpts{Model: l.GateModel, Temperature: 0})
	if err != nil {
		l.Logger.Warn("npc loop: gate call failed, defaulting to YES",
			"npc", l.NPCName, "err", err)
		return true
	}
	if l.Budget != nil {
		l.Budget.Record(l.NPCID, resp.UsageIn, resp.UsageOut)
	}
	tok := firstToken(resp.Content)
	return strings.EqualFold(tok, "YES")
}

func renderObservations(obs []events.Event) string {
	var b strings.Builder
	for _, e := range obs {
		switch e.Kind {
		case events.KindSay:
			fmt.Fprintf(&b, "- someone said: %s\n", e.Text)
		case events.KindEmote:
			fmt.Fprintf(&b, "- someone %s\n", e.Text)
		case events.KindArrive:
			b.WriteString("- someone arrived\n")
		case events.KindDepart:
			b.WriteString("- someone left\n")
		case events.KindSched:
			fmt.Fprintf(&b, "- scheduled goal: %s\n", e.Text)
		default:
			fmt.Fprintf(&b, "- %s\n", e.Kind)
		}
	}
	return b.String()
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '.' || r == ',' {
			return s[:i]
		}
	}
	return s
}

func (l *Loop) recordObservationsToShortTerm(obs []events.Event) {
	// Filled in alongside Task 7 (memory wiring). Stub here so the gate-NO
	// path remains a no-op until that task lands.
	_ = obs
}

func (l *Loop) respond(ctx context.Context, obs []events.Event) {
	// Filled in by Task 7.
	_ = ctx
	_ = obs
}
```

- [ ] **Step 3: Add gate test**

Append to `internal/npc/loop/loop_test.go`:

```go
import (
	"github.com/vaelen/wintermute/internal/llm"
	"github.com/vaelen/wintermute/internal/llm/fake"
)

func TestLoop_GateNo_SkipsRespond(t *testing.T) {
	bus := events.NewMemBus()
	defer bus.Close()
	fb := fake.New(map[string]string{"default": "NO"})
	called := make(chan struct{}, 1)
	l := &Loop{
		RoomID:    1, Bus: bus, Debounce: 20 * time.Millisecond,
		NPCID: 100, NPCName: "Test", Persona: "tester",
		LLM: fb, GateModel: "gate",
	}
	l.Tick = func(ctx context.Context, obs []events.Event) {
		l.defaultTick(ctx, obs)
		called <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	bus.Publish(events.Event{Kind: events.KindSay, RoomID: 1, Text: "ignore me"})
	<-called
	if fb.RespondCalls() > 0 {
		t.Fatal("respond model called on NO gate")
	}
}
```

The `fake` backend currently uses substring matching against the `responses` map; to support a default fallback, extend `internal/llm/fake/fake.go` if needed. Inspect the file: if `responses["default"]` (or similar) isn't honoured today, add a `Default string` field and use it when no key matches.

- [ ] **Step 4: Extend fake backend with a default response if missing**

Read `internal/llm/fake/fake.go`. If a `Default` fallback doesn't exist:

```go
// In fake.go, add to New options:
type Options struct {
    Default string
    // ...existing fields
}

// In Chat, after substring lookups produce nothing:
if def := f.opts.Default; def != "" {
    return llm.Response{Content: def, UsageIn: 1, UsageOut: 1}, nil
}
```

Or — if `fake.New` takes a `map[string]string` only — add a `WithDefault(string)` builder. Follow whatever pattern the existing tests use; adapt the test in Step 3 to match.

Also expose a counter so tests can assert the response model was/wasn't called. If the fake doesn't track per-model invocations, add:

```go
type Fake struct {
    // ...
    chatCallsByModel map[string]int
    mu sync.Mutex
}

func (f *Fake) RespondCalls() int {
    f.mu.Lock(); defer f.mu.Unlock()
    return f.chatCallsByModel["response"] // or whatever the test uses
}
```

Inside Chat, bump the counter keyed by `opts.Model`.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/npc/loop/... ./internal/llm/fake/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/npc/loop/ internal/llm/fake/
git commit -m "M7: NPC loop gate model call + fake-backend default response"
```

---

### Task 7: Loop — response model call, memory retrieval, broadcast

**Files:**
- Modify: `internal/npc/loop/dispatch.go` (implement respond() and recordObservationsToShortTerm())
- Modify: `internal/npc/loop/loop_test.go` (add response/broadcast tests)

- [ ] **Step 1: Write failing test**

Append to `loop_test.go`:

```go
func TestLoop_GateYes_CallsRespondAndBroadcasts(t *testing.T) {
	// Setup: world with one room and one NPC; fake backend returns "YES"
	// for gate and "Hello there." for the response model. Loop should
	// publish via world.NPCSay; observe via a second subscriber on the
	// same room.

	// (Full setup body filled by engineer; see internal/world tests for
	// world construction patterns. Use an in-memory store.DB.)

	// Asserts:
	//   - response model called exactly once
	//   - world.NPCSay broadcast contains "Hello there."
}
```

(Engineer: write a real-world-construction test here following the pattern in `internal/world/world_test.go`. Key assertions:)

```go
respond := make(chan string, 1)
// subscribe a separate channel to capture KindSay from the NPC
ch, cancel := bus.Subscribe(roomID)
defer cancel()
// publish a player Say event
bus.Publish(events.Event{Kind: events.KindSay, RoomID: roomID, Actor: playerID, Text: "hello"})
select {
case e := <-ch:
    if e.Actor != npcID || !strings.Contains(e.Text, "Hello there.") {
        t.Fatalf("unexpected reply: %+v", e)
    }
case <-time.After(2 * time.Second):
    t.Fatal("no reply event")
}
```

Note: the bus receives BOTH the inbound player Say AND the outbound NPC Say (because the loop publishes via World.NPCSay → world publishes structured event). Drain the inbound event first, then assert on the outbound.

- [ ] **Step 2: Implement respond()**

Replace the stub respond function in dispatch.go:

```go
func (l *Loop) respond(ctx context.Context, obs []events.Event) {
	// Step 1: memory retrieval, keyed on the latest spoken observation
	// (skip if no say event in this batch).
	query := latestSayText(obs)
	var memCtx string
	if l.Memory != nil && query != "" {
		mems, err := l.Memory.Retrieve(ctx, 0, 5, 0.7)
		if err != nil {
			l.Logger.Warn("npc loop: memory retrieve failed",
				"npc", l.NPCName, "err", err)
		} else if len(mems) > 0 {
			memCtx = renderMemoryContext(mems)
		}
	}

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: l.Persona},
	}
	if memCtx != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: memCtx})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: renderObservations(obs)})

	tools := l.toolDefs()

	resp, err := l.callChatWithTools(ctx, msgs, tools, 0)
	if err != nil {
		l.Logger.Warn("npc loop: response model failed",
			"npc", l.NPCName, "err", err)
		return
	}
	reply := strings.TrimSpace(resp.Content)
	if reply == "" {
		l.recordObservationsToShortTerm(obs)
		return
	}

	if err := l.World.NPCSay(l.NPCID, reply); err != nil {
		l.Logger.Warn("npc loop: NPCSay failed",
			"npc", l.NPCName, "err", err)
	}
	l.recordObservationsToShortTerm(obs)
	l.recordOwnReplyToShortTerm(reply)
}

func latestSayText(obs []events.Event) string {
	for i := len(obs) - 1; i >= 0; i-- {
		if obs[i].Kind == events.KindSay {
			return obs[i].Text
		}
	}
	return ""
}

func renderMemoryContext(mems []memory.Memory) string {
	var b strings.Builder
	b.WriteString("Relevant memories from past conversations:\n")
	for _, m := range mems {
		b.WriteString("- ")
		b.WriteString(m.Summary)
		b.WriteString("\n")
	}
	return b.String()
}

func (l *Loop) recordObservationsToShortTerm(obs []events.Event) {
	if l.Memory == nil {
		return
	}
	for _, e := range obs {
		if e.Kind != events.KindSay {
			continue
		}
		l.Memory.Append(0, memory.Turn{
			Speaker: "observed",
			Text:    e.Text,
			At:      e.At,
		})
	}
}

func (l *Loop) recordOwnReplyToShortTerm(reply string) {
	if l.Memory == nil {
		return
	}
	l.Memory.Append(0, memory.Turn{
		Speaker: "<npc>",
		Text:    reply,
		At:      time.Now(),
	})
}
```

Add `"time"` and `memory "github.com/vaelen/wintermute/internal/npc/memory"` imports if missing.

- [ ] **Step 3: Stub callChatWithTools and toolDefs**

```go
func (l *Loop) toolDefs() []llm.ToolDef {
	if l.Tools == nil || len(l.ToolNames) == 0 {
		return nil
	}
	out := make([]llm.ToolDef, 0, len(l.ToolNames))
	for _, name := range l.ToolNames {
		entry := l.Tools.Get(name)
		if entry == nil {
			continue
		}
		out = append(out, llm.ToolDef{
			Name:        entry.Name,
			Description: entry.Description,
			Schema:      entry.Schema,
		})
	}
	return out
}

// callChatWithTools issues a single Chat call. Tool-call handling is
// added in Task 8.
func (l *Loop) callChatWithTools(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef, depth int) (llm.Response, error) {
	if l.Budget != nil {
		const estimate = 256
		if !l.Budget.Allow(l.NPCID, estimate) {
			return llm.Response{}, errBudgetExhausted
		}
	}
	resp, err := l.LLM.Chat(ctx, msgs, tools, llm.ChatOpts{Model: l.ChatModel})
	if err != nil {
		return resp, err
	}
	if l.Budget != nil {
		l.Budget.Record(l.NPCID, resp.UsageIn, resp.UsageOut)
	}
	return resp, nil
}

var errBudgetExhausted = errors.New("npc loop: budget exhausted")
```

Add `"errors"` import.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/npc/loop/...`
Expected: PASS (including the new response/broadcast test).

- [ ] **Step 5: Commit**

```bash
git add internal/npc/loop/
git commit -m "M7: NPC loop response model call + memory retrieval + broadcast"
```

---

### Task 8: Loop — tool-call dispatch with bounded depth

**Files:**
- Modify: `internal/npc/loop/dispatch.go` (extend callChatWithTools to loop on tool calls)
- Modify: `internal/npc/loop/loop_test.go` (add tool round-trip test)

- [ ] **Step 1: Write failing test**

```go
func TestLoop_ToolCallRoundTrip(t *testing.T) {
	// Setup: fake backend programmed to return a ToolCall for
	// "say_loud" with arg {"text": "FIRE"} on first call, then a
	// plain Content "Done." on the second call (after the tool
	// returns). ToolRegistry has "say_loud" that returns
	// {ok=true, result="OK"}. Expect:
	//   - registry.Invoke called once with the right args
	//   - second Chat sees a RoleTool message with the result
	//   - final broadcast is "Done."
}
```

Engineer: write the body. The fake backend needs to support sequenced responses. Extend `internal/llm/fake/fake.go` to allow `f.Queue("response", []llm.Response{...})` if not already supported.

- [ ] **Step 2: Replace callChatWithTools with loop body**

```go
func (l *Loop) callChatWithTools(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef, depth int) (llm.Response, error) {
	for {
		if l.Budget != nil {
			const estimate = 256
			if !l.Budget.Allow(l.NPCID, estimate) {
				return llm.Response{}, errBudgetExhausted
			}
		}
		resp, err := l.LLM.Chat(ctx, msgs, tools, llm.ChatOpts{Model: l.ChatModel})
		if err != nil {
			return resp, err
		}
		if l.Budget != nil {
			l.Budget.Record(l.NPCID, resp.UsageIn, resp.UsageOut)
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		if depth >= l.MaxToolDepth {
			l.Logger.Warn("npc loop: tool-call depth exceeded",
				"npc", l.NPCName, "depth", depth)
			return resp, nil
		}
		// Append the assistant's tool-call turn so the model sees
		// its own tool calls in the next round.
		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		for _, tc := range resp.ToolCalls {
			result, err := l.invokeTool(ctx, tc)
			content := encodeToolResult(result, err)
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Name:       tc.Name,
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
		depth++
	}
}

func (l *Loop) invokeTool(ctx context.Context, tc llm.ToolCall) (map[string]any, error) {
	if l.Tools == nil || l.Pool == nil {
		return nil, fmt.Errorf("npc loop: tool registry not configured")
	}
	allowed := false
	for _, name := range l.ToolNames {
		if name == tc.Name {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("npc loop: tool %q not in NPC allow-list", tc.Name)
	}
	return l.Tools.Invoke(ctx, l.Pool, tc.Name, tc.Arguments)
}

func encodeToolResult(result map[string]any, err error) string {
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error())
	}
	out, jerr := json.Marshal(result)
	if jerr != nil {
		return fmt.Sprintf(`{"ok":false,"error":"marshal: %s"}`, jerr.Error())
	}
	return string(out)
}
```

Add `"encoding/json"` import.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/npc/loop/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/npc/loop/ internal/llm/fake/
git commit -m "M7: NPC loop tool-call dispatch with bounded depth"
```

---

### Task 9: Loop — budget exhaustion test (Manager integration)

**Files:**
- Modify: `internal/npc/loop/loop_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestLoop_BudgetExhausted_SkipsResponse(t *testing.T) {
	// Setup: Manager seeded with a budget where minute_used = minute_limit.
	// Publish a Say event. Expect: response model NEVER called; no broadcast.

	store := store.NewMemoryDB(t)  // or whatever helper exists
	m := budget.NewManager(store, budget.Defaults{Minute: 100, Hour: 1000, Day: 10000})
	// Pre-load a budget at the limit:
	m.Record(npcID, 100, 0) // burns minute window

	// (Engineer: instantiate Loop with this Manager; assert no
	// response-model invocation via fake counter.)
}
```

- [ ] **Step 2: Verify the dispatch.go budget checks already implement this**

The `defaultTick` already calls `l.Budget.Allow(l.NPCID, gateBudgetEstimate)`. With a 100-token minute limit and 100 used, this returns false and the tick exits early. Just confirm by reading the code; no implementation change needed.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/npc/loop/... ./internal/llm/budget/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/npc/loop/
git commit -m "M7: NPC loop budget-exhaustion path test"
```

---

### Task 10: Wire Registry to lifecycle-manage per-NPC loops; replace HandleSay direct dispatch

**Files:**
- Create: `internal/npc/loop/manager.go`
- Modify: `internal/npc/registry.go`
- Modify: `cmd/wintermute/main.go`

- [ ] **Step 1: Write manager.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package loop

import (
	"context"
	"sync"

	"github.com/vaelen/wintermute/internal/world/events"
)

// Manager owns the per-NPC Loop goroutines. The npc.Registry calls
// Add/Remove on rebuild; Stop drains everything at shutdown.
type Manager struct {
	mu     sync.Mutex
	loops  map[events.ObjectID]*loopHandle
	wg     sync.WaitGroup
	parent context.Context
}

type loopHandle struct {
	loop   *Loop
	cancel context.CancelFunc
}

func NewManager(parent context.Context) *Manager {
	return &Manager{
		loops:  map[events.ObjectID]*loopHandle{},
		parent: parent,
	}
}

// Add starts a Loop for npcID. Replaces any prior loop for the same id
// (used when an NPC moves rooms — manager.Replace handles the
// sub/unsub).
func (m *Manager) Add(npcID events.ObjectID, l *Loop) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.loops[npcID]; ok {
		old.cancel()
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.loops[npcID] = &loopHandle{loop: l, cancel: cancel}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		l.Run(ctx)
	}()
}

// Remove stops the loop for npcID. No-op if not present.
func (m *Manager) Remove(npcID events.ObjectID) {
	m.mu.Lock()
	h, ok := m.loops[npcID]
	delete(m.loops, npcID)
	m.mu.Unlock()
	if ok {
		h.cancel()
	}
}

// Stop cancels every loop and waits for them to exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	for id, h := range m.loops {
		h.cancel()
		delete(m.loops, id)
	}
	m.mu.Unlock()
	m.wg.Wait()
}
```

- [ ] **Step 2: Modify registry.go — start a Loop per NPC in rebuild**

After the existing memory.Worker startup in `rebuild` (around line 240–261), add:

```go
// Start (or replace) a tick loop per loaded NPC. The loop manager
// owns the goroutines; this rebuild path stops any prior loops and
// starts fresh ones for the new NPC set.
if r.loopMgr != nil {
    r.loopMgr.Stop()
}
r.loopMgr = loop.NewManager(r.rootCtx)
for _, n := range byID {
    if n.llm == nil { continue }
    roomID := r.world.LocationOf(n.ObjectID)
    // … look up room id from world; new helper if needed …
    l := &loop.Loop{
        RoomID:       events.RoomID(roomID),
        Bus:          r.bus,
        Debounce:     r.config.Debounce,
        NPCID:        events.ObjectID(n.ObjectID),
        NPCName:      n.Name,
        Persona:      n.Persona,
        LLM:          n.llm,
        ChatModel:    n.Model,
        GateModel:    n.GateModel,
        Memory:       n.Memory,
        Budget:       r.budgetMgr,
        Tools:        r.tools,
        Pool:         r.luaPool,
        ToolNames:    n.ToolNames,
        MaxToolDepth: r.config.MaxToolDepth,
        World:        r.world,
        Logger:       r.logger.With("npc", n.Name),
    }
    r.loopMgr.Add(events.ObjectID(n.ObjectID), l)
}
```

Add new Registry fields:
```go
bus       events.Bus
budgetMgr *budget.Manager
tools     *lua.ToolRegistry
luaPool   *lua.Pool
loopMgr   *loop.Manager
config    LoopConfig
```

Where `LoopConfig` is a new struct in `registry.go`:
```go
type LoopConfig struct {
    Debounce     time.Duration
    MaxToolDepth int
}
```

Update `Load` signature to accept these dependencies (bus, budget, tools, pool, config).

- [ ] **Step 3: Replace HandleSay's dispatch with bus publish**

In `HandleSay`, REMOVE the goroutine-spawn / `r.dispatch(...)` block. Replace with:

```go
func (r *Registry) HandleSay(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
    text = strings.TrimSpace(text)
    if text == "" {
        return
    }
    // Engagement-aware brush-off must still apply: a busy NPC emits a
    // brush-off line and does NOT receive the say into its loop.
    if r.engage != nil {
        for _, n := range r.NPCsInRoom(roomID) {
            if eng := r.engage.HostEngagement(n.ObjectID); eng != nil {
                if !engagedWithSpeaker(eng, speakerID) {
                    msg := fmt.Sprintf("%s raises a finger to %s — \"one moment.\"\r\n",
                        n.Name, speakerName)
                    r.world.BroadcastToRoom(roomID, 0, msg)
                }
            }
        }
    }
    // The world layer already published a KindSay event for this Say.
    // The per-NPC Loop picks it up via its subscription. Nothing more
    // to do here.
    _ = speakerID
}
```

DELETE the `dispatch` function entirely. DELETE `renderMemoryContext` from registry.go (it now lives in loop/dispatch.go). DELETE `dispatchTimeout`, `retrievalK`, `retrievalThreshold` constants if unused.

- [ ] **Step 4: Update Shutdown**

In `Registry.Shutdown`, after the inFlightWG wait, before the memory drain, stop the loop manager:

```go
if r.loopMgr != nil {
    r.loopMgr.Stop()
}
```

- [ ] **Step 5: Update cmd/wintermute/main.go**

Where the registry is constructed, plumb in the new dependencies:

```go
budgetMgr := budget.NewManager(db, budget.Defaults{Minute: 5000, Hour: 100000, Day: 1000000})
if err := budgetMgr.Load(ctx); err != nil {
    return fmt.Errorf("load budgets: %w", err)
}
go func() {
    if err := budgetMgr.RunFlushLoop(ctx, 30*time.Second); err != nil {
        logger.Warn("budget flush loop ended", "err", err)
    }
}()

npcReg, err := npc.Load(ctx, db, w, cfg.LLM.Default, logger,
    npc.LoopDeps{
        Bus:    bus,
        Budget: budgetMgr,
        Tools:  luaAPI.Tools,
        Pool:   luaAPI.Pool,
        Config: npc.LoopConfig{
            Debounce:     cfg.NPC.Loop.Debounce,
            MaxToolDepth: cfg.NPC.Loop.MaxToolDepth,
        },
    },
)
```

`LoopDeps` is a new struct on the `npc` package that bundles the new constructor arguments. Existing tests that call `npc.Load` should pass a zero `LoopDeps{}` — the loop just won't fire (Budget nil, Bus nil), which the loop already handles.

- [ ] **Step 6: Run tests**

Run: `go test ./...`
Expected: PASS. Existing M3 say tests should still see the eventual `NPCSay` broadcast (now via the Loop).

If any tests need the loop to actually fire to observe the broadcast, the loop's Debounce defaults to 0 in zero-value `LoopConfig`; pick a small value (e.g. 10ms) and add a wait in those tests.

- [ ] **Step 7: Commit**

```bash
git add internal/npc/ cmd/wintermute/main.go
git commit -m "M7: per-NPC loop lifecycle; HandleSay becomes bus publisher"
```

---

### Task 11: NPC config — tools column read; sample tool

**Files:**
- Modify: `internal/npc/registry.go` (read `tools` column in rebuild)
- Modify: `internal/npc/npc.go` (add `ToolNames []string`)
- Modify: `internal/store/migrations/0005_seed_npc.sql` (or wherever NPC seed data lives) — give the kiosk NPC an empty `tools` value (no test data churn).

- [ ] **Step 1: Add `ToolNames []string` to NPC struct in npc.go**

```go
ToolNames []string
```

- [ ] **Step 2: Read the column in registry.rebuild**

Add `tools` to the SELECT:

```go
`SELECT c.object_id, o.name, c.persona, c.backend, c.backend_opts,
        COALESCE(c.chat_model, ''), COALESCE(c.gate_model, ''),
        c.max_context, COALESCE(c.tools, '')`
```

Add `tools string` to the scan vars; scan it; split on comma:

```go
var toolsRaw string
// ...
if err := rows.Scan(..., &toolsRaw); err != nil { ... }

var toolNames []string
for _, name := range strings.Split(toolsRaw, ",") {
    name = strings.TrimSpace(name)
    if name != "" {
        toolNames = append(toolNames, name)
    }
}
n.ToolNames = toolNames
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/npc/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/npc/
git commit -m "M7: read npc_config.tools as comma-separated list"
```

---

### Task 12: Scheduler — npc_goals reader and one-shot/daily dispatch

**Files:**
- Create: `internal/npc/schedule/scheduler.go`
- Create: `internal/npc/schedule/scheduler_test.go`
- Modify: `internal/script/lua/npc.go` (or wherever Lua npc bindings live; create the file if absent) — add `wintermute.npc.schedule`
- Modify: `cmd/wintermute/main.go` — start the scheduler goroutine

- [ ] **Step 1: Write scheduler.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package schedule fires npc_goals as KindSched events into the
// per-room events.Bus when their fire_at timestamp arrives.
package schedule

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/events"
)

// Scheduler reads npc_goals and fires them. One goroutine; not
// horizontally scaled. Cron-format recurrence is documented but only
// "daily" and "hourly" recurrences are honoured in M7.
type Scheduler struct {
	db     *store.DB
	world  *world.World
	bus    events.Bus
	logger *slog.Logger
}

func New(db *store.DB, w *world.World, bus events.Bus, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{db: db, world: w, bus: bus, logger: logger}
}

// Run polls the earliest pending goal on each iteration and waits for
// it. The minimum polling cadence is one minute (we sleep until the
// next earliest fire_at or one minute, whichever is shorter). Cancels
// cleanly on ctx done.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		nextAt, ok := s.peek(ctx)
		var wait time.Duration
		switch {
		case !ok:
			wait = time.Minute
		case time.Now().After(nextAt):
			wait = 0
		default:
			wait = time.Until(nextAt)
			if wait > time.Minute {
				wait = time.Minute
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			s.fireDue(ctx)
		}
	}
}

type goal struct {
	id        int64
	npcID     world.ObjectID
	fireAt    time.Time
	goal      string
	recurring string
}

func (s *Scheduler) peek(ctx context.Context) (time.Time, bool) {
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT fire_at FROM npc_goals ORDER BY fire_at ASC LIMIT 1`)
	var t int64
	if err := row.Scan(&t); err != nil {
		return time.Time{}, false
	}
	return time.Unix(t, 0), true
}

func (s *Scheduler) fireDue(ctx context.Context) {
	now := time.Now().Unix()
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, npc_id, fire_at, goal, COALESCE(recurring, '')
		   FROM npc_goals
		  WHERE fire_at <= ?
		  ORDER BY fire_at ASC`, now)
	if err != nil {
		s.logger.Warn("scheduler: query due goals failed", "err", err)
		return
	}
	defer rows.Close()
	var due []goal
	for rows.Next() {
		var g goal
		var npcID, fireAt int64
		if err := rows.Scan(&g.id, &npcID, &fireAt, &g.goal, &g.recurring); err != nil {
			s.logger.Warn("scheduler: scan goal failed", "err", err)
			continue
		}
		g.npcID = world.ObjectID(npcID)
		g.fireAt = time.Unix(fireAt, 0)
		due = append(due, g)
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("scheduler: iterate goals failed", "err", err)
		return
	}
	for _, g := range due {
		s.fire(ctx, g)
	}
}

func (s *Scheduler) fire(ctx context.Context, g goal) {
	loc, err := s.world.LocationOf(g.npcID)
	if err != nil || loc.RoomID == 0 {
		s.logger.Warn("scheduler: NPC has no room, dropping goal",
			"goal_id", g.id, "npc_id", g.npcID, "err", err)
		s.delete(ctx, g.id)
		return
	}
	s.bus.Publish(events.Event{
		Kind:   events.KindSched,
		RoomID: int64(loc.RoomID),
		Actor:  int64(g.npcID),
		Text:   g.goal,
		At:     time.Now(),
	})
	next, ok := nextRecurrence(g.fireAt, g.recurring)
	if ok {
		s.reschedule(ctx, g.id, next)
		return
	}
	s.delete(ctx, g.id)
}

func nextRecurrence(prev time.Time, recurring string) (time.Time, bool) {
	switch recurring {
	case "daily":
		return prev.Add(24 * time.Hour), true
	case "hourly":
		return prev.Add(time.Hour), true
	}
	return time.Time{}, false
}

func (s *Scheduler) reschedule(ctx context.Context, id int64, next time.Time) {
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_goals SET fire_at = ? WHERE id = ?`,
			next.Unix(), id)
		return err
	})
}

func (s *Scheduler) delete(ctx context.Context, id int64) {
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM npc_goals WHERE id = ?`, id)
		return err
	})
}
```

- [ ] **Step 2: Write scheduler_test.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package schedule

// Engineer: write two table-style tests:
//   1) one-shot goal fires and is then deleted
//   2) daily recurring goal fires and is rescheduled +24h
//
// Use the same DB/world test helpers as the integration suite.
// Subscribe a channel to the bus, insert a goal with fire_at in the
// past, call s.fireDue, and assert the bus emitted KindSched.
```

(This is intentionally a high-level skeleton — engineer fills the body using the same `internal/store/storetest` helpers other tests use.)

- [ ] **Step 3: Add the Lua binding**

In the lua API file that registers the `wintermute.npc.*` namespace (find with `grep -rn 'wintermute.npc' internal/script/lua/`), add a binding:

```go
func (a *API) luaNPCSchedule(L *lua.LState) int {
    npcID := L.CheckInt64(1)
    opts := L.CheckTable(2)
    fireAt := optInt64(opts, "fire_at")
    goal := optString(opts, "goal")
    recurring := optString(opts, "recurring")
    if goal == "" {
        return pushError(L, fmt.Errorf("wintermute: invalid_argument: goal required"))
    }
    if fireAt == 0 {
        return pushError(L, fmt.Errorf("wintermute: invalid_argument: fire_at required"))
    }
    err := a.npcSchedule(context.Background(), world.ObjectID(npcID), fireAt, goal, recurring)
    if err != nil {
        return pushError(L, err)
    }
    L.Push(lua.LBool(true))
    return 1
}
```

`a.npcSchedule` writes a row through `db.Write`. Register on the `wintermute.npc` table at API init time alongside other npc bindings.

- [ ] **Step 4: Start the scheduler in main.go**

```go
sch := schedule.New(db, w, bus, logger)
go sch.Run(ctx)
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/npc/schedule/... ./internal/script/lua/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/npc/schedule/ internal/script/lua/ cmd/wintermute/main.go
git commit -m "M7: NPC goal scheduler + wintermute.npc.schedule Lua binding"
```

---

### Task 13: Memory salience decay daily job

**Files:**
- Create: `internal/npc/memory/decay.go`
- Create: `internal/npc/memory/decay_test.go`
- Modify: `cmd/wintermute/main.go`

- [ ] **Step 1: Write decay.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/vaelen/wintermute/internal/store"
)

// DecayFactor is the multiplicative drop applied to every memory's
// salience per daily tick. 0.95 means a memory loses ~5% of its weight
// per day; combined with M4's salience-bump on retrieval, frequently-
// referenced memories stay high while ignored ones decay below the
// gc-eligible floor.
const DecayFactor = 0.95

// DecayFloor is the salience threshold below which a memory becomes
// eligible for deletion. M7 only marks; @gc-memories deletes.
const DecayFloor = 0.1

// DecayJob runs DecayOnce every 24 hours until ctx is done. The first
// tick fires `wait` after Run is invoked.
type DecayJob struct {
	DB     *store.DB
	Logger *slog.Logger
	Wait   time.Duration
}

func (j *DecayJob) Run(ctx context.Context) {
	logger := j.Logger
	if logger == nil {
		logger = slog.Default()
	}
	wait := j.Wait
	if wait == 0 {
		wait = 24 * time.Hour
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := DecayOnce(ctx, j.DB); err != nil {
				logger.Warn("memory decay: tick failed", "err", err)
			}
			t.Reset(wait)
		case <-ctx.Done():
			return
		}
	}
}

func DecayOnce(ctx context.Context, db *store.DB) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_memories SET salience = salience * ?`,
			DecayFactor)
		return err
	})
}
```

- [ ] **Step 2: Write decay_test.go**

```go
// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package memory

// Engineer: write a test that:
//   1) Opens an in-memory DB with the migrations applied.
//   2) Inserts a memory with salience=1.0.
//   3) Calls DecayOnce.
//   4) Asserts the row's salience is now 0.95 (within epsilon).
//   5) Calls DecayOnce 10 more times; asserts salience drops below 0.6.
```

- [ ] **Step 3: Start the job in main.go**

```go
go (&memory.DecayJob{DB: db, Logger: logger, Wait: 24 * time.Hour}).Run(ctx)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/npc/memory/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/npc/memory/ cmd/wintermute/main.go
git commit -m "M7: daily memory-salience decay job"
```

---

### Task 14: Admin commands — @npc-debug and @gc-memories

**Files:**
- Modify: wherever the admin command dispatch lives (find with `grep -rn 'npcreload' internal/world/`). The existing `@npcreload` command is the closest precedent — clone its registration site.

- [ ] **Step 1: Find the admin command handler**

```bash
grep -rn '@npcreload\|"@reload"\|cmdAdmin' internal/world/cmd/ internal/world/api/
```

Identify the command-dispatch table. Add two entries.

- [ ] **Step 2: Implement @npc-debug**

```go
// In admin.go (or wherever):

func (h *Handler) cmdNPCDebug(p *world.Presence, args []string) error {
    if len(args) == 0 {
        return h.writeln(p, "usage: @npc-debug <npc-slug-or-id>")
    }
    n := h.npcReg.Lookup(args[0])  // helper that resolves slug or numeric id
    if n == nil {
        return h.writeln(p, "unknown NPC")
    }
    // Print: persona snippet, gate model, chat model, tool names,
    // current budgets (from BudgetMgr), last 5 observations (from
    // loop's snapshot — Loop will need a Snapshot() method).
    // ...
}
```

Add `Loop.Snapshot()` to `internal/npc/loop/loop.go`:

```go
type Snapshot struct {
    NPCName       string
    RoomID        events.RoomID
    Observations  []events.Event // last N, defensively copied
    LastReply     string
}

func (l *Loop) Snapshot() Snapshot {
    l.snapMu.Lock(); defer l.snapMu.Unlock()
    out := Snapshot{NPCName: l.NPCName, RoomID: l.RoomID, LastReply: l.lastReply}
    out.Observations = append([]events.Event(nil), l.recentObs...)
    return out
}
```

Maintain `l.recentObs` (capped at 16) and `l.lastReply` inside `defaultTick`.

- [ ] **Step 3: Implement @gc-memories**

```go
func (h *Handler) cmdGCMemories(p *world.Presence, args []string) error {
    n, err := h.db.Write(ctx, func(tx *sql.Tx) error {
        _, err := tx.ExecContext(ctx,
            `DELETE FROM npc_memories WHERE salience < ?`,
            memory.DecayFloor)
        return err
    })
    if err != nil {
        return h.writeln(p, fmt.Sprintf("gc failed: %s", err))
    }
    return h.writeln(p, fmt.Sprintf("gc complete; deleted %d memories", n))
}
```

(Adjust signature to match the existing `db.Write` return contract.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/world/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/world/ internal/npc/loop/
git commit -m "M7: admin commands @npc-debug and @gc-memories"
```

---

### Task 15: Config knobs

**Files:**
- Modify: `internal/config/config.go`
- Modify: `wintermute.toml` (if a sample exists) — add `[npc.loop]` block

- [ ] **Step 1: Add config struct**

In `internal/config/config.go`:

```go
type Config struct {
    // ...existing fields...
    NPC NPCConfig `toml:"npc"`
}

type NPCConfig struct {
    Loop LoopConfig `toml:"loop"`
}

type LoopConfig struct {
    Debounce       time.Duration `toml:"debounce"`
    MaxToolDepth   int           `toml:"max_tool_depth"`
    DefaultMinute  int           `toml:"default_minute_limit"`
    DefaultHour    int           `toml:"default_hour_limit"`
    DefaultDay     int           `toml:"default_day_limit"`
    DefaultGateModel string      `toml:"default_gate_model"`
}

func (c *Config) ApplyDefaults() {
    if c.NPC.Loop.Debounce == 0 {
        c.NPC.Loop.Debounce = 800 * time.Millisecond
    }
    if c.NPC.Loop.MaxToolDepth == 0 {
        c.NPC.Loop.MaxToolDepth = 3
    }
    if c.NPC.Loop.DefaultMinute == 0 {
        c.NPC.Loop.DefaultMinute = 5000
    }
    if c.NPC.Loop.DefaultHour == 0 {
        c.NPC.Loop.DefaultHour = 100000
    }
    if c.NPC.Loop.DefaultDay == 0 {
        c.NPC.Loop.DefaultDay = 1000000
    }
}
```

Call `c.ApplyDefaults()` at the end of the existing config-load path (wherever defaults are merged today — match the file's existing pattern).

- [ ] **Step 2: Sample TOML block**

If `wintermute.toml.example` (or similar) exists:

```toml
[npc.loop]
debounce = "800ms"
max_tool_depth = 3
default_minute_limit = 5000
default_hour_limit = 100000
default_day_limit = 1000000
default_gate_model = "llama3.2:1b"
```

- [ ] **Step 3: Pipe config into Manager construction in main.go**

```go
budgetMgr := budget.NewManager(db, budget.Defaults{
    Minute: cfg.NPC.Loop.DefaultMinute,
    Hour:   cfg.NPC.Loop.DefaultHour,
    Day:    cfg.NPC.Loop.DefaultDay,
})
```

And update LoopConfig wired into registry to read from `cfg.NPC.Loop`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/... ./cmd/wintermute/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/ cmd/wintermute/ wintermute.toml.example
git commit -m "M7: [npc.loop] config block with sensible defaults"
```

---

### Task 16: Integration test — two NPCs, adjacent rooms, tool call, budget exhaustion

**Files:**
- Create: `internal/integration/m7_npc_loop_test.go`

- [ ] **Step 1: Write the scenario**

The test should:

1. Spin up a server with the fake LLM backend (two NPCs: `bartender` in `bar`, `cook` in `kitchen`).
2. Programme the bartender's responses:
   - Gate model: "YES" for any input that mentions "beer", else "NO".
   - Response model: tool-call `serve_drink({"drink":"beer","to_player":<id>})` on first call, then `"Coming right up."` on the followup after the tool returns `{ok=true}`.
3. Register a `serve_drink` Lua tool that moves a drink object into the player's inventory and returns `{ok=true}`.
4. Two simulated player sessions connect via telnet (the same harness existing M3 integration tests use).
5. Player A in `bar` says `"I'll have a beer."` — assert:
   - The bartender's response model is called once.
   - The tool is invoked exactly once.
   - The drink moves into Player A's inventory.
   - The bartender broadcasts "Coming right up." to the bar room.
   - Player B in `kitchen` sees neither the say nor the reply (per-room isolation).
6. Set the bartender's budget to a near-zero minute limit. Have Player A say "I'll have another beer." — assert the response model is NOT called.

- [ ] **Step 2: Reuse existing integration helpers**

Look at `internal/integration/*_test.go` (e.g. memory or engagement integration tests). They already construct a real `World`, `npc.Registry`, etc. The M7 test reuses that scaffold and adds the new dependencies (`bus`, `budget.Manager`, `lua.ToolRegistry`).

- [ ] **Step 3: Run**

Run: `go test -tags test ./internal/integration/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/integration/
git commit -m "M7: two-NPC adjacent-room integration test (per-room isolation, tool round-trip, budget exhaustion)"
```

---

### Task 17: Full verification + acceptance walkthrough

- [ ] **Step 1: Run the full suite**

Run: `go test ./...`
Expected: every package PASS. Note that the `TestGenerateResetToken_Format` test is flaky at baseline; if it fails standalone, isolate-and-rerun once.

- [ ] **Step 2: Verify SPDX headers on every new file**

Run: `make check-headers` (if `make` is available) or:

```bash
git diff --name-only main...HEAD -- '*.go' '*.sql' '*.toml' | \
  xargs -I{} sh -c 'head -2 "{}" | grep -q "SPDX-License-Identifier: MIT" || echo "missing header: {}"'
```

Expected: no output.

- [ ] **Step 3: Lint**

Run: `make lint` (or `golangci-lint run ./...`).
Expected: clean.

- [ ] **Step 4: Walk acceptance criteria from `docs/milestones/07-autonomous-npc-loop.md`**

For each criterion 1–8 in the milestone doc, identify the test or code path that satisfies it. Add a short paragraph in the eventual PR description:

| AC | Test | Notes |
|---|---|---|
| 1. Idle NPC → only gate-model tokens | `m7_npc_loop_test.go` `TestIdleNPC_OnlyGateTokens` | Subscribe budget Manager, assert minute_used == gate cost |
| 2. Busy room → persona-respecting response | `m7_npc_loop_test.go` Player-A flow | Fake backend scripted; assert broadcast text |
| 3. Tool invocation moves world state | `m7_npc_loop_test.go` Player-A "beer" flow | Assert object moved |
| 4. Budget exhaustion → degraded path | `m7_npc_loop_test.go` budget-exhausted flow | Asserts no response-model call |
| 5. Scheduled goal fires | `internal/npc/schedule/scheduler_test.go` | Subscribe bus; assert KindSched event |
| 6. Daily salience decay | `internal/npc/memory/decay_test.go` | `DecayOnce` arithmetic check |
| 7. M3 addressed-only still works | Existing `internal/npc/...` tests + new flow | Loop subscribes and dispatches the same broadcast |
| 8. SPDX headers on every new file | Step 2 above | |

- [ ] **Step 5: Update the project plan status table**

In `docs/plan.md`, change milestone 07's Status column from "not started" to "shipped". This is small but it keeps `/show me the remaining milestones` honest.

- [ ] **Step 6: Final commit**

```bash
git add docs/plan.md
git commit -m "M7: mark milestone shipped in docs/plan.md"
```

- [ ] **Step 7: Push and open PR**

Use the `commit-push-pr` skill or:

```bash
git push -u origin worktree-M7-autonomous-npc-loop
gh pr create --title "M7: semi-autonomous NPC loop" --body "$(cat <<'EOF'
## Summary
- Per-room `events.Bus` with channel fan-out; mutations publish structured events alongside existing string broadcasts.
- Per-NPC `npc.loop.Loop` goroutine: subscribe → debounce → gate model → response model with tool calls.
- `llm/budget` token tracker with minute/hour/day windows, persisted to `npc_budgets`.
- Scheduler reads `npc_goals` and fires synthetic `KindSched` events; supports one-shot, daily, hourly.
- Daily memory-salience decay job; `@gc-memories` admin command to prune.
- `@npc-debug <npc>` admin command for tuning.
- M3 `Registry.HandleSay` becomes a thin publisher; all dispatch logic now lives in the Loop.

## Test plan
- [ ] `go test ./...` is green.
- [ ] `make check-headers` is green.
- [ ] `make lint` is green.
- [ ] Manual smoke with Ollama: two players in `bar`, bartender chimes in based on overheard conversation; budget counters in `npc_budgets` advance.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage (against `docs/milestones/07-autonomous-npc-loop.md`):**

| Spec item | Plan task |
|---|---|
| Event bus | Tasks 2, 3 |
| NPC tick goroutine + debounce | Tasks 5, 10 |
| Two-tier routing (gate then response) | Tasks 6, 7 |
| Tool execution via M5 registry | Task 8 |
| Per-NPC token budgets | Tasks 4, 9 |
| Scheduled goals | Task 12 |
| Memory salience decay | Task 13 |
| `npc_budgets` schema | Task 1 |
| `npc_goals` schema | Task 1 |
| `npc_config.tools` column | Task 1 + Task 11 |
| Replace M3 HandleSay | Task 10 |
| `@npc-debug` admin command | Task 14 |
| `@gc-memories` admin command | Task 14 |
| Config knobs | Task 15 |
| Integration test | Task 16 |
| Acceptance criteria walkthrough | Task 17 |

**Placeholder scan:** Tasks 12 step 2, 13 step 2, 14 step 2 each say "Engineer: write …" for test bodies that need real fixture-helper choices the engineer has more context for than the plan can encode here. These are explicit "fill in using existing patterns" prompts, not unspecified work — each names the helper file to copy from and the asserts to make. If a more rigid plan is needed for those steps, expand them in-flight.

**Type consistency:** `events.RoomID` and `events.ObjectID` are type aliases of `int64` (Step 1a of Task 3). `world.RoomID` and `world.ObjectID` are named `int64`-based types; an explicit cast at the publish boundary is required. The plan honours this everywhere it publishes (`int64(roomID)`, `int64(speakerID)`, etc.). `Loop.NPCID` is `events.ObjectID` — bridge via `events.ObjectID(n.ObjectID)` (Task 10).

**Migrations:** 0024 and 0025 confirmed available (0023 is the last existing). The milestone doc's stale 0014/0015 references are fixed in Task 1 step 4.

**Risks:** Task 3's bus-from-mutations changes touch every mutation method. Keep the patch small per mutation (one extra `if w.bus != nil { ... }` block). If the diff churns existing M2 tests, the bus is nil by default in those tests and they should be unaffected; if not, the assumption is wrong and the plan needs a pre-task to thread bus into the test helpers.
