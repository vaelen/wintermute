# Milestone 01 — Connection layer

## Goal

A user can connect via plain TCP, telnet, or TLS, have their terminal capabilities auto-detected, confirm them at a pre-login prompt, create an account, log in, and arrive at a placeholder room. Output is composed internally as UTF-8 + ANSI and downgraded at the I/O boundary to whichever of six encodings the user chose. Telnet support, character encoding, and screen size are tracked as three independent session properties. Per-account terminal preferences are persisted and used as auto-detect defaults on subsequent logins.

## Dependencies

None. This is the foundation milestone.

## Scope

- Repo skeleton already in place; this milestone fills it: `Makefile`, `.golangci.yml`, a real `cmd/wintermute/main.go`, and the first set of `internal/*` package implementations.
- Source-file header convention enforced via `make check-headers`.
- **TCP listeners on plain and TLS ports.** TLS via `crypto/tls` + `golang.org/x/crypto/acme/autocert` (self-signed cert fallback for dev).
- **Per-connection capability detection** before login:
  - **Telnet detection** — peek the first bytes; enter telnet mode iff IAC (`0xFF`) appears. Telnet support is a per-session flag.
  - **Telnet option negotiation** (when telnet is enabled) — CHARSET (recorded as a hint), NAWS, TTYPE, ECHO/SGA. Anything else is gracefully rejected.
  - **Press-enter + ANSI probe** — send a `WINTERMUTE` banner + `PRESS ENTER TO BEGIN.` line + ANSI Device Attributes query (`ESC [ c`); read raw bytes until the user's Enter and scan the captured line for an `ESC [ ? … c` response. Cooked-mode terminals line-buffer their auto-response together with the user's Enter, so the response (if any) arrives in the same line.
  - **TTYPE hint** — use the telnet TTYPE response as a bias.
  - **Auto-detect defaults** — encoding, width, height, color.
  - **All-uppercase confirmation prompt** before login, with the auto-detected option flagged `[DEFAULT]`. Uppercase-only is what makes the prompt render natively on a PETSCII client in its default (uppercase / graphics) mode — no Shift Out is needed yet.
  - **Apply selection** — rebuild the session's `Encoder`; force 40-column width for PETSCII and ASCII unless overridden.
  - **Emit Shift Out (`0x0E`)** only when the session is transitioning **into** PETSCII (initial confirmation, `terminal encoding petscii` post-login, or a saved-prefs override that resolves to PETSCII). The byte is never sent for non-PETSCII encodings.
- **Six supported encodings** with internal representation always UTF-8 + ANSI:
  - **UTF-8** — pass-through.
  - **CP437** — `golang.org/x/text/encoding` transcode; preserve ANSI; map UTF-8 box-drawing to CP437 box-drawing (single- and double-line covered natively); shade/semi-graphics 1:1.
  - **ISO-8859-1** — `golang.org/x/text/encoding` transcode; preserve ANSI; **DEC line drawing default on** so box-drawing renders as real lines; shade/semi-graphics → space.
  - **MacRoman** — same posture as ISO-8859-1: DEC default on; semi-graphics → space.
  - **PETSCII** — custom translation tables for letters/digits/punctuation, color SGR → PETSCII color control bytes, single-line box-drawing → native PETSCII line graphics, shade/semi-graphics → PETSCII semi-graphics where available else space, cursor motion best-effort. DEC line drawing is meaningless on PETSCII and ignored.
  - **ASCII** — 7-bit only; strip all ANSI escapes; box-drawing → `-`/`|`/`+`; shade/semi-graphics → space; high-byte non-drawing → `?`; no color; DEC ignored.
