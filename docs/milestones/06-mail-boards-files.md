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
- `mail delete <id>` — delete from inbox.

A player's unread mail count is shown on login (`You have 3 new messages.`). The login-time notification is *not* part of an engagement — it's an out-of-band line written at the post-MOTD step.

### Boards

Commands (issued inside a terminal engagement; see M5.7):

- `bb` — list boards visible to the player (read ACL).
- `bbread <board> [<id>]` — list posts in board, or read a specific post.
- `bbpost <board> "<subject>"` — compose post (paste flow).
- `bbcatchup <board>` — mark all posts in board as read.

Per-board ACLs (read/post/admin) are wired through M5's permissions plumbing. Admin scripts create boards and set ACLs via `wintermute.board.create({slug=..., name=..., read=..., post=..., admin=...})`.

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

`internal/store/migrations/0011_mail.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE mail (
    id          INTEGER PRIMARY KEY,
    from_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    to_id       INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    subject     TEXT NOT NULL,
    body        TEXT NOT NULL,
    sent_at     INTEGER NOT NULL,
    read_at     INTEGER
);

CREATE INDEX idx_mail_to_unread ON mail(to_id, read_at);
```

`internal/store/migrations/0012_boards.sql`:

```sql
-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE boards (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    read_perms   INTEGER NOT NULL DEFAULT 0,
    post_perms   INTEGER NOT NULL DEFAULT 0,
    admin_perms  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE board_posts (
    id           INTEGER PRIMARY KEY,
    board_id     INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    author_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    subject      TEXT NOT NULL,
    body         TEXT NOT NULL,
    posted_at    INTEGER NOT NULL
);

CREATE TABLE board_reads (
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    post_id      INTEGER NOT NULL REFERENCES board_posts(id) ON DELETE CASCADE,
    read_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, post_id)
);

CREATE INDEX idx_board_posts_board_time ON board_posts(board_id, posted_at);
```

`internal/store/migrations/0013_files.sql`:

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

1. Add migrations 0011–0013 (mail/boards/files schema).
2. Implement `internal/mail`: queries, command handlers, paste-mode compose.
3. Implement `internal/boards`: queries, ACL checks, paste-mode compose, `bbcatchup`.
4. Implement `internal/files`: blob store on disk, dedup, mime detection, ACL checks, token issuance.
5. Implement `internal/http` listener with the two routes.
6. Replace M5.7's terminal-handler stubs (`mail`, `bb`, `upload`, `download`, …) with real handlers that call into `internal/mail`, `internal/boards`, `internal/files`.
7. Implement the asynchronous "upload complete" notification: when the HTTP handler finalizes the file, it writes a `[terminal] Upload received…` line to the owner's session via the session writer; persistence for disconnected players uses the existing post-MOTD delivery (same path as unread-mail count).
8. Implement a periodic janitor goroutine: deletes expired tokens, deletes orphaned blobs (files with no `files` row referencing the hash), runs every N minutes (configurable).
9. Add `@cleanup-files` admin command that runs the janitor immediately.
10. Expose `wintermute.board.create/delete`, `wintermute.mail.broadcast`, `wintermute.file.list` to admin Lua.
11. Show unread mail count after MOTD on login.
12. Unit tests:
    - Mail send/read/delete.
    - Board post + catchup.
    - File hash dedup (uploading same content twice yields one blob).
    - Token expiry and one-shot use.
13. Integration tests:
    - End-to-end mail between two sessions (each session engages a terminal to send and read).
    - HTTP upload via `httptest` server, then `download`.
    - Concurrent uploads/downloads (no token reuse, no truncated files).

## Testing

- `go test ./internal/mail/... ./internal/boards/... ./internal/files/... ./internal/http/...`
- Integration test using `net/http/httptest`.
- Manual: from a real telnet session, run `upload "notes"`, paste the printed `curl` command into another terminal, verify the upload-complete message arrives.

## Acceptance criteria

1. Two players can exchange mail; the recipient sees the unread count on next login.
2. A board can be created via admin Lua with read/post ACLs; non-permitted players can't read or post.
3. Uploading the same file twice (different slugs) creates two `files` rows but one blob on disk.
4. A download URL refuses a second use; an expired URL refuses the first use.
5. Uploads that finish after the player has disconnected are persisted and visible on next login.
6. The HTTP listener uses TLS in production (autocert) and a self-signed cert in dev.
7. All new files carry the MIT header.

## Risks & open questions

- **TLS for HTTP**: the in-world telnet TLS port and the file HTTP TLS port can share the same cert or use separate certs. Lean toward shared autocert for both, since both want a real DNS name.
- **Public URL construction**: the printed URL needs the server's externally reachable hostname. Make this a required config field (`server.public_host`). Refuse to start `internal/http` listener if unset and the file-transfer feature is enabled.
- **Upload size limits**: enforce a per-upload size cap (default 100 MB) at the HTTP layer; reject early via `Content-Length` then enforce with a `LimitReader`.
- **Slug collisions on upload**: the player names a slug at upload time. If it's taken, refuse and tell the player. Don't auto-rename.
- **Mime sniffing**: `http.DetectContentType` is fine for the common cases. Don't trust the client's `Content-Type` header.
- **Orphan-blob race**: a blob can be referenced briefly between hash computation and the `files` row insert. The janitor uses a grace period (don't delete blobs newer than 10 minutes) to avoid this.
- **Mail/board paste mode and the M5 `@edit` editor**: share the paste implementation via a small helper package (`internal/net/paste`) rather than reimplementing.
