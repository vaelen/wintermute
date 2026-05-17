# Milestone 06 — Mail, boards, HTTPS file transfer

## Goal

Players can send private mail, read and post to shared message boards, and upload/download files via short-lived HTTPS URLs. All metadata is in SQLite; file blobs are content-addressed on disk. BBS-era protocols (X/Y/Z/Kermit) are deferred to M9–M10; this milestone ships the modern path so the features are usable while M9 is being built.

All player-facing mail/board/file commands are reached **inside a terminal engagement** (M5.7) rather than at the world prompt — sitting down at a terminal opens a private command interface, and the commands below run there. Other players in the room see that the engaged player is at the terminal but not the content. The terminal command-table dispatch is set up in M5.7; M6 fleshes out the handlers and the backing services.

## Dependencies

- M5.7 (diegetic engagement primitive): the terminal engagement handler hosts every command in this milestone. M5.7 ships these commands as stubs returning `not yet implemented`; M6 replaces the stubs with the real implementations.
- M2 (world layer): ACL bits set up in M5 are used; the seed world (extended in M5.7 with a lobby terminal) is where these commands first become reachable.

## Scope

- Mail: schema, commands (`mail`, `mail send`, `mail read`, `mail delete`), unread-count indicator on login.
- Boards: schema, commands (`bb`, `bbread`, `bbpost`, `bbcatchup`), per-board read/post/admin ACLs.
- Blob store: content-addressed disk layout, dedup by SHA-256, mime sniffing.
- HTTPS file-transfer endpoint: separate listener (port configurable), `POST /upload/<token>` and `GET /download/<token>`. One-shot, 5-minute tokens.
- In-world commands `upload` and `download` that mint a token, print a URL, and wait (with a configurable timeout) for the transfer to complete.
- A small `@cleanup-files` admin command to prune orphaned blobs.

## Out of scope

- BBS protocols (M9, M10).
- A web UI for mail/boards. (The HTTP listener exists for file transfer only this milestone; it can grow later.)
- Mail attachments. Files are a separate feature; if a player wants to share a file via mail, they include the in-world file slug in the body.
- Encryption-at-rest for files. The user's filesystem-level controls are the trust boundary.

## Architecture

### Packages introduced

- `internal/mail` — schema queries + command handlers.
- `internal/boards` — schema queries + command handlers + ACL checks.
- `internal/files` — blob store, metadata, ACL checks, token issuance.
- `internal/http` — the HTTPS listener and route handlers.

### Mail

Commands (issued inside a terminal engagement; see M5.7):

- `mail` — show inbox: sender, subject, date, unread flag.
- `mail send <user> "<subject>"` — enters compose mode (same `.`-terminated paste flow as `@edit`).
- `mail read <id>` — show message body; marks read.
- `mail reply <id>` — compose a reply; the new message records the parent's MSGID in `reply_to_msgid` so future FidoNet netmail tooling can follow the chain.
- `mail delete <id>` — delete from inbox.

A player's unread mail count is shown on login (`You have 3 new messages.`). The login-time notification is *not* part of an engagement — it's an out-of-band line written at the post-MOTD step.

### Boards

Commands (issued inside a terminal engagement; see M5.7):

- `bb` — list boards visible to the player (read ACL).
- `bbread <board>` — list threads in the board, newest activity first, with reply counts and unread indicators.
- `bbread <board> <id>` — show that post in the context of its thread (root + ancestors + the post itself).
- `bbthread <board> <id>` — show every post in the thread containing `<id>`, oldest first.
- `bbpost <board> "<subject>"` — start a new thread (paste-mode compose).
- `bbreply <board> <id>` — reply to an existing post (paste-mode compose); the new post inherits the thread and links to the parent.
- `bbcatchup <board>` — mark all posts in board as read.