- **Drawing-character vocabulary** in the engine's UTF-8 representation: single-line box (U+2500–U+257F), double-line box (the CP437-mirroring subset of U+2550–U+256C), and shade/block/semi-graphics (U+2580–U+259F).
- **Double-line downgrade**: in every encoding except UTF-8 and CP437, double-line characters are normalized to their single-line equivalent before character mapping.
- **Drawing-char "space not ?" rule**: drawing characters with no native target encoding emit space; non-drawing characters keep the `?` substitution.
- **DEC line drawing as an independent capability**, settable from the `terminal` command regardless of encoding. The encoder maintains line-drawing state across writes and emits the `ESC ( 0` / `ESC ( B` designators only at run boundaries — a 60-character horizontal rule sends one switch pair, not 60.
- **Five independent capability axes** on `Session`: telnet (bool), encoding (enum), screen size (width × height), color (bool), DEC line drawing (bool).
- **Per-account preferences** persisted (encoding, width, height, color) so a returning user's defaults match what they confirmed last time.
- **`terminal` command** (any access level) to view and change capabilities after login.
- **NAWS live updates** during a telnet session feed back into the session's tracked screen size.
- Account creation, login, password storage (argon2id), basic access levels.
- Persistent MOTD displayed after the encoding handshake and after login.
- A placeholder "void" room — fixed string output for `look`, no real world model yet (M2).
- Config loader (TOML).
- Graceful shutdown via SIGINT/SIGTERM.

## Out of scope

- Real rooms, objects, movement (M2).
- LLM features (M3+).
- Mail, boards, files (M6).
- BBS-era file transfer protocols (M9, M10).
- A web UI / web client.
- A "rich" terminal abstraction (true color, sixel, mouse) — out of scope; ANSI 16-color is the ceiling.
- Per-encoding *input* translation beyond what the encoding's standard implies. We assume what the user types arrives in their chosen encoding and is decoded to UTF-8; we do not attempt to be clever about partial keystroke streams.

## Architecture

### Packages introduced

- `cmd/wintermute` — entry point. Parses flags, loads config, opens DB, starts listeners, waits for shutdown.
- `internal/config` — TOML config loader.
- `internal/store` — SQLite open, migration runner, single-writer goroutine.
- `internal/auth` — password hashing (argon2id), account CRUD, login, persistence of terminal preferences.
- `internal/net/telnet` — IAC parser, option-negotiation state machine, ECHO suppression for password entry. **Used only when telnet is enabled** on the session.
- `internal/net/tls` — TLS listener helpers (self-signed dev cert, autocert in prod).
- `internal/term` — capability types, capability detection, per-encoding encoder/decoder, ANSI sequence parser, box-drawing translation, the confirmation-prompt dialog, and the post-login `terminal` command. This is the package that turns "UTF-8 + ANSI in, six-encoding wire formats out."

### Key types

```go
// internal/term/term.go
type Encoding int
const (
    EncodingUTF8 Encoding = iota
    EncodingCP437
    EncodingISO8859_1
    EncodingMacRoman
    EncodingPETSCII
    EncodingASCII
)

// Independent capability axes. Tracked separately on each Session.
type Capabilities struct {
    Telnet         bool      // server speaks IAC on this session
    Encoding       Encoding  // chosen encoding
    Width          int       // columns
    Height         int       // rows
    Color          bool      // emit color codes (ANSI SGR or encoding-native)
    DECLineDrawing bool      // substitute UTF-8 box-drawing with VT100 DEC Special Graphics
    TermType       string    // raw TTYPE if reported; informational only
}

// Encoder converts between UTF-8+ANSI (engine side) and the chosen wire encoding.
type Encoder interface {
    // EncodeOut writes engine-side UTF-8+ANSI bytes out to the wire.
    EncodeOut(p []byte) ([]byte, error)
    // DecodeIn translates wire bytes back to UTF-8 for the engine to read.
    DecodeIn(p []byte) ([]byte, error)
}

// Open builds the appropriate Encoder for a Capabilities snapshot.
func Open(c Capabilities) Encoder
```

```go
// internal/net/telnet/session.go
type Session struct {
    conn    net.Conn
    caps    term.Capabilities
    encoder term.Encoder         // rebuilt whenever caps change
    account *auth.Account        // nil until login
    width   int                  // mirror of caps.Width for fast reads
    // ... read/write mutex, cancellation, etc.
}

func Accept(ctx, conn) (*Session, error)
    // Runs capability detection, the confirmation prompt, and (if telnet
    // is on) IAC negotiation. Returns a session ready for login.

func (s *Session) ReadLine(ctx) (string, error)   // UTF-8 in
func (s *Session) WriteString(ctx, s string) error // s is UTF-8+ANSI; encoded on the way out
func (s *Session) Close() error
```

