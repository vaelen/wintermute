# Wintermute — Design

## Overview

Wintermute is a text-based, telnet-accessible chat server in the MUD/MUSH tradition, leaning toward the MUSH end of the spectrum: role-play, collaborative world-building, and player-owned creative spaces over combat and stats. The world is populated by LLM-powered NPCs that can hold persistent conversations with players, remember past interactions, and take actions in the world (selling items, training players, running shops, etc.).

The engine itself is theme-agnostic. The reference world is Neuromancer/cyberpunk-flavored, but nothing in the core engine depends on that.

In-world features modeled after BBSes — private mail, shared message boards, file uploads/downloads — are first-class.

## Goals & non-goals

**Goals**
- Persistent, multi-user, text-based virtual world.
- LLM-powered NPCs with per-NPC persona, long-term memory, and tool use.
- Tiered scripting: admins author full-power scripts; players script their owned spaces in a sandboxed subset.
- Authentic BBS-era protocol support (telnet, TLS, character set negotiation, X/Y/ZModem, Kermit) *and* modern HTTPS paths for the same operations.
- Single-binary distribution, embedded storage, minimal operational footprint.
- Pluggable LLM backends — local (Ollama) and hosted APIs (Anthropic, OpenAI, etc.) interchangeable per-NPC.

**Non-goals**
- High-fidelity MUD game mechanics (combat resolution, levelling curves, loot tables).
- Web-first UI. A web client may exist later; the canonical interface is a terminal.
- Horizontal scaling. Target is tens to low-hundreds of concurrent players on a single host.

## Language: Go

Chosen for:

- **Goroutine-per-connection and goroutine-per-NPC** map naturally onto the workload. The world is a mutable graph of stateful actors; Go's scheduler handles hundreds of mostly-idle goroutines cheaply, without async coloring or actor-framework ceremony.
- **Single static binary distribution** — drop on a VPS, run, done.
- **Standard library covers the networking and text layer** — `net`, `crypto/tls`, `golang.org/x/text/encoding` give us telnet, TLS, and character set conversion with no third-party dependencies.
- **Mature LLM ecosystem** — Eino (CloudWeGo) or a thin custom abstraction, plus official Go SDKs from Anthropic, OpenAI, and Ollama.
- **Strong embedded scripting options** — `gopher-lua` is a pure-Go Lua 5.1 implementation with a well-trodden sandboxing story.

Rust was considered and rejected: the borrow checker is a poor fit for a stateful mutable world graph with many cross-references, and async coloring complicates the NPC tick loop. Java was excluded by the user. Crystal/Nim/Zig lack mature LLM SDKs.

## Storage: SQLite + sqlite-vec

A single SQLite database, embedded in-process:

- **Relational tables** for world state: rooms, exits, objects, players, ACLs, mail, boards, file metadata, NPC config.
- **`sqlite-vec` extension** for vector similarity search over NPC long-term memory (summary embeddings).
- **WAL mode**, `synchronous=NORMAL`, single writer goroutine fed by a channel — eliminates lock contention without sacrificing durability.
- Driver: `modernc.org/sqlite` (pure-Go, no CGo) preferred for build simplicity; `mattn/go-sqlite3` if CGo features become necessary.

Escape hatch: if the engine ever outgrows SQLite, migration to Postgres + pgvector is straightforward — the schema is portable and the abstraction layer keeps query construction in one place.

## NPC architecture

NPCs are semi-autonomous: they react to events, can run scheduled behaviors, and can initiate action with goals of their own.

### Per-NPC state

- **Persona** — system prompt describing identity, voice, knowledge, constraints.
- **Location** — current room ID.
- **Inventory, stats** — same shape as a player; NPCs are first-class world objects.
- **Short-term memory** — ring buffer of recent observations (≈20 events).
- **Long-term memory** — summaries + embeddings in SQLite, retrieved by similarity to the current context.
- **Tools** — admin-authored Lua functions registered with the NPC (move, say, emote, give, buy, sell, train, …).
- **Budget** — tokens/min, tokens/hour, hard cap per day. Enforced at the LLM abstraction layer, not the provider layer.
- **Tick policy** — wake on event, wake on timer, or both.

### Event bus

- Rooms publish events ("player Foo said X", "Bar entered", "tick").
- NPCs subscribe to the rooms they occupy.
- Events are debounced: observations accumulate in a per-NPC buffer and fire an LLM call at most every N seconds.