Per-board ACLs (read/post/admin) are wired through M5's permissions plumbing. Admin scripts create boards and set ACLs via `wintermute.board.create({slug=..., name=..., read=..., post=..., admin=..., network=..., area_tag=...})`. `network` names a configured FTN network (see *FTN networks* below) and defaults to `local`; `area_tag` is the echo's tag within that network (e.g. `FIDONET.GENERAL`, `FSX_GEN`) and stays NULL for boards on the `local` network.

#### FTN networks

Wintermute supports being a node on **multiple** FTN networks at once (FidoNet, RetroNet, fsxNet, AmigaNet, …). Each network is its own address space; the same zone number can mean different things in different networks. The FTSC answer is domain-form addressing per FSC-0089 / FSC-0090: a full address looks like `<zone>:<net>/<node>.<point>@<domain>`, e.g. `1:234/5.0@fidonet` or `21:1/100.0@fsxnet`.

The `local` network is treated identically to any other network — it just isn't gateable. Everything that doesn't cross a real FTN boundary lives there: mail between two accounts on this instance, local-only message boards, system announcements. There is **one** mechanism for boards and mail; "no FTN" is just the `local` network.

Configuration (in `wintermute.toml`):

```toml
[[ftn.network]]
slug    = "local"        # always present; auto-created if not declared
name    = "Local"
domain  = "local"        # full origaddr form: "255:255/255.0@local"
addr    = "255:255/255.0"
default = true

[[ftn.network]]
slug    = "fidonet"
name    = "FidoNet"
domain  = "fidonet"
addr    = "1:234/5.0"

[[ftn.network]]
slug    = "fsxnet"
name    = "fsxNet"
domain  = "fsxnet"
addr    = "21:1/100.0"
```

At startup, each `[[ftn.network]]` block is upserted into the `ftn_networks` table. Exactly one network is marked `default = true` and is the network new boards land on when `wintermute.board.create` is called without an explicit `network=` argument. If the operator hasn't declared the `local` network or hasn't marked any network default, the startup code auto-creates `local` (domain `local`, address `255:255/255.0`) and marks it default. The `local` network is also the network used for mail between two local accounts, regardless of which network is currently flagged default.

#### Threading (FidoNet-compatible)

Threading uses FidoNet semantics (FTS-0009 MSGID/REPLY kludges) so a later milestone can add netmail/echomail ingress and egress without reshaping the schema or rewriting message bodies.

- Every post and message has a **MSGID** of the form `<origaddr> <serialno>` (FTS-0009). For locally-originated content, `<origaddr>` is the full domain-form address (e.g. `1:234/5.0@fidonet` or `255:255/255.0@local`) of the row in `ftn_networks` attached to the board, or of the `local` network for mail between local accounts; `<serialno>` is an 8-hex-digit monotonic counter unique within that origaddr. MSGIDs ingressed from FTN later are preserved verbatim. The MSGID parser must handle the FTS-0009 quoted-origaddr form (`^aMSGID: "addr with spaces" deadbeef`, with `""` as an embedded literal quote) — don't naively split on the last space.
- A reply records its parent in **`reply_to_msgid`** — the FTS-0009 `^aREPLY` value. NULL means the post is a thread root.
- **`thread_root_msgid`** (boards only) is denormalized at insert time by walking the REPLY chain; it equals `msgid` for roots. Listing threads is then a single indexed query, no recursive CTE required.
- `reply_to_msgid` is intentionally **not** a foreign key. Echomail can arrive out of order, so a reply whose parent we have not seen yet must still be insertable. Such a post is treated as a thread root until the parent arrives; a small reconciliation pass (TODO comment in M6, real implementation in the FTN milestone) re-roots orphans when their parent appears.
- Surrogate `board_posts.id` (INTEGER PK) is used only for local references (`board_reads.post_id`, command-line shorthand). Nothing outside this server is allowed to depend on it — cross-system references always use MSGID.
- Subjects are stored verbatim. For thread display, the renderer normalizes by stripping leading `Re:` / `RE:` / `Re^N:` prefixes for grouping/heading purposes only; the post's original subject is preserved.
- The MSGID issuer is a small service in `internal/store` that hands out the next serialno from a counter persisted in a `kv` row keyed by full origaddr (so each `(network, our_addr)` pair has its own monotonic counter). The single-writer SQLite goroutine owns the increment to keep it atomic.