### Connection flow

```
TCP accept
  └─> wrap in telnet.Conn; send initial offers
        (WILL ECHO, WILL SGA, DONT LINEMODE, DO TTYPE, DO NAWS, WILL CHARSET)
  └─> brief settle (~200 ms); ProcessBuffered drains any IAC responses
        - if any IAC arrived, tc.Negotiated() == true; serverEcho auto-on
  └─> send "WINTERMUTE\r\n\r\nPRESS ENTER TO BEGIN.\r\n" + ESC[c (ANSI probe)
  └─> read raw bytes until \r or \n; scan for ESC[?...c
        - DA present → hints.ANSICapable = true
        - absent     → hints.ANSICapable stays false
        - hints.Telnet = tc.Negotiated()
        - hints.TermType / NAWS pulled from tc.State()
  └─> compute auto-detect defaults from (telnet status + TTYPE + ANSI hint)
  └─> open prompt encoder (ASCII; the prompt is uppercase-only ASCII)
  └─> render uppercase confirmation prompt with detected default flagged
  └─> read user choice (single char + Enter, or Enter for default)
  └─> finalize Capabilities; rebuild Encoder
  └─> if chosen encoding == PETSCII, send 0x0E (Shift Out) now
  └─> login prompt → auth → load saved prefs; if they differ AND resolve to
      PETSCII, send 0x0E before the re-render → MOTD → void room
```

### Per-encoding output pipeline

```
Engine code emits UTF-8 + ANSI (may include double-line box, shade blocks, semi-graphics)
  └─> term.Encoder.EncodeOut applies, in order:
        1. ANSI handling per encoding:
             - UTF-8/CP437/L1/MacRoman: pass through.
             - PETSCII: parse SGR sequences, emit equivalent PETSCII color
                        control bytes; map known cursor codes; drop the rest.
             - ASCII: strip all ESC[...] sequences entirely.
        2. DEC line-drawing substitution (only if caps.DECLineDrawing && encoding can carry escapes):
             - walk input; group consecutive single-line box-drawing chars.
             - on entering a graphics run, emit "ESC ( 0" if not already in graphics mode.
             - emit the DEC byte for each char (q ─, x │, l ┌, k ┐, m └, j ┘, n ┼, t ├, u ┤, v ┴, w ┬, ...).
             - on leaving a graphics run, emit "ESC ( B" once.
             - encoder state (in_graphics bool) persists across EncodeOut calls so adjacent writes don't double-bracket.
             - on session close, emit a final "ESC ( B" if still in graphics mode.
        3. Double-line downgrade (every encoding except UTF-8 and CP437):
             - replace each double-line char with its single-line equivalent
               ('═' → '─', '║' → '│', '╔' → '┌', etc.) before step 4.
        4. Character mapping per encoding:
             - UTF-8: identity.
             - CP437: x/text/encoding NewEncoder (covers all three drawing categories natively).
             - L1/MacRoman: x/text/encoding NewEncoder; box-drawing already
                            handled in step 2 if DEC was on, else '-'/'|'/'+';
                            shade/semi-graphics → space.
             - PETSCII: custom table; box-drawing → 0xC0/0xDD/etc;
                        shade where native, else space.
             - ASCII: strict ASCII; box-drawing → '-'/'|'/'+';
                      shade/semi-graphics → space; high-byte non-drawing → '?'.

Two general rules at step 4:
  - drawing chars (box, shade, semi-graphics) with no encoding equivalent  → space
  - non-drawing chars with no encoding equivalent                          → '?'
```

### Confirmation prompt

The prompt is **all uppercase**, constrained to characters whose byte positions render the same in every supported encoding **and** in PETSCII's default (uppercase / graphics) mode. Concrete text:

```
WELCOME TO WINTERMUTE.

TERMINAL TYPE: U - UNICODE [MODERN, DEFAULT], D - DOS [CP437],
M - MAC [CLASSIC], L - LATIN-1, P - PETSCII, A - ASCII:
```

The `[DEFAULT]` marker attaches to whichever option auto-detect chose. Examples for the same prompt with different detected defaults:

- ANSI/UTF-8 client → `U - UNICODE [MODERN, DEFAULT]`
- DOS-style TTYPE   → `D - DOS [CP437, DEFAULT]`
- PETSCII TTYPE     → `P - PETSCII [DEFAULT]`
- nothing detected  → `A - ASCII [DEFAULT]`

Pressing Enter accepts the default. Single-letter responses (case-insensitive) override.

The prompt encoder is always ASCII regardless of what auto-detect chose. PETSCII clients in default mode render uppercase ASCII bytes as PETSCII uppercase letters; the punctuation set used (`:`, `,`, `-`, `(`, `)`, `[`, `]`, space, digits) is identical across all six encodings. So no Shift Out has been emitted yet, and the prompt is unambiguously readable on every kind of client we support.

After selection, the engine rebuilds the session encoder for the chosen encoding. **If the chosen encoding is PETSCII, `0x0E` (Shift Out) is emitted before any further output** so the C64 enters mixed-case mode and subsequent text — including the login prompt — renders correctly with PETSCII's case-swap.

## Schema changes

`internal/store/migrations/0001_accounts.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE accounts (
    id                   INTEGER PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash        TEXT NOT NULL,                       -- argon2id encoded
    access_level         TEXT NOT NULL DEFAULT 'player'
                         CHECK (access_level IN ('admin','builder','player')),
    created_at           INTEGER NOT NULL,
    last_login_at        INTEGER,
    -- Persisted terminal preferences (auto-detect bias for next login).
    -- NULL on any column means "use the auto-detected / encoding default."
    terminal_encoding    TEXT
                         CHECK (terminal_encoding IS NULL OR
                                terminal_encoding IN ('utf8','cp437','iso88591','macroman','petscii','ascii')),
    terminal_width       INTEGER,
    terminal_height      INTEGER,
    terminal_color       INTEGER,                             -- 0 or 1
    terminal_dec_lines   INTEGER                              -- 0 or 1; toggles VT100 DEC Special Graphics
);

CREATE INDEX idx_accounts_username ON accounts(username);
```

`internal/store/migrations/0000_schema_version.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
```

## Implementation tasks

### Repo bootstrap (one-time)

1. Add `Makefile` (`build`, `test`, `lint`, `check-headers`, `run`, `db-reset`).
2. Add `.golangci.yml` with `govet`, `staticcheck`, `errcheck`, `revive`, `gofmt`.
3. Implement `check-headers`: enumerate tracked `.go`/`.lua`/`.sql`/`.toml`/`.sh` files, grep each for `SPDX-License-Identifier: MIT`, non-zero exit on any miss.
4. Replace the stub `cmd/wintermute/main.go` with real entry-point code (config, logger, DB, listeners, signal handling).

### Storage and accounts

5. Implement `internal/config` TOML loader, including `[server]`, `[db]`, `[llm.default]` sections (LLM is parsed but unused this milestone).
6. Implement `internal/store`: open with WAL + `foreign_keys=ON`, single writer goroutine fed by a `chan WriteRequest`, embedded migrations via `//go:embed`.
7. Implement `internal/auth`: argon2id (OWASP-aligned params), `Create`, `Login`, `Touch`, plus accessors for the four `terminal_*` columns.

### Connection acceptance and capability detection

