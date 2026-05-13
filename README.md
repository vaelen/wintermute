# Wintermute

A text-based MUD/MUSH-style chat server, populated by LLM-powered NPCs with persistent memory, scriptable rooms and objects, and in-world BBS features (private mail, shared message boards, file uploads and downloads).

The engine is theme-agnostic; the reference world is Neuromancer/cyberpunk-flavored.

## Status

Early planning. The design is locked, the implementation roadmap is committed, and the code skeleton compiles — but the milestone-1 implementation has not yet started. Watch this space.

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
| [`docs/milestones/01-…md`](docs/milestones/) | Per-milestone executable plans. Ten milestones, each with goal, scope, schema changes, tasks, tests, and acceptance criteria. |

If you want to follow along with development, start with the design doc and then the milestone you're curious about.

## Building

Requires Go 1.23 or newer.

```sh
git clone git@github.com:vaelen/wintermute.git
cd wintermute
go build ./...
```

The `wintermute` binary currently exits with a "not yet implemented" message — that changes when milestone 1 lands.

## Spinoff libraries

The X/Y/ZModem implementations needed for in-band BBS-era file transfer are being developed as **standalone, separately-published** Go libraries under the same author:

- `github.com/vaelen/go-xmodem`
- `github.com/vaelen/go-ymodem`
- `github.com/vaelen/go-zmodem`

These will land alongside milestone 9. They're general-purpose enough to be useful outside Wintermute; releasing them separately is the whole point.

## License

[MIT](LICENSE). Copyright (c) 2026 Andrew C. Young &lt;andrew@vaelen.org&gt;.
