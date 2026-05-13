# Milestone 01 — Connection layer

## Goal

A player can `telnet` (or TLS) into the server, create an account, log in, and arrive at a placeholder room. The server handles telnet option negotiation, character-set conversion, password storage, and clean disconnects. No world content yet — just the front door.

## Dependencies

None. This is the foundation milestone.

## Scope

- Repo skeleton: `go.mod`, `LICENSE` (MIT), `README.md` stub, `Makefile`, `.golangci.yml`, `cmd/wintermute/main.go`.
- Source-file header convention enforced via `make check-headers`.
- Plain telnet listener with IAC option negotiation (CHARSET, NAWS, TTYPE; gracefully decline anything else).
- TLS listener on a separate port using `crypto/tls` + `golang.org/x/crypto/acme/autocert` (self-signed cert dev fallback).
- Per-connection goroutine and a `Session` struct.
- Account creation, login, password storage (argon2id), basic access levels.
- Character-set negotiation: UTF-8 preferred, fall back to CP437, then Latin-1.
- Persistent MOTD displayed after login.
- A placeholder "void" room — fixed string output for `look`, no real world model yet.
- Config loader (TOML).
- Graceful shutdown via SIGINT/SIGTERM.

## Out of scope

- Real rooms, objects, movement (M2).
- Anything LLM-related (M3+).
- Mail, boards, files (M6).
- Web UI.

## Architecture

### Packages introduced

- `cmd/wintermute` — entry point. Parses flags, loads config, opens DB, starts listeners, waits for shutdown.
- `internal/config` — TOML config loader; struct types for `[server]`, `[db]`, `[llm.default]` (the LLM section is parsed but unused this milestone).
- `internal/store` — SQLite open with WAL, `foreign_keys=ON`. Migration runner consuming embedded `.sql` files. Single-writer goroutine + write-request channel.
- `internal/auth` — password hashing (argon2id), account CRUD, login verification.
- `internal/net/telnet` — IAC parser, option-negotiation state machine, charset transcoding helpers, `Session` type that wraps a `net.Conn` and exposes `ReadLine` / `WriteString`.
- `internal/net/tls` — thin wrapper around `crypto/tls.Listen`. Wires autocert when configured, else loads a self-signed cert for dev.

### Key types

```go
// internal/net/telnet
type Session struct {
    conn     net.Conn
    enc      encoding.Encoding   // chosen via CHARSET
    width    int                 // from NAWS
    height   int
    termType string               // from TTYPE
    account  *auth.Account        // nil until login
    // ... cancellation, write mutex, etc.
}

func Accept(ctx, conn) (*Session, error)             // does IAC negotiation
func (s *Session) ReadLine(ctx) (string, error)      // UTF-8 in
func (s *Session) WriteString(ctx, s string) error   // transcodes to wire enc
func (s *Session) Close() error
```

```go
// internal/auth
type Account struct {
    ID           int64
    Username     string
    AccessLevel  string  // "admin" | "builder" | "player"
    CreatedAt    time.Time
    LastLoginAt  time.Time
}

func Create(ctx, username, password string) (*Account, error)
func Login(ctx, username, password string) (*Account, error)
```

### Data flow (login)

1. `net.Listener.Accept` → new goroutine.
2. `telnet.Accept` negotiates options (sends WILL CHARSET, WILL NAWS, WILL TTYPE; reads client responses).
3. Session prints banner + login prompt.
4. Read username, then password (with IAC ECHO suppression).
5. `auth.Login` verifies. On success, update `last_login_at`, attach account to session.
6. Print MOTD, enter the placeholder command loop. (Just an `> ` prompt that echoes "you are in the void" for now.)

## Schema changes

`internal/store/migrations/0001_accounts.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE accounts (
    id              INTEGER PRIMARY KEY,
    username        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash   TEXT NOT NULL,           -- argon2id encoded
    access_level    TEXT NOT NULL DEFAULT 'player'
                    CHECK (access_level IN ('admin','builder','player')),
    created_at      INTEGER NOT NULL,        -- unix epoch
    last_login_at   INTEGER
);

CREATE INDEX idx_accounts_username ON accounts(username);
```