8. Implement `internal/net/telnet`:
   - Byte-level reader/writer over `net.Conn`.
   - IAC state machine (WILL/WONT/DO/DONT, SB/SE subnegotiation framing).
   - Options: TTYPE (subneg fetch), NAWS (subneg parse), CHARSET (parse hint, do not override engine's authoritative choice), ECHO/SGA (password entry).
   - **Telnet detection helper**: peeks the first 200 ms of bytes; returns `(isTelnet bool, buffered []byte)`.
9. Implement `internal/net/tls`: dev self-signed cert generation persisted to `~/.wintermute/dev-cert.pem`; autocert wrapper for prod.
10. Implement `internal/term`:
    - `Capabilities`, `Encoding`, `Encoder` types (Capabilities includes `DECLineDrawing bool`).
    - Encoders for each of the six encodings (separate files: `utf8.go`, `cp437.go`, `iso88591.go`, `macroman.go`, `petscii.go`, `ascii.go`).
    - ANSI sequence parser (used by PETSCII and ASCII encoders to recognize and selectively translate or strip).
    - Drawing-character tables: single-line box, double-line box, shade/semi-graphics, with per-encoding mappings. The "drawing → space, non-drawing → ?" rule is implemented once in a shared codepath and reused across encoders.
    - Double-line normalization table (`═` → `─`, etc.) used by all non-(UTF-8|CP437) encoders.
    - **DEC line-drawing emitter** with persistent `in_graphics` state. Single function that takes a UTF-8 run and a target encoding and emits the DEC-bracketed byte stream, never bracketing a single character at a time when adjacent chars are also line-drawing. Tested independently with golden outputs.
    - PETSCII tables: UTF-8 ↔ PETSCII shifted/unshifted, ANSI SGR → PETSCII color control bytes, PETSCII line graphics, PETSCII semi-graphics where they exist.
    - `HasDAResponse([]byte) bool` and the ANSI probe byte sequence (`ANSIProbe`); the session layer drives the press-enter probe and feeds the scanned-out hint into `AutoDetect`.
    - `ConfirmPrompt(ctx, session, defaults)` — renders the prompt, parses the response, returns a confirmed `Capabilities`.
11. Wire it all together in the connection acceptor: telnet peek → option negotiation if applicable → press-enter banner + ANSI Device Attributes probe sent in one write → read raw input until \r or \n → scan the captured bytes for an `ESC [ ? … c` response to set the ANSI hint → compute defaults → render uppercase confirmation prompt via an ASCII prompt encoder → read selection → reconfigure the session encoder → **if chosen encoding is PETSCII, write `0x0E` Shift Out** → continue with login. Saved per-account prefs are loaded *after* login and applied to subsequent renders; if the applied encoding is PETSCII (and the pre-login choice wasn't), another `0x0E` is emitted before re-rendering.

### Login and session lifecycle

12. Implement login flow: prompt for username + password (echo suppressed via telnet IAC ECHO if telnet is on; otherwise echo client-side and accept the security trade-off, documenting it).
13. On successful login: update `last_login_at`, save current `Capabilities` to the account's `terminal_*` columns, print MOTD (rendered using the chosen encoding), drop into placeholder "void" command loop.
14. Implement the `terminal` command: prints the current axes; subcommands update them and rebuild the encoder.
    - `terminal encoding utf8|cp437|iso88591|macroman|petscii|ascii`
    - `terminal width <n>`
    - `terminal height <n>`
    - `terminal color on|off`
    - `terminal lines vt100|native` — toggles DEC Special Graphics output regardless of encoding (`native` means each encoding's default per the table in design.md).
    - `terminal` (no args) — prints all five axes plus the telnet flag.
    Changes are persisted to the account.

### Tests

15. Unit tests:
    - IAC parser: WILL/WONT/DO/DONT handshakes; subnegotiation framing; CHARSET parse; NAWS parse; TTYPE fetch.
    - Telnet peek: classifies IAC vs non-IAC correctly; returns buffered bytes intact for non-telnet.
    - DA response detection: `HasDAResponse` finds a complete `ESC [ ? … c` anywhere in a buffer; rejects malformed prefixes; ignores non-digit/semicolon bytes between `?` and `c`.
    - Each encoder's `EncodeOut`/`DecodeIn` round-trip for a representative string.
    - PETSCII: shifted-mode handling, ANSI SGR → PETSCII color mapping, box-drawing mapping, shade-block downgrade to space.
    - ASCII: ANSI stripping, box-drawing approximation, shade → space, non-drawing high-byte → `?`.
    - Double-line normalization: `╔═╗║╚═╝` in ISO-8859-1 with DEC off renders as `+-+|+-+`; with DEC on renders as the DEC equivalents of `┌─┐│└─┘` bracketed once by `ESC ( 0` / `ESC ( B`.
    - DEC batching: emitting `─────────` (nine horizontal line chars) produces exactly one `ESC ( 0`, nine `q` bytes, and one `ESC ( B` — not nine bracket pairs.
    - DEC state persists across `EncodeOut` calls: writing `─` then `─` then `─` in three separate calls produces one open + three `q` + one close.
    - DEC close on session end: closing a session while still in graphics mode emits the trailing `ESC ( B`.
    - Argon2id hash/verify.
16. Integration tests:
    - Connect via raw TCP (no IAC) — server must not block waiting for telnet, must reach the prompt.
    - Connect with IAC sent immediately — server enters telnet, negotiates, reaches the prompt.
    - Cycle each encoding via the confirmation prompt and verify the post-prompt MOTD bytes match expected.
    - PETSCII selection results in `0x0E` being emitted before the next prompt.
    - Saved preferences from a prior login are reflected as the default on the next connect by the same user (requires the prompt to happen *after* login? — see Risks for the ordering decision).
17. Manual smoke tests:
    - `telnet localhost <port>` reaches the prompt (defaults to whatever auto-detect picks).
    - `nc localhost <port>` reaches the prompt (no IAC).
    - `openssl s_client -connect localhost:<tlsport>` reaches the prompt over TLS.
    - A real BBS / C64 client (or emulator) sees readable PETSCII output after selecting PETSCII.

## Testing

- `go test ./...` covers unit + integration tests.
- Manual: `telnet`, `nc`, `openssl s_client`, plus at least one PETSCII-capable client (an emulated C64 via VICE with a TCP-modem patch is fine).
- Verify `make check-headers` fails when a header is deliberately removed, succeeds otherwise.

## Acceptance criteria

1. A fresh build started from an empty DB accepts connections on a plain port, a TLS port, and through `nc` (raw TCP) without hanging.
2. The capability prompt appears within ~500 ms of connect for every transport.
3. Choosing each of the six encodings produces visibly correct output for an MOTD that contains a single-line UTF-8 border, a double-line UTF-8 border, a shade-block backdrop, a line with ANSI color, and a paragraph of mixed-case text. "Correct" means:
   - UTF-8: identical bytes.
   - CP437: native CP437 box-drawing (single and double), native shade blocks (0xB0/0xB1/0xB2/0xDB), ANSI color intact.
   - ISO-8859-1 / MacRoman, DEC on (default): single-line border rendered via DEC Special Graphics (`ESC ( 0` ... `ESC ( B`), double-line border normalized to single-line then rendered via DEC, shade blocks replaced with spaces, ANSI color intact.
   - ISO-8859-1 / MacRoman, DEC off: borders rendered as `-`/`|`/`+`, shade blocks as spaces.
   - PETSCII: the engine emits `0x0E` (Shift Out) at the moment PETSCII is selected, putting the C64 into mixed-case mode for the rest of the session; single- and double-line borders both rendered via native PETSCII line graphics; shade blocks rendered via native PETSCII semi-graphics or spaces where there is no equivalent; PETSCII color codes in place of ANSI SGR. On selections that do **not** choose PETSCII, the engine never emits `0x0E`.
   - ASCII: no escape sequences in the byte stream, no high-bytes, borders rendered as `-`/`|`/`+`, shade blocks as spaces.
4. NAWS during a telnet session updates `session.caps.Width/Height` and subsequent output wraps to the new width.
5. A logged-in player running `terminal encoding ascii` sees subsequent output stripped of ANSI; running `terminal encoding utf8` restores full output. `terminal lines vt100` enables DEC line drawing regardless of encoding (verified by inspecting wire bytes for the `ESC ( 0` / `ESC ( B` brackets). All such changes persist across logout/login.
6. A player's previous `Capabilities` choice (e.g. PETSCII / 40×24 / color on) is the default on their next connection from a matching client. Auto-detect overrides only when the saved value is incompatible with the current connection (e.g. PETSCII was saved but the new connection is clearly ANSI).
7. `SIGINT` cleanly disconnects all sessions and closes the DB.
8. `make check-headers` passes on all committed source files.

## Risks & open questions

- **PETSCII pre-selection rendering (decided).** Use an **all-uppercase ASCII** prompt during the capability handshake. Uppercase ASCII letters and the punctuation set we use render natively on a PETSCII client in its default (uppercase / graphics) mode, so no protocol acrobatics are needed before the user has picked. `0x0E` (Shift Out) is then emitted *only* at the moment the session transitions into PETSCII: at the end of the prompt if PETSCII was chosen, on `terminal encoding petscii` post-login, or after a saved-prefs override that resolves to PETSCII. Non-PETSCII sessions never see Shift Out from the engine, which means modern clients get a clean byte stream from the start (no possibly-confusing `0x0E` on connect).
- **DEC graphics designator choice (decided).** Use `ESC ( 0` (designate G0 → DEC Special Graphics) and `ESC ( B` (designate G0 → ASCII) rather than the alternative SO/SI mechanism. SO is already in use as the connect-time PETSCII switch; mixing it with line-drawing toggles invites confusion and would behave differently across clients. The `ESC ( 0` / `ESC ( B` pair is the canonical VT100 idiom for this use, is unambiguous, and does not conflict with the PETSCII path.
- **DEC graphics trailing state (decided).** The encoder always emits a closing `ESC ( B` on session shutdown if it would otherwise leave the terminal in graphics mode. This falls out naturally of the "emit only at transitions" rule applied at close.
- **Preference loading order (decided).** The connect-time confirmation prompt always uses live auto-detect, because the user hasn't authenticated yet. After login, if the account has saved preferences that differ from what the user picked at the prompt, apply them silently and re-render the MOTD. Players can override at any time with the `terminal` command. This keeps the prompt simple and avoids ordering the encoding prompt after the username.
- **ANSI probe leakage on non-ANSI terminals.** A terminal that doesn't recognize `ESC [ c` renders the three bytes as a stray escape-sequence start. On PETSCII clients in default mode this is also harmless garble (PETSCII just shows the bytes' glyphs and moves on). Because the probe is sent in the same write as the human-readable `PRESS ENTER TO BEGIN.`, the artifact appears on the *same line* and the next thing the user does is press Enter, so it scrolls away before they read the encoding prompt.
- **Telnet ECHO during password entry on non-telnet sessions.** Without telnet, we can't suppress local echo. The password is still hashed server-side but is visible on the user's screen. Document this in the manpage; recommend telnet/TLS for production.
- **40-column wrapping.** The MOTD and login text need to wrap at the chosen width. Some content is internal and not directly under the user's control. Wrap on whitespace; truncate long unbroken tokens with `…` (or `...` in ASCII).
- **Argon2id parameters.** Pick defaults (e.g. `memory=64MB, time=3, parallelism=2`); document; revisit before public deployment.
- **Self-signed dev cert.** Persist at `~/.wintermute/dev-cert.pem` so browsers/CLIs can pin it across runs.
- **Telnet on port 23.** Requires root or `setcap`; document the high-port dev recommendation (2323 plain, 2424 TLS).
- **Color flag default.** PETSCII has color support; CP437 has color (via ANSI); UTF-8 has color (via ANSI). ASCII does not. Default color to on whenever the encoding can carry it, off for ASCII; user override via `terminal color on|off` regardless.
- **NAWS in non-telnet sessions.** No way to know the user's screen size. Default to encoding-appropriate (40 for PETSCII/ASCII, 80 otherwise); rely on `terminal width N` for everything else.
- **Module path.** `github.com/vaelen/wintermute` is confirmed.
