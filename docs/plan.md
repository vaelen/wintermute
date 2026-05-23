# Wintermute — Implementation Plan

This document turns the 10-step build order from [`design.md`](./design.md) into an executable roadmap. Each milestone has its own plan in [`milestones/`](./milestones/).

## Vision recap

A text-based, telnet-accessible MUSH-leaning chat server populated by LLM-powered NPCs with persistent memory, scriptable objects, and in-world BBS features (mail, boards, file transfer). Single static binary, embedded SQLite, local Ollama. Cyberpunk world theme; theme-agnostic engine.

See [`design.md`](./design.md) for the full system design.

## Constraints

- **Pluggable LLM backends; Ollama is the only registered backend initially.** The `LLM` interface, factory registry, and per-NPC backend config exist from milestone 3 onward so that Anthropic/OpenAI/etc. can be added later by registering a new factory — with zero changes to NPC code, world state, or persisted config.
- **Local Ollama for all NPCs.** Two-tier model routing (a small gate model + a larger response model) still applies; both happen to be Ollama models for now.
- **Single static binary.** Pure-Go SQLite (`modernc.org/sqlite`); no CGo unless explicitly justified.
- **SQLite is the only datastore.** Vector search via the `sqlite-vec` extension.
- **No code outside its milestone.** Resist the urge to half-implement future milestones for convenience.

## License and source-file header

The project is **MIT-licensed, Copyright Andrew C. Young &lt;andrew@vaelen.org&gt;**.

`LICENSE` at the repo root carries the standard MIT text:

```
MIT License

Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

Every source file in the engine repo (and in the X/Y/ZModem spinoff repos) begins with a two-line header. The `<year>` is the year the file was first added.

Go (`.go`):
```go
// Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT
```

Lua (`.lua`):
```lua
-- Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT
```

SQL (`.sql`):
```sql
-- Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT
```

TOML / YAML / shell (`.toml`, `.yml`, `.sh`):
```toml
# Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
# SPDX-License-Identifier: MIT
```

The `Makefile` includes a `check-headers` target that greps every committed source file for the `SPDX-License-Identifier: MIT` line and fails the build on a miss. Run as part of CI.

Exempt: vendored third-party code (retain its own license), `go.mod`/`go.sum`, generated files.

## Repo layout

```
wintermute/
├── cmd/
│   └── wintermute/         # main binary entry point
├── internal/
│   ├── auth/               # accounts, password hashing, login
│   ├── net/
│   │   ├── telnet/         # IAC negotiation, NAWS, GMCP later (opt-in per session)
│   │   └── tls/            # TLS listener helpers (autocert glue)
│   ├── term/               # capability detection + per-encoding I/O translation
│   │                       # (UTF-8, CP437, Latin-1, MacRoman, PETSCII, ASCII)
│   ├── world/              # rooms, exits, objects, world cache
│   │   ├── api/            # the API surface exposed to Lua
│   │   └── events/         # event bus (M7)
│   ├── llm/                # LLM interface + registry
│   │   ├── ollama/         # Ollama backend (registers as "ollama")
│   │   └── fake/           # test-only deterministic backend
│   ├── npc/                # NPC state, conversation loop
│   │   ├── memory/         # short/long-term memory
│   │   └── loop/           # autonomous tick loop (M7)
│   ├── script/
│   │   ├── lua/            # gopher-lua wrapper (admin tier)
│   │   └── sandbox/        # player-tier sandbox (M8)
│   ├── mail/               # in-world mail
│   ├── boards/             # message boards
│   ├── files/              # blob store + ACLs
│   ├── http/               # HTTPS listener for file transfer + future web
│   ├── store/              # SQLite open, migrations, single-writer goroutine
│   │   └── migrations/     # embedded .sql files
│   ├── config/             # TOML config loader
│   └── integration/        # scenario tests
├── docs/
│   ├── design.md
│   ├── plan.md             (this file)
│   └── milestones/01..11
├── LICENSE
├── README.md
├── Makefile
├── .golangci.yml
├── go.mod
└── go.sum
```

The X/Y/ZModem libraries live in **separate repos** under the same author:

```
github.com/vaelen/go-xmodem
github.com/vaelen/go-ymodem
github.com/vaelen/go-zmodem
```

(Exact module path for the engine itself, e.g. `github.com/vaelen/wintermute`, to be confirmed at milestone 1.)

## Tech stack

| Concern | Choice |
|---|---|
| Language | Go 1.23+ |
| Storage | SQLite via `modernc.org/sqlite` (pure-Go) |
| Vector search | `sqlite-vec` extension |
| Telnet | hand-rolled IAC parser on top of `net`; opt-in per session after IAC peek |
| TLS | `crypto/tls`, `golang.org/x/crypto/acme/autocert` |
| Encoding | `golang.org/x/text/encoding` for CP437/Latin-1/MacRoman; **custom `internal/term` package** for PETSCII, ASCII downgrade, ANSI sequence parsing, and box-drawing translation |
| Scripting | `github.com/yuin/gopher-lua` |
| LLM | `github.com/ollama/ollama/api` (initial only registered backend) |
| Logging | `log/slog` |
| Config | TOML (`github.com/pelletier/go-toml/v2`) |
| HTTP | `net/http` (stdlib) |
| Password hashing | `golang.org/x/crypto/argon2` |
| Lint | `golangci-lint` |
| Test fixtures | `lrzsz` (for ZModem CI) |

## Milestones

| #      | Milestone                                   | Summary                                                                                                                                 | Depends on        | Effort | Status      |
|--------|---------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------|-------------------|--------|-------------|
| 01     | Connection layer                            | Telnet (opt-in) + TLS + raw-TCP; capability detection across six encodings; accounts, login                                             | —                 | L      | shipped     |
| 02     | World layer                                 | Rooms, exits, objects, movement, basic commands                                                                                         | 01                | M      | shipped     |
| 03     | Reactive NPCs                               | Pluggable `LLM` interface, Ollama backend, addressed-only NPC responses                                                                 | 02                | M      | shipped     |
| 04     | NPC memory                                  | Short-term ring buffer, long-term summary+embedding via `sqlite-vec`                                                                    | 03                | M      | shipped     |
| 05     | Admin scripting                             | gopher-lua, admin world API, tool registry (NPC tool calls wired in M7)                                                                 | 02                | L      | shipped     |
| 05.5   | Readline line editing                       | In-line editing + per-session history on ANSI terminals; simple loop kept for the rest                                                  | 01                | S      | shipped     |
| 05.7   | Diegetic engagement primitive               | Player↔object and player↔NPC private engagements; M3 name-tag fallback becomes engagement-aware                                         | 03, 05            | M      | shipped     |
| 06     | Mail / boards / HTTPS file transfer         | In-world mail, message boards, token-gated upload/download — accessed via a terminal engagement (M5.7)                                  | 05.7              | M      | shipped     |
| 06.1   | Admin Lua: mail / boards / networks         | `wintermute.mail.*`, `wintermute.board.*`, `wintermute.ftn.network.*` admin bindings; no new in-game admin commands                     | 06                | S      | shipped     |
| 06.2   | Admin Lua: files + Dropbox + mail-on-upload | File-area concept + default `dropbox`; admin Lua for files; upload-complete delivered as system mail; `@cleanup-files`                  | 06, 06.1          | S      | shipped     |
| 06.3   | Menu-driven engagement interface            | Reusable line-drawing menu handler (mail/boards/files entries opt-in per object); ASCII fallback for non-UTF-8                          | 05.7, 06, 06.2    | M      | shipped     |
| 06.4   | Menu-driven admin interface                 | Admin entry in the M6.3 menu, gated by admin level + per-object opt-in; users/mail/boards/files/objects/rooms/networks                  | 06.1, 06.2, 06.3  | M      | shipped     |
| 06.4.1 | Password reset via admin                    | Admin-issued one-time word-list tokens, 48h TTL, forced password change on redemption; adds the Reset Password action to the admin menu | 06.4              | S      | shipped     |
| 06.5   | ANSI-based terminal detection               | Detect terminal type and size from ANSI sequences for non-telnet clients; telnet TTYPE/NAWS remain preferred when available             | 01, 06.3.1        | S      | shipped     |
| 06.6   | Login hardening                             | Disallowed-username list, IP deny list with TTL, optional subtext-filter UDP bridge, `@rename` admin command                            | 01                | M      | shipped     |
| 07     | Semi-autonomous NPC loop                    | Event bus, tick goroutines, two-tier routing, budget enforcement, tool execution                                                        | 04, 05            | L      | shipped     |
| 08     | Player-tier scripting                       | Sandboxed Lua, instruction/memory budgets, ACL-restricted world API                                                                     | 05                | M      | not started |
| 09     | X/Y/ZModem spinoff libraries                | Three MIT-licensed Go modules, engine integration                                                                                       | 06                | L      | not started |
| 10     | TLS configuration                           | Shared `self-signed` / `files` / `autocert` provider for the telnet TLS port and the HTTPS file-transfer port                           | 01, 06            | S      | not started |
| 11     | End-to-end integration testing              | Living scenario suite; each milestone appends its scenario wishlist as it ships                                                         | (all)             | M      | not started |

### Sequencing

```
01 ── 02 ── 03 ── 04 ──┐
 │     │     │         ├── 07
 │     │     └── 05 ───┤
 │     │           │   └── 05.7 ── 06 ── 06.1 ── 06.2 ── 06.3 ── 06.4 ── 06.4.1
 │     │           │                                      │
 │     │           │                                      └── 09
 │     │           └── 08
 │     │
 │     └── 10 (depends on 01 + 06; can land any time after 06)
 │
 ├── 05.5 (independent QoL pass; depends only on 01)
 ├── 06.5 (ANSI terminal detection; depends on 01 + 06.3.1)
 └── 06.6 (login hardening; depends on 01)