#### FTN-fidelity fields (carried on every message)

So that messages ingressed from a future FTN gateway survive round-trip without data loss, both `mail` and `board_posts` carry a uniform set of FTN-derived columns. For locally-originated content these are auto-filled at insert time; for FTN-ingressed content they are populated from the message's kludges and stripped from the rendered body.

- **`attributes INTEGER`** — FTS-0001 AttributeWord bitfield (Private, Crash, KillSent, FileAttached, ReturnReceiptRequest, …). Stored as INTEGER so unknown bits round-trip unchanged.
- **`charset TEXT`** — FSC-0054 CHRS kludge (`"UTF-8 4"`, `"CP437 2"`, …). Default `"UTF-8 4"` for locally-originated; preserve verbatim on ingress so we never transcode-on-read.
- **`pid TEXT`** — FSC-0046 PID kludge: program-ID of the originating editor. `Wintermute/<version>` for locally-originated.
- **`tz_offset INTEGER`** — FRL-1004 TZUTC kludge: signed minutes east of UTC. Captures the originator's local time zone so we don't lose it on re-emit.
- **`kludges TEXT`** — JSON-encoded ordered list of `[name, value]` pairs for every `^a…` kludge line not already pulled into a dedicated column. FTS-0009 mandates that intermediate systems must not strip or modify MSGID/REPLY; the same hygiene applies to all kludges — this opaque-passthrough column is the single most important field for future-proofing, because it lets us carry forward kludges we don't yet recognize.

Boards-only (echomail-shaped):

- **`area_tag TEXT`** — FTS-0004 AREA: tag, denormalized from the board's `area_tag` at insert. Storing it per-post preserves the original tag if the local board is later renamed.
- **`tearline TEXT`** — FTS-0004 tear line (`--- <product>`). Auto-filled to `--- Wintermute/<version>` for local; parsed-and-stripped on ingress, re-added on egress.
- **`origin_line TEXT`** — FTS-0004 origin line (`* Origin: <name> (<addr>)`). Auto-filled for local from the board's network. Same parse-on-ingress / re-add-on-egress lifecycle as `tearline`.
- **`seen_by TEXT`** — newline-separated SEEN-BY entries. Local-originated starts with our address in the board's network.
- **`path TEXT`** — newline-separated PATH entries. Local-originated starts with our address in the board's network.

Mail-only (netmail-shaped):

- **`from_name TEXT`, `to_name TEXT`** — netmail addresses recipients by name + FTN address, not by local account. For local mail these denormalize `accounts.username`; for future netmail ingress, `to_id`/`from_id` may be NULL and the name fields are canonical.
- **`from_addr TEXT`, `to_addr TEXT`** — full domain-form FTN addresses. For mail between two local accounts, both equal the `local` network's `our_addr` (`255:255/255.0@local` by default).

The body column stores only the canonical message text — kludge lines, tearline, origin line, SEEN-BY, and PATH are stripped on ingress and re-added on egress. This keeps the displayed body clean while preserving everything for round-trip.

### Files

Blob store layout on disk:

```
<files_root>/
├── ab/
│   └── cd/
│       └── abcdef0123... (full SHA-256)
└── ...
```

The first two hex bytes form two directories of one byte each (256×256 fanout). Blobs are write-once and never modified.

```go
// internal/files/store.go
type File struct {
    ID         int64
    Slug       string             // human-readable, unique
    Hash       string             // sha256 hex
    Size       int64
    MIME       string
    OwnerID    int64
    CreatedAt  time.Time
}

func (s *Store) Put(ctx, r io.Reader) (hash string, size int64, mime string, err error)  // streams to disk
func (s *Store) Get(ctx, hash string) (io.ReadCloser, error)
func (s *Store) NewFile(ctx, slug string, owner int64, hash string, size int64, mime string) (*File, error)
func (s *Store) DeleteFile(ctx, id int64) error
```

