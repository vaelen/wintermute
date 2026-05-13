# Milestone 04 — NPC memory

## Goal

NPCs remember the gist of past conversations across server restarts. A returning player addressing the bartender hears "good to see you again — last time you mentioned looking for the Sprawl gate" rather than a cold greeting. Memory is built from asynchronous summarization of completed conversations and surfaced via embedding-similarity retrieval.

## Dependencies

- M3 (reactive NPCs, LLM interface).

## Scope

- Short-term memory: in-process per-NPC ring buffer of the last ~20 turns. Wiped on restart.
- Long-term memory: SQLite table of conversation summaries with embeddings, queried via `sqlite-vec`.
- Summarization worker: an async goroutine pool that takes completed transcripts, generates a summary + embedding via Ollama, and writes them to SQLite.
- Retrieval: before each NPC LLM call, embed the current observation buffer, fetch top-K memories above a similarity threshold, splice them into the system context between persona and short-term history.
- `sqlite-vec` extension loaded at DB open; safe fallback (log + disable retrieval) if loading fails.
- Per-NPC salience: a simple "this memory was useful" counter incremented when a memory is retrieved and the resulting response is delivered. Used for tie-breaking and eventual decay.

## Out of scope

- Structured facts ("knows player X bought item Y") — would need an extraction pipeline. Deferred.
- Memory decay/forgetting beyond salience tie-breaking. Deferred.
- Cross-NPC memory sharing. Deferred.
- Hosted embedding APIs — Ollama only.

## Architecture

### Packages introduced

- `internal/npc/memory` — short-term ring buffer, summarization worker, retrieval, all the memory plumbing.
- `internal/store/vec` — thin helpers around `sqlite-vec`: load the extension, encode/decode `[]float32` to/from the BLOB format the extension expects, helpers for `vec_search`.

### Short-term memory

```go
// internal/npc/memory/shortterm.go
type Turn struct {
    Speaker string   // player name or "<npc>" or "<system>"
    Text    string
    At      time.Time
}

type ShortTerm struct {
    mu     sync.Mutex
    turns  []Turn
    cap    int
}

func (s *ShortTerm) Append(t Turn)
func (s *ShortTerm) Recent(n int) []Turn
func (s *ShortTerm) Drain() []Turn   // takes everything, leaves buffer empty
```

Owned by the `NPC` struct. Wiped on restart.

### Long-term memory

```go
// internal/npc/memory/longterm.go
type Memory struct {
    ID        int64
    NPCID     world.ObjectID
    Summary   string
    Embedding []float32
    CreatedAt time.Time
    Salience  float64
}

type Store interface {
    Insert(ctx, m Memory) (int64, error)
    Search(ctx, npcID world.ObjectID, queryVec []float32, k int, threshold float64) ([]Memory, error)
    BumpSalience(ctx, id int64) error
}
```

### Summarization worker

```go
// internal/npc/memory/worker.go
type SummaryJob struct {
    NPCID    world.ObjectID
    Turns    []Turn
}

type Worker struct {
    jobs    chan SummaryJob
    llm     llm.LLM             // the NPC's own backend
    embed   llm.LLM             // typically the same client; configurable
    summarizerModel string      // small/cheap model
    embedModel      string
    store   Store
}

func (w *Worker) Submit(j SummaryJob)
func (w *Worker) Run(ctx)
```

- One worker per NPC keeps ordering simple (no concurrent summarization for the same NPC). Total goroutine cost is O(npcs).
- Jobs are submitted when a conversation is considered "ended": no `say` events directed at the NPC for a configurable idle period (default 5 minutes), OR the player leaves the room, OR the buffer is about to overflow (drain-then-summarize).
- Summarizer prompt is a fixed template that asks for a 2–3 sentence summary in third person, focused on facts and intents.

### Retrieval

