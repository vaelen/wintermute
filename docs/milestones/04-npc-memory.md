# Milestone 04 — NPC memory

## Goal

NPCs remember the gist of past conversations across server restarts. A returning player addressing the bartender hears "good to see you again — last time you mentioned looking for the Sprawl gate" rather than a cold greeting. Memory is built from asynchronous summarization of completed conversations and surfaced via embedding-similarity retrieval.

## Dependencies

- M3 (reactive NPCs, LLM interface).

## Scope

- Short-term memory: in-process per-NPC ring buffer of the last ~20 turns. Wiped on restart.
- Long-term memory: SQLite table of conversation summaries with embeddings, queried by brute-force cosine similarity in Go.
- Summarization worker: an async goroutine pool that takes completed transcripts, generates a summary + embedding via Ollama, and writes them to SQLite.
- Retrieval: before each NPC LLM call, embed the current observation buffer, fetch top-K memories above a similarity threshold, splice them into the system context between persona and short-term history.
- Vector search runs in Go, not via a SQLite extension. The pure-Go `modernc.org/sqlite` driver this project uses cannot load native C extensions, and CLAUDE.md forbids CGo, so `sqlite-vec` is not available. `internal/store/vec` stores embeddings in the same little-endian float32 BLOB format `sqlite-vec` expects on disk (forward-compatible) and exposes a `CosineSimilarity` helper that the long-term store calls per row. A package-level `Enabled` flag is kept at `false` as a placeholder for the day a native loader becomes viable; nothing in M4 toggles it.
- Per-NPC salience: a simple "this memory was useful" counter incremented when a memory is retrieved and the resulting response is delivered. Used for tie-breaking and eventual decay.
- Disconnect-reason signalling: the session layer distinguishes purposeful `quit` from sudden socket drop and passes that through to `world.Detach`. The world layer broadcasts `"X goes to sleep."` for `quit` and `"X fell asleep."` for a dropped link (replacing today's single `"X falls asleep."` message). `"X wakes up."` on reconnect is unchanged.
- Conversation-end via room observation: NPCs subscribe to their room's broadcast stream and treat any leave/fell-asleep/goes-to-sleep event for an actively-engaged player as a conversation-end signal — drain the short-term buffer for that interlocutor, submit a summary job. The 5-minute idle timer is preserved as a fallback for forgotten conversations, not as the primary trigger.

## Out of scope

- Structured facts ("knows player X bought item Y") — would need an extraction pipeline. Deferred.
- Memory decay/forgetting beyond salience tie-breaking. Deferred.
- Cross-NPC memory sharing. Deferred.
- Hosted embedding APIs — Ollama only.

## Architecture

### Packages introduced

- `internal/npc/memory` — short-term ring buffer, summarization worker, retrieval, all the memory plumbing.
- `internal/store/vec` — encode/decode helpers for `[]float32` ↔ little-endian BLOB (the on-disk format `sqlite-vec` expects, kept forward-compatible) plus `CosineSimilarity`. Also exposes a package-level `Enabled` flag that stays `false` on this driver.

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
- Jobs are submitted when a conversation is considered "ended". The NPC tracks a small set of *actively engaged* interlocutors (players who have addressed it within the short-term window). A conversation ends — and the per-interlocutor slice of the short-term buffer is drained into a summary job — when any of the following is observed for an engaged player:
  - room broadcast `"X leaves <dir>."` (player moved away),
  - room broadcast `"X fell asleep."` (player's link dropped),
  - room broadcast `"X goes to sleep."` (player ran `quit`),
  - the short-term buffer is about to overflow (drain-then-summarize),
  - the fallback idle timer fires (default 5 minutes with no new turn from that player).
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

### Vector search

`internal/store/vec` is a small Go-only helper, not a wrapper around a native extension. `Encode`/`Decode` round-trip `[]float32` ↔ little-endian BLOB (the same format `sqlite-vec` expects on disk, so a future migration to native search needs no data rewrite). `CosineSimilarity(a, b)` returns the similarity in `[-1, 1]`, with mismatched dimensions and zero-magnitude vectors both mapping to `0`.

`Search` lives in `internal/npc/memory/longterm.go`: it issues a single `SELECT` against `npc_memories` scoped to the NPC, decodes each row's embedding, scores it with `vec.CosineSimilarity`, applies the threshold, and returns the top-K (salience and id breaking ties). This is brute-force but well within budget at realistic NPC memory volumes; M4 doesn't pretend otherwise.

The package-level `vec.Enabled` flag is reserved for a future native-acceleration path (custom SQLite functions exposed by `modernc.org/sqlite`, a pure-Go port of `sqlite-vec`, or a CGo backdoor explicitly carved out for this one extension). It stays `false` throughout M4. No code branches on it yet; the variable is the integration seam.

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

The `embedding` BLOB is little-endian float32 packed — the same on-disk format `sqlite-vec` expects, even though M4 doesn't load it. `internal/store/vec` provides the encode/decode helpers.

The search query is plain SQL, scoring done in Go:

```sql
SELECT id, npc_id, summary, embedding, created_at, salience
  FROM npc_memories
 WHERE npc_id = ?
```

Each row's blob is decoded, scored via `vec.CosineSimilarity`, filtered against the threshold, and ranked. Salience and row id break ties. Top-K is taken after sort.

## Implementation tasks

1. Add migration `0006_memory.sql`.
2. Implement `internal/store/vec`: `Encode`/`Decode` for the little-endian float32 BLOB format, `CosineSimilarity` in Go, and a placeholder `Enabled` flag (kept `false`) reserved for future native acceleration.
3. Implement `internal/npc/memory/shortterm.go`.
4. Implement `internal/npc/memory/longterm.go` with `Store` against SQLite.
5. Implement `internal/npc/memory/worker.go`.
6. Extend the `NPC` struct (M3) with a `Memory` field holding `ShortTerm` + a reference to the worker + the store.
7. Hook short-term `Append` into `HandleSay`: append the player turn and the NPC turn.
8. Hook retrieval into `HandleSay`: embed the recent short-term buffer, fetch memories, prepend to LLM context as a system message.
9. Hook salience bump after a response is delivered (best-effort; ignore errors).
10. Plumb a `DisconnectReason` (`quit` | `dropped`) from the session layer through to `world.Detach`. Split the existing single `"X falls asleep."` broadcast in `internal/world/mutations.go` into `"X goes to sleep."` (quit) and `"X fell asleep."` (dropped). `"X wakes up."` on `Attach` is unchanged.
11. Hook conversation-end detection: NPCs subscribe to their room's broadcast stream (using the M2 broadcast machinery — the M7 event bus replaces this later). Maintain a per-NPC `engaged` set keyed by `world.ObjectID`. On any leave/fell-asleep/goes-to-sleep broadcast naming an engaged player, drain that player's slice of the short-term buffer and submit it to the worker. Keep a per-(NPC, player) idle timer (`time.AfterFunc`, default 5 min) as a fallback; reset on each new turn from that player.
12. Implement a config knob for the summarizer model (e.g. `llama3.2:1b` by default) and embedding model (`nomic-embed-text`).
13. Bootstrap a worker pool at startup, one per loaded NPC.
14. On shutdown, drain remaining short-term buffers and wait for in-flight summarization (with a timeout) before closing.
15. Unit tests:
    - Encode/decode round-trip for float32 vectors.
    - Short-term ring buffer (append, drain, recent).
    - Retrieval with seeded embeddings against the fake backend.
    - `Detach(reason=quit)` vs. `Detach(reason=dropped)` produce the expected room broadcast strings.
16. Integration test: with the fake backend, run a scripted conversation, force a conversation-end via each trigger (player moves away, dropped link, quit, idle timer), verify a memory row is written and retrievable on the next turn.
17. Integration test: end the conversation, restart the server, start a new conversation, verify the memory still influences retrieval (deterministic embeddings from the fake backend make this reliable).

## Testing

- `go test -tags test ./internal/npc/memory/... ./internal/store/vec/...`
- Scenario tests using the fake backend.
- Manual: with real Ollama and the bartender NPC, hold two separate conversations (separated by a `quit`/relogin), confirm the second reflects something from the first.

## Acceptance criteria

1. Ending a conversation by any of the supported triggers (player moves to another room, player `quit`s, player's link drops, server-restart drain, or 5-min idle fallback) results in exactly one `npc_memories` row per ended conversation per NPC.
2. The disconnect-reason split is observable: running `quit` produces `"X goes to sleep."` in the room, while a dropped socket produces `"X fell asleep."`. Reconnect produces `"X wakes up."` (unchanged from M2).
3. Reopening a conversation with the same NPC retrieves at least one prior memory and includes it in the LLM context.
4. `vec.Enabled` is `false` at runtime; the implementation does not depend on a native SQLite extension being loaded. Search continues to function because cosine similarity is computed in Go over the BLOB-decoded embeddings.
5. After a restart, memories from before the restart influence new conversations.
6. Two NPCs in the same room maintain independent memory streams.
7. The summarization worker doesn't block the player's session — replies arrive at the same speed as in M3.

## Risks & open questions

- **Embedding consistency**: changing the embedding model later invalidates all stored embeddings. Track the model name in a metadata table (`memory_meta(npc_id, embedding_model)`) and either refuse to retrieve when models mismatch or re-embed lazily. Lean toward refusing + admin command to re-embed.
- **Future native vector search**: brute-force Go cosine is fine for hundreds-to-low-thousands of memories per NPC. When that ceiling becomes uncomfortable, the path forward is either (a) `modernc.org/sqlite`'s custom-function hook to register a cosine UDF and let SQL do the ranking, (b) a pure-Go port of `sqlite-vec`, or (c) carving a single CGo exception specifically for the extension. `vec.Enabled` exists for the day this lands; the BLOB format is already compatible.
- **Summarization quality**: small models can hallucinate. Pin a tested prompt and consider few-shot examples in M7 if quality bites.
- **Conversation-end signal reliability**: the primary mechanism is now NPC observation of room broadcasts (leaves / fell asleep / goes to sleep). That depends on every disconnect path actually reaching `world.Detach` and emitting a broadcast — including panics in the session goroutine, TCP RSTs, and TLS errors. The idle timer remains as a belt-and-suspenders fallback. Add a `defer` in the session loop that runs `Detach(reason=dropped)` no matter how the loop exits.
- **Salience drift**: a single retrieval bumps salience by +1.0; without decay, ancient memories dominate. Add a daily decay job in M7 when the scheduler exists.
- **Embedding latency**: an embedding call on every `say` adds latency. If it becomes noticeable, cache the most-recent embedding and only re-embed when the short-term buffer changes meaningfully (e.g. ≥2 new turns).
