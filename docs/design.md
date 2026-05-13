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

### Connection acceptance

Every connection is wrapped in a telnet layer immediately on accept. The server sends its initial IAC option offers (WILL ECHO, WILL SGA, DONT LINEMODE, DO TTYPE, DO NAWS, WILL CHARSET) right away, without waiting to see whether the client speaks telnet. Telnet clients respond with their own IAC and drop into character mode. Non-telnet clients (netcat, hardware modems, telnet-incapable BBS clients) ignore the offers — they render as a brief garble of ~18 bytes before the readable banner. The trade-off is intentional: BSD `telnet`, for example, does not send IAC until the server speaks first, so any "detect telnet by waiting for client IAC" strategy fails for it.

The session tracks two facts independently:

- **Negotiated** — flips true the first time *any* IAC command arrives from the client. Used to decide whether server-side echo is safe to enable by default and whether 8-bit-clean binary streams need their IAC bytes escaped. Exposed as `tc.Negotiated()`.
- **Server echo** — controllable independently via `SetEcho`. Auto-enabled on the first negotiated transition (if `OfferEcho` was set), suppressed during password entry, and toggleable by user command in the future. Decoupled from `Negotiated()` so a non-telnet user could enable echo manually (with the understood double-echo trade-off).

Telnet options supported when negotiated:

- **CHARSET** — recorded as a hint; the engine's own encoding handshake (see below) is authoritative.
- **NAWS** — window size; updates the session's tracked screen size dynamically.
- **TTYPE** — terminal type; used as a hint during capability auto-detect.
- **ECHO / SGA / LINEMODE** — together establish character-at-a-time mode with server-side echo.
- Unknown / unsupported options receive a clean `WONT` / `DONT` reply so the negotiation completes; new options can be added by extending the small switch in `handleWILL` / `handleDO` / `handleSB`.
- **GMCP / MSDP** — out-of-band structured data for modern MUD clients (Mudlet, MUSHclient); accepted but unused until a later milestone.

### TLS

`crypto/tls` from the stdlib. Listen on both a plaintext port and a TLS port. Cert provisioning via Let's Encrypt (`golang.org/x/crypto/acme/autocert`) for production deployments. TLS is orthogonal to telnet — a TLS session may or may not negotiate telnet, exactly as a plaintext session does.

### Terminal capabilities — independent axes

Every session tracks the following capability axes independently. Real-world clients combine them in non-obvious ways (a netcat connection may have full UTF-8 + ANSI but no telnet; a C64 over a modern dialer may have telnet but PETSCII encoding; stock `telnet(1)` typically has telnet but no NAWS), so they are not collapsed into a single "client kind."

| Axis | Values | Default / detection | Mutable post-login |
|---|---|---|---|
| **Telnet protocol** | on / off | IAC peek on connect | no (set at connect time) |
| **Character encoding** | UTF-8 / CP437 / ISO-8859-1 / MacRoman / PETSCII / ASCII | auto-detect → user confirmation prompt | yes (`terminal` command) |
| **Screen size** | width × height | NAWS (when telnet on) or encoding default (40 for PETSCII/ASCII, else 80) | yes (`terminal` command, plus live NAWS during a session) |
| **Color** | on / off | on for every encoding except ASCII | yes (`terminal` command) |
| **DEC line drawing** | on / off | on for ISO-8859-1 and MacRoman; off elsewhere | yes (`terminal` command), independent of encoding |

The rest of the engine treats screen size as a real-world property (used for wrapping, listing, etc.); the other axes are purely I/O concerns.

### Internal representation: UTF-8 + ANSI

All in-engine strings, all stored content, and all output composed by the engine are **UTF-8 with ANSI escape codes** for color and cursor control. The internal vocabulary explicitly admits:

- **Single-line box-drawing** (`─│┌┐└┘├┤┬┴┼` and the rest of U+2500–U+257F single-stroke set).
- **Double-line box-drawing** (`═║╔╗╚╝╠╣╦╩╬`-style — the CP437-mirroring subset of U+2550–U+256C).
- **Shade / block / semi-graphics** (`░▒▓█` and half-block / quadrant family, U+2580–U+259F — mirroring CP437 0xB0–0xDF).

Engine code and Lua scripts compose UTF-8 strings that may include any of these. Per-encoding translation happens only at the I/O boundary in `internal/term`.

#### Per-encoding output pipeline

The encoder applies these transformations in order:

1. **ANSI handling** — pass through, translate to encoding-native equivalents, or strip, depending on the encoding (see below).
2. **DEC line-drawing substitution** (only if the session has DEC line drawing enabled): walk the buffer for runs of single-line box-drawing characters and emit them using the DEC Special Graphics set (`q` for `─`, `x` for `│`, `l k m j` for corners, `t u v w n` for tees/cross, etc.), bracketed by `ESC ( 0` (designate G0 → DEC Special Graphics) and `ESC ( B` (designate G0 → ASCII). **The encoder maintains line-drawing state across writes** and emits the switch sequences only at transitions — entering a run emits one `ESC ( 0`, leaving one emits one `ESC ( B` — so a horizontal rule of 60 line characters is bracketed by exactly one pair, not 60 pairs. (`ESC ( 0` / `ESC ( B` is used rather than SO/SI to avoid colliding with the connect-time PETSCII Shift Out.)
3. **Double-line downgrade** — for any encoding other than UTF-8 and CP437, replace each double-line character with its single-line equivalent before the character-mapping step.
4. **Character mapping** — encoding-specific (below).

Two general rules apply at the mapping step:

- **Drawing characters with no encoding equivalent emit a space**, not `?`. This includes single-line box (after the optional DEC step), double-line box (after downgrade), and shade / semi-graphics. Diagrams degrade as holes, not as visual noise.
- **Non-drawing characters with no encoding equivalent emit `?`**, as a normal lossy-transcoding signal.

Per-encoding specifics:

- **UTF-8** — pass-through. ANSI preserved. Every drawing character is native. (DEC line drawing remains user-toggleable for completeness, but defaults off.)
- **CP437** — `golang.org/x/text/encoding` transcode; ANSI preserved. CP437's native repertoire covers single-line, double-line, and shade/semi-graphics 1:1, so the mapping is direct.
- **ISO-8859-1** — `golang.org/x/text/encoding` transcode; ANSI preserved; **DEC line drawing default on** (so box-drawing characters render as real lines via the VT100 graphics set rather than as `-`/`|`/`+`); shade and other semi-graphics → space.
- **MacRoman** — same posture as ISO-8859-1: DEC line drawing default on; semi-graphics → space.
- **PETSCII** — custom translation:
  - Letters / digits / punctuation → PETSCII bytes (respecting the connect-time mixed-case mode).
  - ANSI SGR colors → PETSCII color control bytes (`0x05`, `0x1C`, `0x1E`, `0x1F`, `0x81`–`0x9F`).
  - Single-line box-drawing → native PETSCII line graphics (`0xC0`, `0xDD`, etc.).
  - Shade / semi-graphics → native PETSCII semi-graphics where an equivalent exists; otherwise space.
  - ANSI cursor motion → best-effort PETSCII cursor controls; anything ambiguous dropped.
  - DEC line drawing is meaningless on PETSCII clients and is ignored even if toggled on.
- **ASCII** — strict 7-bit. Strip all ANSI sequences. Box-drawing (single- or double-line, after downgrade) → `-` / `|` / `+`. Shade / semi-graphics → space. Anything else high-byte → `?`. DEC line drawing is ignored on ASCII (ASCII has no escape mechanism by definition).

### Capability detection

On every connection, before login:

1. **Telnet peek.** Block up to 200 ms reading the initial bytes. If the first byte is `IAC (0xFF)`, mark telnet on and enter option negotiation (offer DO/WILL for TTYPE, NAWS, CHARSET, ECHO, SGA). Otherwise mark telnet off and return the buffered bytes to the session's input stream.
2. **TTYPE / NAWS hints.** If telnet is on, drain any IAC subnegotiation responses already buffered; TTYPE and NAWS values are recorded if the client supplied them.
3. **Press-enter banner + ANSI probe.** Send:
   ```
   WINTERMUTE
   PRESS ENTER TO BEGIN.
   ```
   immediately followed by the ANSI Device Attributes query (`ESC [ c`). The banner doubles as a synchronization point — cooked-mode terminals line-buffer their stdin until the user presses Enter, so any auto-response the terminal generates in response to the probe arrives together with that keystroke. This sidesteps the timing race that would otherwise force a probe timeout.
4. **Read raw input line.** Read bytes from the connection until either `\r` or `\n` (CRLF consumed as one terminator). The captured bytes may contain an ANSI Device Attributes response (`ESC [ ? <digits and semicolons> c`), nothing, or unrelated terminal auto-responses. Scan for the DA response: if found, mark the session ANSI-capable.
5. **Compute defaults.**
   - encoding: TTYPE-derived if obvious; else UTF-8 if ANSI-capable; else ASCII.
   - width / height: NAWS if reported; else 80×24, except PETSCII/ASCII default to 40×24.
   - telnet: as detected in step 1.
   - If the connecting account has saved preferences from a prior session, prefer those over the auto-detected defaults (applied after login).
6. **Confirmation prompt.** Send an **all-uppercase** prompt asking the user to confirm or override:

   ```
   WELCOME TO WINTERMUTE.

   TERMINAL TYPE: U - UNICODE [MODERN, DEFAULT], D - DOS [CP437],
   M - MAC [CLASSIC], L - LATIN-1, P - PETSCII, A - ASCII:
   ```

   The `[DEFAULT]` marker attaches to whichever option was auto-detected (e.g. `P - PETSCII [DEFAULT]` if the auto-detect chose PETSCII); pressing Enter accepts that choice. The prompt uses only characters whose byte positions render correctly on a PETSCII client in its **default (uppercase / graphics) mode** — uppercase letters `A`–`Z`, digits, space, and the punctuation `: , - ( ) [ ]` — so no Shift Out is needed yet.
7. **Apply selection.**
   - Rebuild the session's `Encoder` for the chosen encoding.
   - **If PETSCII was chosen**, emit PETSCII control code `0x0E` (Shift Out) so the C64 switches into mixed-case mode for all subsequent output. The engine emits Shift Out **every time the session transitions into PETSCII** — at the end of this prompt, after `terminal encoding petscii` post-login, or after a saved-prefs override loads PETSCII. Non-PETSCII encodings never see Shift Out from the engine.
   - Force width to 40 columns for PETSCII and ASCII unless the user later overrides it.

After login, the user can view or change any axis via a `terminal` command; preferences are persisted per-account and used as the auto-detect bias on next login.

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
| Text encoding | `golang.org/x/text/encoding` (CP437 / Latin-1 / MacRoman) + custom `internal/term` (PETSCII, ASCII downgrade, box-drawing translation, ANSI parser) | Comprehensive charset support; PETSCII is not in `x/text` and has to be hand-written |
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