### NPC tick

1. Drain observations since last tick.
2. If nothing interesting *and* no scheduled goal, skip (cheap heartbeat — no LLM call).
3. Otherwise, retrieve top-K long-term memories by embedding similarity to the current observation buffer.
4. Compose context (persona + short-term + retrieved + structured facts), call LLM with tool definitions.
5. Execute returned tool calls. Each tool is a Lua function in the admin-authored tool registry.
6. On conversation end (or every N turns), enqueue an async summarization job that writes a summary + embedding to long-term memory.

### Two-tier LLM routing

Idle rooms are the dominant cost driver. Mitigation:

- **Gate model** — a small local model (e.g. Llama 3.2 3B via Ollama) decides "should this NPC respond at all?" — single token in/out, near-free.
- **Response model** — only invoked when the gate says yes. May be hosted (Anthropic/OpenAI) or local depending on per-NPC config.

This routing is internal to the LLM abstraction; NPC authors don't see it.

### Budget enforcement

Per-NPC token budgets are tracked in the abstraction layer and persisted to SQLite. When a budget is exhausted, the NPC degrades gracefully (canned responses, "the merchant seems distracted") rather than erroring. Daily hard caps prevent a buggy persona from running up costs overnight.

## LLM abstraction

A thin `LLM` interface in the engine, configurable per-NPC:

```
type LLM interface {
    Chat(ctx, messages, tools, opts) (Response, error)
    Embed(ctx, text) ([]float32, error)
}
```

Implementations: Ollama, Anthropic, OpenAI. Eino is a candidate for the orchestration layer (tool calling, retries, streaming); if its abstractions feel heavy, a custom interface above the official SDKs is the fallback.

Embeddings default to a local model (Ollama, `nomic-embed-text` or similar) regardless of the chat backend, to keep memory writes off the metered API.

## Scripting: Lua, two sandboxes

One language, two configurations of `gopher-lua`.

### Admin tier

- Full Lua 5.1 surface plus the engine's world API.
- Can create/edit rooms, spawn NPCs, register tools, modify ACLs, schedule jobs.
- No sandbox — trust model is "admins are trusted."

### Player tier

- `os`, `io`, `debug`, `package`, raw `loadstring`/`load`/`loadfile`, `dofile` removed.
- Bounded replacements for `string.rep`, `string.format`, table operations.
- CPU budget enforced via `lua.SetHook` instruction counter.
- Memory budget tracked by allocation count.
- World API restricted to objects the player owns or has explicit permission on.
- No filesystem, no network, no process spawn.

### MUSH softcode compatibility (optional, deferred)

If a softcode-flavored DSL is desired for nostalgia/UX reasons, it can be a thin parser that compiles `[switch(%0,foo,bar)]`-style expressions to sandboxed Lua. Not part of the MVP.

## Protocol stack

### Telnet

Custom IAC option-negotiation handler. Options to support:

- **CHARSET** — negotiate UTF-8 vs CP437 vs Latin-1, etc.
- **NAWS** — window size for proper line wrapping.
- **TTYPE** — terminal type, for ANSI/VT100 capability detection.
- **GMCP / MSDP** — out-of-band structured data for modern MUD clients (Mudlet, MUSHclient).

### TLS

`crypto/tls` from the stdlib. Listen on both a plaintext port and a TLS port. Cert provisioning via Let's Encrypt (`golang.org/x/crypto/acme/autocert`) for production deployments.

### Character sets

`golang.org/x/text/encoding` handles conversion in and out of the wire encoding. Internal representation is always UTF-8 Go strings.

### File transfer

Two parallel paths, both surfaced through identical in-world commands (`upload <file>`, `download <id>`):

1. **BBS-era protocols over the existing telnet session.** Implemented as **standalone, publishable Go libraries** (see "Spinoff libraries" below). Detection is automatic where possible (ZModem auto-start sequences) and explicit via in-world subcommands otherwise.
2. **HTTPS endpoints with one-shot, short-lived tokens.** When a player initiates a transfer, the engine emits a URL containing a token; the URL is served by an HTTP listener integrated into the same binary. This is the modern fallback and the only path that works for web-based clients.