Before each NPC turn (in M3's `HandleSay`):

1. Build a query string from the recent short-term buffer (last few turns).
2. Embed via the configured embedding model.
3. `Store.Search(npcID, vec, k=5, threshold=0.7)`.
4. Splice results into the context as a system message:
   ```
   Relevant memories from past conversations:
   - <summary 1>
   - <summary 2>
   ...
   ```
5. After the assistant response is delivered, call `BumpSalience` on each retrieved memory.

### Extension loading

`internal/store/vec.Load(conn)` calls `sqlite_load_extension` with the bundled `sqlite-vec` library (or a path from config). On failure: log a warning and set a package-level `Enabled = false`. The memory layer checks `vec.Enabled` and degrades gracefully (skip retrieval; still summarize and store, even if search doesn't work — at least the data accumulates).

## Schema changes

`internal/store/migrations/0006_memory.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE npc_memories (
    id          INTEGER PRIMARY KEY,
    npc_id      INTEGER NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    summary     TEXT NOT NULL,
    embedding   BLOB NOT NULL,
    created_at  INTEGER NOT NULL,
    salience    REAL NOT NULL DEFAULT 1.0
);

CREATE INDEX idx_npc_memories_npc ON npc_memories(npc_id);
```

`sqlite-vec` is loaded as a SQLite extension; it adds virtual-table machinery rather than altering the regular schema. The `embedding` BLOB is the format `sqlite-vec` expects (little-endian float32 packed). `internal/store/vec` provides the encode/decode helpers.

Search query (representative — exact `sqlite-vec` syntax to confirm at implementation time):

```sql
SELECT m.id, m.summary, m.created_at, m.salience,
       vec_distance_cosine(m.embedding, ?1) AS dist
FROM npc_memories m
WHERE m.npc_id = ?2
ORDER BY dist ASC
LIMIT ?3;
```

Filter results in Go against the similarity threshold (cosine distance → similarity = 1 - dist).

## Implementation tasks

1. Add migration `0006_memory.sql`.
2. Implement `internal/store/vec`: extension load, encode/decode helpers, a small `Search` helper.
3. Implement `internal/npc/memory/shortterm.go`.
4. Implement `internal/npc/memory/longterm.go` with `Store` against SQLite.
5. Implement `internal/npc/memory/worker.go`.
6. Extend the `NPC` struct (M3) with a `Memory` field holding `ShortTerm` + a reference to the worker + the store.
7. Hook short-term `Append` into `HandleSay`: append the player turn and the NPC turn.
8. Hook retrieval into `HandleSay`: embed the recent short-term buffer, fetch memories, prepend to LLM context as a system message.
9. Hook salience bump after a response is delivered (best-effort; ignore errors).
10. Hook conversation-end detection: a per-NPC idle timer (`time.AfterFunc`); reset on each new turn; on fire, drain short-term and submit to worker. Also drain on `Move` away from the room.
11. Implement a config knob for the summarizer model (e.g. `llama3.2:1b` by default) and embedding model (`nomic-embed-text`).
12. Bootstrap a worker pool at startup, one per loaded NPC.
13. On shutdown, drain remaining short-term buffers and wait for in-flight summarization (with a timeout) before closing.
14. Unit tests:
    - Encode/decode round-trip for float32 vectors.
    - Short-term ring buffer (append, drain, recent).
    - Retrieval with seeded embeddings against the fake backend.
15. Integration test: with the fake backend, run a scripted conversation, force a conversation-end, verify a memory row is written and retrievable on the next turn.
16. Integration test: end the conversation, restart the server, start a new conversation, verify the memory still influences retrieval (deterministic embeddings from the fake backend make this reliable).

## Testing

- `go test -tags test ./internal/npc/memory/... ./internal/store/vec/...`
- Scenario tests using the fake backend.
- Manual: with real Ollama and the bartender NPC, hold two separate conversations (separated by a `quit`/relogin), confirm the second reflects something from the first.

## Acceptance criteria

1. Ending a conversation (5 min idle, leaving the room, or server-restart drain) results in exactly one `npc_memories` row per ended conversation per NPC.
2. Reopening a conversation with the same NPC retrieves at least one prior memory and includes it in the LLM context.
3. Disabling `sqlite-vec` (rename the extension path in config) does not crash the server — retrieval is skipped, summarization continues.
4. After a restart, memories from before the restart influence new conversations.
5. Two NPCs in the same room maintain independent memory streams.
6. The summarization worker doesn't block the player's session — replies arrive at the same speed as in M3.

## Risks & open questions

- **Embedding consistency**: changing the embedding model later invalidates all stored embeddings. Track the model name in a metadata table (`memory_meta(npc_id, embedding_model)`) and either refuse to retrieve when models mismatch or re-embed lazily. Lean toward refusing + admin command to re-embed.
- **`sqlite-vec` shipping**: the extension is a single shared library. Ship it inside the binary's data dir (download on first run or vendor a copy per OS/arch). Decide at implementation time.
- **Summarization quality**: small models can hallucinate. Pin a tested prompt and consider few-shot examples in M7 if quality bites.
- **Idle detection edge case**: a player who logs out without a `quit` leaves the conversation in limbo. Mitigation: on session close, treat as conversation-end for all NPCs the player was actively engaged with.
- **Salience drift**: a single retrieval bumps salience by +1.0; without decay, ancient memories dominate. Add a daily decay job in M7 when the scheduler exists.
- **Embedding latency**: an embedding call on every `say` adds latency. If it becomes noticeable, cache the most-recent embedding and only re-embed when the short-term buffer changes meaningfully (e.g. ≥2 new turns).