11 is a living document tracking every other milestone's end-to-end scenarios.
```

Natural milestones along the way:
- **After M5**: a complete, playable MUSH with reactive NPCs that have memory and an admin scripting layer. This is the first "show someone the game" moment.
- **After M6.4**: full BBS-style menus + admin console reachable from in-game, with mail/boards/files all manageable without leaving the world.
- **After M8**: feature-complete MUSH with autonomous NPCs and player-authored content.
- **After M9 + M10**: BBS-authentic protocols and production-grade TLS in place.

## Cross-cutting concerns

### Logging
- `log/slog` with JSON handler to stdout in production, text handler in dev.
- Structured fields: `session_id`, `account`, `room`, `npc`, `tool`, `backend`.
- A separate **world log** in SQLite (`world_log` table) captures auditable in-game events: logins, room/object mutations, mail sent, files transferred, budget exhaustion. Keep for moderation and replay.

### Config
- Single `wintermute.toml` at a configurable path. Env-var overrides for secrets only (e.g. `WINTERMUTE_OLLAMA_URL`).
- No multi-environment ceremony. If staging vs. prod is needed someday, hand-edit a second file.
- Example shape:
  ```toml
  [server]
  telnet_port = 23
  tls_port = 2323
  http_port = 8443

  [db]
  path = "/var/lib/wintermute/wintermute.db"

  [llm.default]
  backend = "ollama"
  [llm.default.opts]
  url = "http://localhost:11434"
  model = "llama3.2:3b"
  gate_model = "llama3.2:1b"
  embedding_model = "nomic-embed-text"
  ```

### Storage
- WAL mode, `synchronous=NORMAL`, `foreign_keys=ON`.
- **Single writer goroutine** owns all `INSERT`/`UPDATE`/`DELETE`. Other goroutines send write requests over a channel and receive `(rowid, error)` back. Reads can go direct.
- Migrations: numbered `.sql` files in `internal/store/migrations/`, embedded with `//go:embed`. A tiny `schema_version` table tracks applied versions. Forward-only; no auto-down.

### Testing
- Unit tests next to code (`*_test.go`).
- `internal/integration` spins the server on an ephemeral port and drives sessions through real telnet/TLS connections.
- Ollama-dependent tests carry a build tag `ollama` and only run when an `OLLAMA_URL` env var is set in CI.
- ZModem tests use `lrzsz` on the other end; tagged `zmodem`.
- The test-only `internal/llm/fake` backend provides deterministic, scripted responses so the bulk of NPC logic can be tested without a live LLM.

### Build / CI
- `Makefile` targets: `build`, `test`, `lint`, `check-headers`, `run`, `db-reset`.
- CI runs `lint`, `check-headers`, `test`, then the tagged integration suites against a service-container Ollama.
- Release artifacts deferred until at least M5.

### Graceful shutdown
- Single `context.Context` cancelled on SIGINT/SIGTERM. Connection acceptors stop, in-flight sessions drain (configurable grace period), writer goroutine flushes, DB closes. Each milestone that introduces a new background goroutine wires its cancellation into this context.

## Definition of done (project-wide)

A milestone is done when:
1. All tasks in its plan are checked off.
2. Acceptance criteria are demonstrably met (commands work in a live telnet session where applicable; tests pass).
3. `make lint check-headers test` is green.
4. Documentation referenced by the milestone is updated.
5. The next milestone's plan is up to date with anything learned along the way.

## Cadence

This is a side project, not a sprint. Expected pacing:
- M1: 2–3 weekends (the telnet/encoding capability layer is bigger than it looks). M2: a weekend.
- M3–M4: 1–2 weeks each (LLM plumbing is fiddly).
- M5: 2–3 weeks (Lua API surface is large).
- M5.5: a weekend (small package, isolated from the rest of the engine).
- M5.7: 1–2 weeks (new world-layer primitive plus parser + M3 integration).
- M6: 1 week.
- M6.1 + M6.2: a weekend each (admin Lua wraps existing services).
- M6.3: 1–2 weeks (the menu renderer + state machine is the bulk).
- M6.4: 1 week (each admin subsection is small once the M6.3 framework exists).
- M6.4.1: a weekend (small auth/migration + login-flow change, mostly tests).
- M7: 2–3 weeks (event bus + budgets + tool invocation is the trickiest milestone).
- M8: 1–2 weeks.
- M9: 1 week per protocol, run as three separate open-source releases.
- M10: a long weekend (autocert is the bulk; self-signed already works).
- M11: continuous; pick a 1-week sweep after each major milestone to drain its scenario backlog.

Total: roughly 4–6 months of focused weekends to reach M8, more realistically 7–10 months calendar time.

## How to use these plans

1. Read [`design.md`](./design.md) once for context.
2. Read this file for the roadmap.
3. When starting a milestone, open its plan in [`milestones/`](./milestones/) and treat the Implementation tasks section as your TODO list.
4. If you discover something that changes the plan, edit the milestone's plan first, then the code.