File metadata (owner, ACLs, size, content type, location) lives in SQLite. Blobs live on disk in a content-addressed store.

## Spinoff libraries

The XModem/YModem/ZModem implementations needed by the engine are general-purpose enough to be valuable on their own. They will be developed as standalone Go modules under their own import paths, depended on by the engine via normal Go module mechanics.

Scope per library:

- **`go-xmodem`** — XModem (checksum and CRC variants) and XModem-1K. Smallest, simplest; ≈150–300 LoC. Spec is in XMODEM.TXT (Chuck Forsberg).
- **`go-ymodem`** — YModem and YModem-G batch transfer. Builds on XModem-1K framing. ≈300–500 LoC.
- **`go-zmodem`** — ZModem with crash recovery and streaming windows. The largest of the three; ≈600–1000 LoC. Spec is in zmodem.doc (Forsberg, 1988).

Each library:

- Exposes a small `io.Reader`/`io.Writer`-shaped API so it can be layered over any stream (telnet session, raw socket, serial port).
- Has no dependency on the engine.
- Ships with a CLI demo and reference fixtures.
- Targets compatibility with `lrzsz` on the other end.

Kermit is **not** spun off — its spec is large and varied, and demand for a pure-Go Kermit is low. The engine wraps Columbia's `G-Kermit` as a subprocess piped through the player's session.

## Library stack (summary)

| Layer | Choice | Rationale |
|---|---|---|
| Network | `net` (stdlib) + custom telnet IAC | No third-party dependency for the core protocol |
| TLS | `crypto/tls` (stdlib) | Zero-effort, stdlib-stable |
| Cert provisioning | `golang.org/x/crypto/acme/autocert` | Let's Encrypt out of the box |
| Text encoding | `golang.org/x/text/encoding` | Comprehensive charset support |
| Storage | `modernc.org/sqlite` + `sqlite-vec` | Pure Go, no CGo, vector search included |
| LLM (Ollama) | `github.com/ollama/ollama/api` | Official Go client |
| LLM (Anthropic) | `github.com/anthropics/anthropic-sdk-go` | Official Go SDK |
| LLM (OpenAI) | `github.com/openai/openai-go` | Official Go SDK |
| LLM orchestration | Eino *or* custom thin interface | Decide once tool-calling needs are concrete |
| Scripting | `github.com/yuin/gopher-lua` | Mature pure-Go Lua 5.1 |
| HTTP | `net/http` (stdlib) | For the file-transfer fallback path |
| XModem | own (this project) | Standalone library |
| YModem | own (this project) | Standalone library |
| ZModem | own (this project) | Standalone library |
| Kermit | `G-Kermit` (subprocess) | Spec too large to port |

## Build order

The maximalist target is a multi-year project. The following ordering keeps the system playable as early as possible:

1. **Connection layer** — telnet server, TLS, login, character-set negotiation. Persistent player accounts in SQLite. No NPCs, no scripting.
2. **World layer** — rooms, exits, movement, look, say, emote, basic object model. Persistent.
3. **Reactive NPCs** — addressed-only response. Single LLM provider (start with Ollama for cost reasons). No long-term memory.
4. **NPC memory** — short-term ring buffer, long-term summarization + vector retrieval.
5. **Admin scripting** — full-power Lua, world API, tool registration for NPCs.
6. **In-world mail, boards, HTTPS file transfer.**
7. **Semi-autonomous NPC loop** — event bus, tick scheduler, two-tier model routing, budget enforcement.
8. **Player-tier scripting** — sandboxed Lua, ACL-restricted world API.
9. **XModem / YModem / ZModem spinoff libraries**, integrated into the file-transfer flow.
10. **Kermit** via `G-Kermit` wrapper, if still wanted at this point.

Steps 1–5 alone constitute a complete, playable MUSH with conversational NPCs. The remaining steps add the features that distinguish Wintermute from a generic LLM-MUD: autonomy, player creativity, and BBS-authentic file transfer.

## Open questions

- Eino vs. a hand-rolled LLM abstraction — defer until the tool-calling surface is concrete.
- Whether GMCP/MSDP support is in scope for MVP. Probably no, but the telnet layer should not preclude it.
- Web client — out of scope for now, but the HTTPS file-transfer listener could share a port with a future web frontend.
- Whether the engine itself should be released as open source alongside the protocol libraries. No decision yet.