Mime sniffing via `net/http.DetectContentType` on the first 512 bytes.

### Tokens and HTTP endpoints

```go
// internal/files/token.go
type Token struct {
    Value     string        // 32 hex chars, crypto/rand
    Kind      string        // "upload" | "download"
    AccountID int64
    FileID    *int64        // set for downloads; nil for uploads (filled in after upload completes)
    Slug      string        // proposed slug for upload
    ExpiresAt time.Time
    UsedAt    *time.Time
}

func (s *Store) IssueUpload(ctx, accountID int64, slug string, ttl time.Duration) (Token, error)
func (s *Store) IssueDownload(ctx, accountID int64, fileID int64, ttl time.Duration) (Token, error)
func (s *Store) Redeem(ctx, value string) (Token, error)        // marks used
```

HTTP routes:

- `POST /upload/<token>` — body is the file. Server streams to blob store, computes hash, creates `files` row, marks token used. Responds with `{ "slug": "...", "size": N, "hash": "..." }`.
- `GET /download/<token>` — server resolves token to a file, streams blob with `Content-Disposition: attachment; filename=...`. Marks token used.

Both routes refuse multiple uses of the same token.

### In-engagement commands

Issued inside a terminal engagement (see M5.7):

- `upload "<slug>" [<description>]` — issues an upload token, prints:
  ```
  Upload URL (valid for 5 minutes):
    https://<public-host>:<port>/upload/<token>

  Example: curl --upload-file <local> https://<public-host>:<port>/upload/<token>
  ```
  The terminal stays usable; upload completion is signalled to the player via an asynchronous message (`[terminal] Upload received: "filename.txt" (12.3 KB).`) that is delivered to the player's session whether or not they are still at the terminal. If they have disconnected, it persists for next login.
- `download <slug>` — checks read ACL on the file, issues a download token, prints the URL.

Both commands also accept a `--noauto` flag (later, when M9 lands ZModem auto-detection) to skip in-band BBS detection. For M6, only the HTTPS path exists.

## Schema changes

`internal/store/migrations/0011_ftn_networks.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE ftn_networks (
    id         INTEGER PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,    -- e.g. "fidonet", "fsxnet", "local"
    name       TEXT NOT NULL,           -- display name
    domain     TEXT NOT NULL UNIQUE,    -- FSC-0089 domain (e.g. "fidonet", "fsxnet")
    our_addr   TEXT NOT NULL,           -- our 5D address in this network ("1:234/5.0")
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1))
);

-- Exactly one default network. A partial unique index enforces it.
CREATE UNIQUE INDEX idx_ftn_networks_default ON ftn_networks(is_default) WHERE is_default = 1;
```

`internal/store/migrations/0012_mail.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE mail (
    id              INTEGER PRIMARY KEY,
    network_id      INTEGER NOT NULL REFERENCES ftn_networks(id) ON DELETE RESTRICT,
    from_id         INTEGER REFERENCES accounts(id) ON DELETE SET NULL,   -- NULL for FTN-ingressed mail from a non-local sender
    to_id           INTEGER REFERENCES accounts(id) ON DELETE SET NULL,   -- NULL for FTN-ingressed mail to a non-local recipient
    from_name       TEXT NOT NULL,                                        -- canonical sender display name (netmail TO_USER field)
    to_name         TEXT NOT NULL,                                        -- canonical recipient display name
    from_addr       TEXT NOT NULL,                                        -- full domain-form address of sender
    to_addr         TEXT NOT NULL,                                        -- full domain-form address of recipient
    msgid           TEXT NOT NULL UNIQUE,                                 -- FTS-0009 "<origaddr> <serialno>"
    reply_to_msgid  TEXT,                                                 -- FTS-0009 REPLY; not a FK (out-of-order netmail)
    origin_addr     TEXT NOT NULL,                                        -- the <origaddr> portion of msgid
    attributes      INTEGER NOT NULL DEFAULT 0,                           -- FTS-0001 AttributeWord bitfield
    charset         TEXT NOT NULL DEFAULT 'UTF-8 4',                      -- FSC-0054 CHRS
    pid             TEXT,                                                 -- FSC-0046 PID kludge
    tz_offset       INTEGER,                                              -- FRL-1004 TZUTC, signed minutes east of UTC
    kludges         TEXT,                                                 -- JSON [[name, value], ...] of unrecognized ^a kludges
    subject         TEXT NOT NULL,
    body            TEXT NOT NULL,                                        -- canonical body; kludges stripped
    sent_at         INTEGER NOT NULL,
    read_at         INTEGER
);

CREATE INDEX idx_mail_to_unread    ON mail(to_id, read_at);
CREATE INDEX idx_mail_reply_parent ON mail(reply_to_msgid);
CREATE INDEX idx_mail_network      ON mail(network_id);
```