`internal/store/migrations/0000_schema_version.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
```

## Implementation tasks

1. Initialize `go.mod` (module path to confirm with user; placeholder `github.com/vaelen/wintermute`).
2. Add `LICENSE` (MIT text from `plan.md`).
3. Add `Makefile` with `build`, `test`, `lint`, `check-headers`, `run`, `db-reset` targets.
4. Add `.golangci.yml` with reasonable defaults (`govet`, `staticcheck`, `errcheck`, `revive`, `gofmt`).
5. Implement `check-headers` shell script: enumerate tracked `.go`, `.lua`, `.sql`, `.toml`, `.sh` files and grep for `SPDX-License-Identifier: MIT`; non-zero exit on any miss.
6. Write `cmd/wintermute/main.go` with flag parsing, config load, logger init, signal handling, listener startup.
7. Implement `internal/config` TOML loader.
8. Implement `internal/store`: open DB with WAL, run embedded migrations, start the single writer goroutine, expose `Querier` (read) and `Writer` (write-via-channel) handles.
9. Embed `migrations/*.sql` with `//go:embed`.
10. Implement `internal/auth`: argon2id (params per OWASP), `Create`, `Login`, `Touch` (update `last_login_at`).
11. Implement `internal/net/telnet`: byte-level reader/writer, IAC state machine, option negotiation for CHARSET / NAWS / TTYPE, line buffering (CRLF), local echo control for password prompts.
12. Implement charset selection: `golang.org/x/text/encoding` + `transform`.
13. Implement `internal/net/tls`: self-signed cert generator for dev, autocert wrapper for prod.
14. Wire both listeners to the same `Session` handler.
15. Implement the placeholder login → MOTD → "void" command loop. `quit` disconnects; `help` prints a short message.
16. Unit tests:
    - IAC parser: option WILL/WONT/DO/DONT handshakes, subnegotiation framing, CHARSET REQUEST/ACCEPTED.
    - Charset transcoding round-trip for UTF-8, CP437, Latin-1.
    - Argon2id hash/verify.
17. Integration test: spin server on ephemeral ports, connect via `net.Dial`, create an account, log in, see MOTD, send `quit`.
18. Manual smoke test with a real `telnet` client and a real `openssl s_client` against the TLS port.

## Testing

- `go test ./...` covers unit + integration tests.
- Manual: `telnet localhost 23` and `openssl s_client -connect localhost:2323`.
- Verify `make check-headers` fails when a header is deliberately removed, succeeds otherwise.

## Acceptance criteria

1. A clean clone followed by `make build` produces a single binary.
2. Running `./wintermute --config wintermute.toml` listens on the configured telnet and TLS ports.
3. `telnet localhost <port>` performs IAC negotiation (verifiable via packet capture or a debug-log flag) and reaches a login prompt.
4. A new account can be created via the `new` command at the login prompt, then logged into.
5. The password is stored hashed; the raw password is not present in any log or table.
6. `last_login_at` updates on each successful login.
7. `SIGINT` cleanly disconnects all sessions and closes the DB.
8. `make check-headers` passes on all committed source files.

## Risks & open questions

- **Argon2id parameters**: pick defaults now (e.g. `memory=64MB, time=3, parallelism=2`) and document; revisit before public deployment.
- **Self-signed dev cert**: regenerate per-process or persist to disk? Lean toward persisting under `~/.wintermute/dev-cert.pem` for browser/cli reuse.
- **Module path**: `github.com/vaelen/wintermute` assumed; confirm at start of milestone.
- **Telnet on port 23** typically requires root or `setcap`; document the dev recommendation of using a high port (e.g. 2323 plain, 2424 TLS).
- **NAWS** is widely supported by modern MUD clients but not by stock `telnet(1)`; the `Session` should handle absence gracefully (defaults to 80×24).
- **CHARSET option** is rarely sent unless the client supports it; UTF-8 default + best-effort detection is fine.
