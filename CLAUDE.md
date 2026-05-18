# CLAUDE.md

Guidance for Claude (and other AI assistants) working in this repository.

## What this is

**Wintermute** is a text-based MUD/MUSH-leaning chat server with LLM-powered NPCs. The reference world is Neuromancer/cyberpunk-flavored, but the engine itself is theme-agnostic. Players connect via telnet or TLS, talk to each other and to NPCs, hold persistent inventories, send mail, post to message boards, and upload/download files. Admins and (eventually) players can script the world with Lua.

The project is in early planning — most code does not exist yet. The authoritative documents are:

- [`docs/design.md`](docs/design.md) — the system design.
- [`docs/plan.md`](docs/plan.md) — the overall implementation roadmap.
- [`docs/milestones/01-…md`](docs/milestones/) through `10-…md` — per-milestone executable plans.

Read those first. The plan is the source of truth for what to build, in what order, and how it fits together. **Do not propose architecture changes without reading them.**

## Non-negotiable rules

### License header on every source file

The project is MIT-licensed, Copyright Andrew C. Young &lt;andrew@vaelen.org&gt;. Every Go, Lua, SQL, TOML, YAML, and shell file in the repo begins with a two-line SPDX header. Use the comment marker appropriate to the language:

```go
// Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT
```

```lua
-- Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT
```

```sql
-- Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT
```

```toml
# Copyright (c) <year> Andrew C. Young <andrew@vaelen.org>
# SPDX-License-Identifier: MIT
```

`<year>` is the year the file was first introduced. `make check-headers` enforces this in CI. Vendored third-party code and generated files are exempt.

### Stay inside the current milestone

The plan partitions work into ten milestones with explicit dependencies. **Do not implement code that belongs to a later milestone, even "to save a trip later."** If something feels tempting and isn't in the current milestone's scope, leave it for its owning milestone. The plan exists precisely so the project stays buildable end-to-end at every step.

If you discover the plan is wrong, edit the affected milestone document first, then write code.

### LLM backends are pluggable, Ollama is the only registered one

The `internal/llm` package defines an `LLM` interface and a factory registry. **Ollama is the only registered backend.** Do not add Anthropic, OpenAI, or other backends without explicit instruction — but design any new code so adding them later requires only a new `internal/llm/<name>` package that registers itself in `init()`. Per-NPC config references backends by name (`"ollama"`); the rest of the engine never branches on backend.

## Conventions

- **Go version**: 1.23+.
- **SQLite driver**: `modernc.org/sqlite` (pure-Go, no CGo). Don't introduce CGo dependencies without justification.
- **Vector search**: `sqlite-vec` extension, loaded at DB open.
- **Single-writer goroutine** owns all SQLite writes. Other goroutines send write requests over a channel. Reads can go direct. Don't sprinkle `*sql.DB.Exec` calls from arbitrary goroutines.
- **Logging**: `log/slog`, JSON in prod, text in dev. Structured fields: `session_id`, `account`, `room`, `npc`, `tool`, `backend`.
- **Config**: a single `wintermute.toml`. Env-var overrides only for secrets.
- **Tests**: unit tests beside code. Scenario tests in `internal/integration`. The test-only `internal/llm/fake` backend (build tag `test`) provides deterministic LLM responses; use it for anything that exercises NPC logic. Tests that hit a real Ollama use build tag `ollama` and skip if `OLLAMA_URL` is unset.
- **No comments** explaining what code already says. Only comment when the *why* is non-obvious (a hidden constraint, a workaround, a surprise).
- **No backwards-compatibility shims**, no half-finished implementations, no premature abstractions. The plan tells you what each milestone delivers; deliver that and stop.
- **Error matching**: use sentinel errors (`var ErrFoo = errors.New(...)`) and `errors.Is` / `errors.As` for cross-package error dispatch. Do not match on `err.Error()` substrings — message text is presentation, not API, and substring matches break silently when wording changes. The one exception is third-party concrete error types we can't introspect cleanly (e.g. `modernc.org/sqlite`'s unique-constraint error); document the reason inline when you do.

## Repo layout

(Expected at milestone 1+. See `docs/plan.md` for the full description.)

```
cmd/wintermute/        # main binary
internal/auth/         # accounts, password hashing
internal/net/telnet/   # telnet IAC, charset, NAWS
internal/net/tls/      # TLS listener glue
internal/world/        # rooms, exits, objects, world cache
internal/world/api/    # API surface exposed to Lua (admin tier)
internal/world/events/ # event bus (milestone 7)
internal/llm/          # LLM interface + registry
internal/llm/ollama/   # Ollama backend (registers as "ollama")
internal/llm/fake/     # test-only deterministic backend
internal/npc/          # NPC state, conversation loop
internal/npc/memory/   # short/long-term memory
internal/npc/loop/     # autonomous tick loop (milestone 7)
internal/script/lua/   # admin gopher-lua VM
internal/script/sandbox/  # player-tier sandbox (milestone 8)
internal/mail/
internal/boards/
internal/files/
internal/http/
internal/store/        # SQLite open, migrations, writer goroutine
internal/store/migrations/  # embedded .sql files
internal/config/
internal/integration/  # scenario tests
docs/                  # design, plan, milestone plans
```

The X/Y/ZModem libraries (`go-xmodem`, `go-ymodem`, `go-zmodem`) live in **separate repos** under the same author; the engine depends on them as ordinary Go modules. See [`docs/milestones/09-xymodem-zmodem-libraries.md`](docs/milestones/09-xymodem-zmodem-libraries.md).

## Common commands

Once the Makefile lands in milestone 1:

```sh
make build           # build the binary
make test            # unit + integration tests (Ollama tests skipped if OLLAMA_URL is unset)
make lint            # golangci-lint
make check-headers   # verify every source file has the MIT SPDX header
make run             # run the server against ./wintermute.toml
```

Until then, plain `go build ./...` and `go test ./...` work on the skeleton.

## When in doubt

1. Re-read `docs/plan.md`.
2. Re-read the relevant `docs/milestones/NN-….md`.
3. If the docs disagree with the code, the docs are likely right at this stage of the project — but raise the discrepancy with the user rather than silently picking one.
