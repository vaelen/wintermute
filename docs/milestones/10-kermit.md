# Milestone 10 — Kermit

## Goal

Players whose clients prefer Kermit (relatively few; the protocol is niche but historically iconic) can transfer files via Kermit in-band. The engine wraps Columbia's `gkermit` (or any `kermit` binary) as a subprocess and pipes the player's byte stream through it. No native Go port of Kermit is undertaken.

## Dependencies

- M6 (mail/boards/files): the blob store and `upload`/`download` commands.
- M9 is **not** a dependency — Kermit's subprocess model is independent of the X/Y/ZModem libraries — but landing Kermit after M9 lets us reuse the `--bbs` flag plumbing and the BINARY-mode telnet helpers.

## Scope

- Detection of `gkermit` (or `kermit`) on the host at startup; feature-flag the `upload --bbs kermit` / `download --bbs kermit` paths accordingly.
- Subprocess plumbing: fork `gkermit`, attach its stdin/stdout to the player's session bytes (via the IAC-escape stream from M9), wait for completion, capture the resulting file, hand to the blob store.
- A clean abort path: SIGTERM the subprocess if the session disconnects or a wall-clock timeout fires.
- An admin `@kermit-info` command that reports whether the binary was detected and what version.

## Out of scope

- A native-Go Kermit implementation.
- Long-distance / sliding-window / packet-length tuning. `gkermit` is invoked with sensible defaults.
- Bulk/batch transfers. One file per invocation, mirroring how players actually use it.

## Architecture

### Detection

```go
// internal/files/bbs/kermit.go
type Kermit struct {
    Binary  string   // resolved path
    Version string
    Enabled bool
}

func DetectKermit(ctx) Kermit {
    for _, name := range []string{"gkermit", "kermit"} {
        path, err := exec.LookPath(name)
        if err == nil {
            ver, _ := versionOf(path)
            return Kermit{Binary: path, Version: ver, Enabled: true}
        }
    }
    return Kermit{Enabled: false}
}
```

Logged at startup. The feature is gated by `Enabled`.

### Transfer

```go
// internal/files/bbs/kermit_transfer.go
func (k Kermit) Receive(ctx, session *telnet.Session, blobStore *files.Store, ownerID int64, slug string) (*files.File, error)
func (k Kermit) Send(ctx, session *telnet.Session, blobStore *files.Store, fileID int64) error
```

`Receive` flow:

1. Verify `k.Enabled`.
2. Put the session into telnet BINARY mode (reusing M9's helpers).
3. Create a temp directory; the subprocess will write the file there.
4. `cmd := exec.CommandContext(ctx, k.Binary, "-r", "-l", "9", ...)` — `-r` receive, configured for a streaming transfer; details to tune in implementation.
5. Wire `cmd.Stdin = session.binaryReader()` (the player's incoming bytes) and `cmd.Stdout = session.binaryWriter()` (bytes back to the player).
6. Set a wall-clock context cap (configurable; default 10 minutes).
7. `cmd.Run()`; on success, locate the file in the temp directory and `blobStore.Put` it; create a `files` row.
8. Restore telnet line mode.

`Send` is symmetric (`-s <file>`).

Critically, the subprocess sees a clean byte stream — the IAC-escape filter from M9 handles telnet IAC bytes transparently.

### Errors

- If the subprocess exits non-zero, surface its stderr to the engine log (not the player; it's noisy and unhelpful).
- If the context is cancelled (session disconnect or timeout), send SIGTERM, wait briefly, SIGKILL.
- If no file is produced on a successful exit (rare; user-cancelled), treat as a clean abort.

### Admin command

```
@kermit-info
> Kermit binary: /usr/bin/gkermit
> Version:       G-Kermit 2.01
> Feature:       enabled
```

If not detected:

```
@kermit-info
> Kermit binary: not found
> Feature:       disabled
> Install:       sudo apt install gkermit  (or build from https://www.kermitproject.org/)
```

## Schema changes

None.

## Implementation tasks

1. Implement `DetectKermit` in `internal/files/bbs/kermit.go`. Call at startup and stash on the engine context.
2. Implement the subprocess plumbing with careful stdio piping.
3. Add `kermit` as a valid value for the `--bbs` flag on `upload`/`download`.
4. Implement `@kermit-info`.
5. Add a config knob `[files.kermit]` for `binary_override` (force a specific path) and `transfer_timeout`.
6. Unit tests:
    - `DetectKermit` table tests with a mock `exec.LookPath` (or by setting `PATH` in the test).
    - Argument construction for `Send`/`Receive`.
7. Integration test (skipped if `gkermit` not installed): round-trip a small file through a real `gkermit` subprocess piped via in-memory `net.Pipe()`.
8. Manual exercise: with `gkermit` installed and a Kermit-capable client (e.g. SyncTERM, C-Kermit), perform a transfer end to end.

## Testing

- `go test ./internal/files/bbs/...` (covers detection and arg construction).
- Tagged integration test (`-tags kermit`): only meaningful with `gkermit` present.
- Manual: full round-trip with a real client.

## Acceptance criteria

1. With `gkermit` installed, `upload --bbs kermit "<slug>"` initiates a Kermit transfer from inside a player session; the resulting file appears in the blob store with correct size and hash.
2. With `gkermit` *not* installed, `@kermit-info` reports disabled, and `upload --bbs kermit` errors with a helpful message.
3. SIGINT/disconnect during a Kermit transfer terminates the subprocess and does not leak processes (verify via test).
4. A wall-clock timeout (default 10 minutes) aborts a stuck transfer.
5. Engine binary remains a single static Go binary; Kermit dependency is detected at runtime, not linked.
6. All new files carry the MIT header.

## Risks & open questions

- **Binary availability across distros**: `gkermit` is in Debian/Ubuntu repos; macOS users likely install via `brew install c-kermit` (which provides the larger `kermit` binary, not `gkermit`). Try both names.
- **Process leaks**: a misbehaving subprocess on a misbehaving network connection is a real concern. The `exec.CommandContext` + `Cancel` pattern handles most cases; explicit SIGKILL after a grace period covers the rest.
- **Stdio buffering**: the Go `os.Pipe` between session and subprocess can buffer; ensure neither direction blocks the other under flow control. Use unbuffered, synchronous io.Copy goroutines.
- **Argument tuning**: `gkermit` has many flags. Pick defaults that match what BBS clients typically expect; expose advanced tuning via config rather than command flags.
- **Security**: the subprocess runs as the engine user. The file is written into a process-private temp directory and immediately ingested. Don't grant the subprocess access to the blob store directly — go through the engine.
- **Compared to ZModem**: ZModem's auto-start handshake means most BBS clients pick ZModem by default. Kermit will be a deliberate user choice via `--bbs kermit`. Set that expectation in documentation.