`internal/store/migrations/0013_boards.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE boards (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    network_id   INTEGER NOT NULL REFERENCES ftn_networks(id) ON DELETE RESTRICT,
    area_tag     TEXT,                                                   -- FTS-0004 echo tag within the network; NULL for `local` boards
    read_perms   INTEGER NOT NULL DEFAULT 0,
    post_perms   INTEGER NOT NULL DEFAULT 0,
    admin_perms  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (network_id, area_tag)                                       -- same tag is allowed across different networks; SQLite treats NULL as distinct so multiple `local` boards with NULL area_tag coexist
);

CREATE TABLE board_posts (
    id                 INTEGER PRIMARY KEY,
    board_id           INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    network_id         INTEGER NOT NULL REFERENCES ftn_networks(id) ON DELETE RESTRICT,
    author_id          INTEGER REFERENCES accounts(id) ON DELETE SET NULL,    -- NULL for FTN-ingressed posts from non-local authors
    author_name        TEXT NOT NULL,                                         -- canonical author display name (echomail FROM_USER)
    msgid              TEXT NOT NULL UNIQUE,                                  -- FTS-0009 "<origaddr> <serialno>"
    reply_to_msgid     TEXT,                                                  -- FTS-0009 REPLY; not a FK (out-of-order echomail)
    thread_root_msgid  TEXT NOT NULL,                                         -- denormalized; = msgid for roots
    origin_addr        TEXT NOT NULL,                                         -- the <origaddr> portion of msgid
    area_tag           TEXT,                                                  -- FTS-0004 AREA, denormalized from boards.area_tag at insert
    attributes         INTEGER NOT NULL DEFAULT 0,                            -- FTS-0001 AttributeWord bitfield
    charset            TEXT NOT NULL DEFAULT 'UTF-8 4',                       -- FSC-0054 CHRS
    pid                TEXT,                                                  -- FSC-0046 PID kludge
    tz_offset          INTEGER,                                               -- FRL-1004 TZUTC, signed minutes east of UTC
    tearline           TEXT,                                                  -- FTS-0004 "--- <product>" line
    origin_line        TEXT,                                                  -- FTS-0004 "* Origin: <name> (<addr>)" line
    seen_by            TEXT,                                                  -- newline-separated SEEN-BY entries
    path               TEXT,                                                  -- newline-separated PATH entries
    kludges            TEXT,                                                  -- JSON [[name, value], ...] of unrecognized ^a kludges
    subject            TEXT NOT NULL,
    body               TEXT NOT NULL,                                         -- canonical body; kludges/tearline/origin/SEEN-BY/PATH stripped
    posted_at          INTEGER NOT NULL
);

CREATE TABLE board_reads (
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    post_id      INTEGER NOT NULL REFERENCES board_posts(id) ON DELETE CASCADE,
    read_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, post_id)
);

CREATE INDEX idx_board_posts_board_time   ON board_posts(board_id, posted_at);
CREATE INDEX idx_board_posts_thread       ON board_posts(thread_root_msgid, posted_at);
CREATE INDEX idx_board_posts_reply_parent ON board_posts(reply_to_msgid);
CREATE INDEX idx_board_posts_network      ON board_posts(network_id);
```

