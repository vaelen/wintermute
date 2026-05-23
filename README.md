# Wintermute

A text-based MUD/MUSH-style chat server, populated by LLM-powered NPCs with persistent memory, scriptable rooms and objects, and in-world BBS features (private mail, shared message boards, file uploads and downloads).

The engine is theme-agnostic; the reference world is Neuromancer/cyberpunk-flavored.

## Status

Active development. Most of the engine works end-to-end:

- **Shipped:** connection layer (M1), world layer (M2), reactive NPCs (M3), NPC memory (M4), admin Lua scripting (M5), readline editing (M5.5), diegetic engagement (M5.7), mail + boards + HTTPS file transfer (M6), admin Lua bindings for mail/boards/files (M6.1, M6.2), menu-driven engagement and admin interfaces (M6.3, M6.4), admin-issued password reset (M6.4.1), ANSI-based terminal detection (M6.5), login hardening (M6.6), and the semi-autonomous NPC loop with token budgets, tool calls, scheduled goals, and memory decay (M7).
- **Remaining:** player-tier scripting (M8), X/Y/ZModem spinoff libraries (M9), production TLS configuration (M10), and the living end-to-end integration suite (M11).

After M7, you can stand up a server, log in over telnet or TLS, talk to NPCs that overhear conversation and respond on a per-room debounce, hand them tools that mutate the world, post to message boards, send mail, and upload/download files through a menu engagement. See [`docs/plan.md`](docs/plan.md) for the full milestone table.

## What's in the box (the plan)

- **Telnet + TLS** front door, character-set negotiation, classic BBS-era protocols (X/Y/ZModem, Kermit) alongside modern HTTPS file transfer.
- **LLM-powered NPCs** with per-NPC persona, short-term and long-term memory, and tool calls that affect the world. NPCs run on **local Ollama** by default; the LLM layer is pluggable so other backends can be added without touching NPC code.
- **Tiered Lua scripting**: admins get the full surface; players script their owned spaces in a sandboxed VM.
- **Single static Go binary**, embedded SQLite with `sqlite-vec` for memory retrieval. No external services required to run the server.

For the full picture, read the docs.

## Documentation

| Document | What it is |
|---|---|
| [`docs/design.md`](docs/design.md) | The system design — what Wintermute is and why. |
| [`docs/plan.md`](docs/plan.md) | The overall implementation roadmap, tech stack, repo layout, conventions. |
| [`docs/milestones/01-…md`](docs/milestones/) | Per-milestone executable plans, each with goal, scope, schema changes, tasks, tests, and acceptance criteria. |

If you want to follow along with development, start with the design doc and then the milestone you're curious about.

## Building

Requires Go 1.23 or newer. SQLite is embedded (pure-Go `modernc.org/sqlite`); no CGo toolchain needed.

```sh
git clone git@github.com:vaelen/wintermute.git
cd wintermute
make build      # or: go build ./...
make test       # unit + integration tests; Ollama-tagged tests skip if OLLAMA_URL is unset
```

To run a local server:

```sh
make run        # uses ./wintermute.toml; the database is created on first start
```

NPC LLM calls go to a local [Ollama](https://ollama.ai) instance by default (`http://localhost:11434`). Without Ollama running, players can still log in, move, talk to each other, use mail/boards/files, and engage with objects — only NPC reactions and memory summarisation require the LLM.

## Spinoff libraries

The X/Y/ZModem implementations needed for in-band BBS-era file transfer are being developed as **standalone, separately-published** Go libraries under the same author:

- `github.com/vaelen/go-xmodem`
- `github.com/vaelen/go-ymodem`
- `github.com/vaelen/go-zmodem`

These will land alongside milestone 9. They're general-purpose enough to be useful outside Wintermute; releasing them separately is the whole point.

## License

[MIT](LICENSE). Copyright (c) 2026 Andrew C. Young &lt;andrew@vaelen.org&gt;.