`internal/store/migrations/0014_files.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE files (
    id          INTEGER PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    hash        TEXT NOT NULL,
    size        INTEGER NOT NULL,
    mime        TEXT NOT NULL,
    owner_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_files_hash  ON files(hash);
CREATE INDEX idx_files_owner ON files(owner_id);

CREATE TABLE file_acls (
    file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    account_id  INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    perms       INTEGER NOT NULL,   -- bitmask (read/write/delete)
    PRIMARY KEY (file_id, account_id)
);

CREATE TABLE file_tokens (
    value        TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('upload','download')),
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    file_id      INTEGER REFERENCES files(id) ON DELETE CASCADE,
    slug         TEXT,
    expires_at   INTEGER NOT NULL,
    used_at      INTEGER
);

CREATE INDEX idx_file_tokens_expires ON file_tokens(expires_at);
```

## Implementation tasks

1. Add migrations 0011 (`ftn_networks`), 0012 (mail), 0013 (boards), 0014 (files).
2. Add `[[ftn.network]]` blocks to config. At startup, upsert each into `ftn_networks`; if no `local` network is declared, auto-create it (domain `local`, addr `255:255/255.0`, full origaddr `255:255/255.0@local`) and mark it `default` if no other network is. Validate that exactly one row has `is_default = 1`.
3. Implement a small `internal/ftn/addr` package: parse 5D and domain-form addresses, render them, parse MSGID lines (handling the FTS-0009 quoted-origaddr form with embedded `""` escapes), define AttributeWord bit constants, and parse/serialize CHRS, PID, TZUTC kludge values.
4. Implement the MSGID issuer in `internal/store` (or a sibling): 8-hex-digit monotonic serialno per full origaddr, persisted in a `kv` row keyed by origaddr, mutated only by the single-writer goroutine. Exposed as a shared package used by both `internal/mail` and `internal/boards`.
5. Implement `internal/mail`:
    - Queries, command handlers, paste-mode compose.
    - On send: resolve network (`local` for mail between two local accounts; the recipient's network for outgoing netmail in a future milestone), stamp MSGID, fill `from_name`/`to_name`/`from_addr`/`to_addr`, set `attributes=0`, `charset="UTF-8 4"`, `pid="Wintermute/<version>"`, `tz_offset` from server clock.
    - On reply: set `reply_to_msgid` from the parent.
6. Implement `internal/boards`:
    - Queries, ACL checks, paste-mode compose, `bbcatchup`.
    - On insert: resolve `network_id` from the board, stamp MSGID for that network's origaddr, fill `author_name`/`area_tag`/`charset`/`pid`/`tz_offset`, generate `tearline` (`--- Wintermute/<version>`) and `origin_line` (`* Origin: <server.name> (<addr>)`), seed `seen_by`/`path` with our address.
    - Threading: if replying, set `reply_to_msgid` and resolve `thread_root_msgid` by looking up the parent's `thread_root_msgid` (parent unseen ⇒ treat the new post as a root and leave a TODO for the future FTN reconciliation pass).
    - `bbread <board>` lists threads grouped by `thread_root_msgid`, ordered by latest `posted_at` in the thread.
    - `bbthread <board> <id>` returns all posts sharing the post's `thread_root_msgid` ordered by `posted_at`.
    - Subject normalization (`Re:` stripping) for thread headings only; raw subject preserved per post.
7. Implement `internal/files`: blob store on disk, dedup, mime detection, ACL checks, token issuance.
8. Implement `internal/http` listener with the two routes.
9. Replace M5.7's terminal-handler stubs (`mail`, `bb`, `bbreply`, `bbthread`, `upload`, `download`, …) with real handlers that call into `internal/mail`, `internal/boards`, `internal/files`. (If M5.7 did not stub `bbreply`/`bbthread`, add them to the engagement command table.)
10. Implement the asynchronous "upload complete" notification: when the HTTP handler finalizes the file, it writes a `[terminal] Upload received…` line to the owner's session via the session writer; persistence for disconnected players uses the existing post-MOTD delivery (same path as unread-mail count).
11. Implement a periodic janitor goroutine: deletes expired tokens, deletes orphaned blobs (files with no `files` row referencing the hash), runs every N minutes (configurable).
12. Add `@cleanup-files` admin command that runs the janitor immediately.
13. Expose to admin Lua: `wintermute.ftn.network.list/get`, `wintermute.board.create/delete` (accepting `network` slug + `area_tag`), `wintermute.mail.broadcast`, `wintermute.file.list`. Adding/removing networks is a config-file operation, not an admin-Lua one — the schema lookups exist but no mutating Lua APIs are exposed in M6.
14. Show unread mail count after MOTD on login.
15. Unit tests:
    - `internal/ftn/addr`: round-trip parse/render of 5D and domain-form addresses including point and `@domain`; MSGID parse handles quoted origaddrs and embedded `""` escapes; AttributeWord constants round-trip.
    - Mail send/read/delete; mail reply links via `reply_to_msgid`; `from_addr`/`to_addr` populated from the `local` network.
    - Board post + catchup.
    - Board reply: child's `thread_root_msgid` matches parent's; multi-level replies all share the root's MSGID.
    - Board reply to an unknown MSGID: post is stored as a thread root (orphan handling).
    - MSGID issuer: 100 concurrent issuances per origaddr produce 100 distinct serialnos; serial counters are independent across origaddrs.
    - Boards on different networks with the same `area_tag` coexist (the per-network UNIQUE allows it).
    - File hash dedup (uploading same content twice yields one blob).
    - Token expiry and one-shot use.
16. Integration tests:
    - End-to-end mail between two sessions (each session engages a terminal to send and read), including a reply that links back to the original MSGID.
    - Multi-post threaded board conversation across three sessions; `bbthread` returns the full chain in order; every post carries `tearline`/`origin_line`/`seen_by`/`path` populated for the board's network.
    - HTTP upload via `httptest` server, then `download`.
    - Concurrent uploads/downloads (no token reuse, no truncated files).

## Testing

- `go test ./internal/mail/... ./internal/boards/... ./internal/files/... ./internal/http/...`
- Integration test using `net/http/httptest`.
- Manual: from a real telnet session, run `upload "notes"`, paste the printed `curl` command into another terminal, verify the upload-complete message arrives.

## Acceptance criteria

1. Two players can exchange mail; the recipient sees the unread count on next login. Replies link to the parent via FTS-0009 MSGID/REPLY. Every mail row has populated `from_name`/`to_name`/`from_addr`/`to_addr`/`attributes`/`charset` columns.
2. A board can be created via admin Lua with read/post ACLs; non-permitted players can't read or post. Every board belongs to exactly one network (default `local`); the same `area_tag` can exist on two different networks.
3. A multi-post board thread is navigable: `bbread <board>` lists threads with reply counts; `bbreply` continues an existing thread; `bbthread <board> <id>` returns the entire thread in posted order. Every post in the thread shares the same `thread_root_msgid` and has its `tearline`, `origin_line`, `seen_by`, and `path` columns populated from the board's network.
4. Every locally-originated post and message has a unique MSGID in `<origaddr> <serialno>` form, with `<origaddr>` being the full domain-form address of the row in `ftn_networks` attached to the board, or of the `local` network for mail between local accounts, and `<serialno>` an 8-hex-digit value never reused for that origaddr.
5. The MSGID parser correctly handles FTS-0009 quoted-origaddr lines, including the `""` embedded-quote escape.
6. With two `[[ftn.network]]` blocks configured (`fidonet` and `fsxnet`), posts to a fidonet board carry MSGIDs in the form `<addr>@fidonet <serial>` and posts to an fsxnet board carry MSGIDs in the form `<addr>@fsxnet <serial>`; the two networks' serial counters are independent.
7. An unrecognized `^a` kludge line on an ingressed message round-trips: it appears verbatim in `kludges` and is not lost. (Synthesised test, since FTN ingress isn't in M6 — the test feeds a hand-crafted message body through the parser/serializer.)
8. Uploading the same file twice (different slugs) creates two `files` rows but one blob on disk.
9. A download URL refuses a second use; an expired URL refuses the first use.
10. Uploads that finish after the player has disconnected are persisted and visible on next login.
11. The HTTP listener uses TLS in production (autocert) and a self-signed cert in dev.
12. All new files carry the MIT header.

## Risks & open questions

- **TLS for HTTP**: the in-world telnet TLS port and the file HTTP TLS port can share the same cert or use separate certs. Lean toward shared autocert for both, since both want a real DNS name.
- **Public URL construction**: the printed URL needs the server's externally reachable hostname. Make this a required config field (`server.public_host`). Refuse to start `internal/http` listener if unset and the file-transfer feature is enabled.
- **Upload size limits**: enforce a per-upload size cap (default 100 MB) at the HTTP layer; reject early via `Content-Length` then enforce with a `LimitReader`.
- **Slug collisions on upload**: the player names a slug at upload time. If it's taken, refuse and tell the player. Don't auto-rename.
- **Mime sniffing**: `http.DetectContentType` is fine for the common cases. Don't trust the client's `Content-Type` header.
- **Orphan-blob race**: a blob can be referenced briefly between hash computation and the `files` row insert. The janitor uses a grace period (don't delete blobs newer than 10 minutes) to avoid this.
- **Mail/board paste mode and the M5 `@edit` editor**: share the paste implementation via a small helper package (`internal/net/paste`) rather than reimplementing.
- **The `local` network is always present**: auto-created with domain `local` and a reserved placeholder address (e.g. `255:255/255.0`, so origaddr `255:255/255.0@local`). Local-only content (mail between accounts on this server, default-network boards) lives here using exactly the same code paths as gateable content; "no FTN" is just a network the gateway happens not to gate. An operator joining a real FTN adds the `[[ftn.network]]` block alongside `local`, optionally marks it default for new boards, and the existing `local` network is untouched. Once messages have been issued under an origaddr, changing that origaddr invalidates the FTS-0009 uniqueness contract for previously-issued serials — so don't rename `local` or change its `addr` once posts exist.
- **Cross-network gating**: deliberately out of scope for M6. A board belongs to exactly one network. If a future milestone wants to gate the same conversation across FidoNet and fsxNet, the gateway will create two boards (one per network) with their own MSGIDs and mirror messages between them — not a single board straddling two networks.
- **Domain-form vs bare 5D addresses**: always store and emit the full domain-form `zone:net/node.point@domain` for `origin_addr` / `from_addr` / `to_addr` / `our_addr`. Bare 5D is ambiguous once more than one network is configured. Accept both forms on parse; emit only the domain form.
- **Orphan replies on local boards**: in practice this only happens when the FTN echomail gateway lands and posts arrive out of order. For M6 (local-only), the orphan code path is technically reachable only via direct DB manipulation or admin Lua, but it must still be implemented correctly — leaving `thread_root_msgid` NULL is not acceptable. A TODO comment in `internal/boards` should mark the reconciliation pass as a FTN-milestone concern.
- **Surrogate ID vs MSGID in commands**: the `<id>` in `bbread <board> <id>`, `bbreply`, and `bbthread` is the local `board_posts.id` (an integer the player sees in `bbread <board>` listings) — never the MSGID, which is long and unfriendly to type. Internally, all linkage is via MSGID.
- **FTN kludge lines in message bodies**: deliberately out of scope for M6. Local-only posts have no kludges. The future FTN gateway is responsible for adding `^aMSGID`/`^aREPLY`/`^aPATH`/`^aSEEN-BY` lines on egress and stripping/parsing them on ingress; the body column stores the canonical message text in either direction.
