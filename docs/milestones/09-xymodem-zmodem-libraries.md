# Milestone 09 — X/Y/ZModem spinoff libraries

## Goal

Three standalone, MIT-licensed Go libraries — `go-xmodem`, `go-ymodem`, `go-zmodem` — that implement their respective protocols against arbitrary `io.Reader` / `io.Writer` streams. The engine depends on them to deliver in-band file transfer over the player's existing telnet/TLS session. The libraries are useful outside Wintermute and are released as separate open-source projects.

## Dependencies

- M6 (mail/boards/files): the engine blob store and the `upload`/`download` in-world commands are the integration point.

## Scope

### Three new repositories

```
github.com/vaelen/go-xmodem
github.com/vaelen/go-ymodem
github.com/vaelen/go-zmodem
```

Each library has:

- Public Go module under its own import path.
- Sender and receiver APIs against `io.Reader`/`io.Writer`.
- A CLI demo binary (`cmd/xmodem`, `cmd/ymodem`, `cmd/zmodem`) for manual testing against `lrzsz`.
- Wire-compatibility test suite against `lrzsz` (Linux/macOS only, behind a build tag).
- `LICENSE` file (MIT, © Andrew C. Young).
- Source-file headers identical to the engine's convention.
- `README.md` with a usage example and a protocol reference link.
- CI: `go test`, lint, build the demo binary.

### Engine integration

- A new `internal/files/bbs` package that wraps the three libraries and bridges them to the M6 blob store.
- `upload`/`download` in-world commands grow a `--bbs xmodem|ymodem|zmodem` flag.
- ZModem auto-start sequence (`rz\r`) detection: when a download is initiated, the engine sends ZModem auto-detect bytes; if the player's client picks them up, the transfer proceeds via ZModem. Otherwise the HTTPS path stays available (M6).

### Order of implementation (within the milestone)

1. **XModem first** (smallest, simplest; CRC-16 and 1K variants). ~300 LoC.
2. **YModem next** (batch transfer; builds on XModem-1K framing). ~500 LoC.
3. **ZModem last** (crash recovery, streaming windows, header types). ~800–1200 LoC.

Each library can be tagged/released independently when complete.

## Out of scope

- Kermit. (Dropped from the roadmap.)
- Compression and 32-bit CRC enhancements to ZModem (defer; the standard subset is enough for compatibility with `lrzsz`).
- A pluggable framing layer shared across all three libraries. The protocols differ enough that shared abstractions cost more than they save.

## Architecture

### `go-xmodem`

```go
package xmodem

type Variant int
const (
    Checksum Variant = iota
    CRC16
    OneK
)

type Receiver struct { /* state */ }

// Sender writes a file via XModem to w, reading bytes from src.
func Send(ctx context.Context, w io.Writer, r io.Reader, src io.Reader, opts SendOpts) error

// Receive reads an XModem transfer from r/w into dst.
func Receive(ctx context.Context, w io.Writer, r io.Reader, dst io.Writer, opts ReceiveOpts) error

type SendOpts struct {
    Variant       Variant
    BlockTimeout  time.Duration  // typically 10s
    MaxRetries    int            // typically 10
}
type ReceiveOpts SendOpts
```

- Blocks of 128 bytes (Checksum, CRC16) or 1024 bytes (OneK).
- NAK/CRC negotiation: receiver opens with `C` (CRC) or `NAK` (Checksum); sender complies.
- `r` and `w` are usually the same connection — XModem is half-duplex over one stream.
- The `src`/`dst` are the file's bytes — separate to keep the protocol library agnostic of where data comes from/goes to.

### `go-ymodem`

```go
package ymodem

type FileMeta struct {
    Name string
    Size int64
    ModTime time.Time
}

// SendBatch sends multiple files. The callback yields each file's bytes when requested.
func SendBatch(ctx, w, r io.ReadWriter, files func(idx int) (FileMeta, io.Reader, error), opts SendOpts) error

// ReceiveBatch receives a batch. The callback yields a writer for each file.
func ReceiveBatch(ctx, w, r io.ReadWriter, dst func(meta FileMeta) (io.Writer, error), opts ReceiveOpts) error
```

- Builds on XModem-1K framing internally; vendored or imported from `go-xmodem`'s internal packages.
- "Block 0" carries filename, size, mtime.

### `go-zmodem`

```go
package zmodem

// SendFiles streams one or more files using ZModem.
func SendFiles(ctx, conn io.ReadWriter, files []File, opts SendOpts) error

// ReceiveFiles receives a ZModem session.
func ReceiveFiles(ctx, conn io.ReadWriter, dst Sink, opts ReceiveOpts) error

type File struct {
    Meta FileMeta
    Data io.Reader
}

type Sink interface {
    Create(meta FileMeta) (io.Writer, error)
    Finish(meta FileMeta) error
}
```

- Header types: ZRQINIT, ZRINIT, ZFILE, ZDATA, ZEOF, ZFIN, ZCAN, etc.
- Two encodings: hex header and binary header (binary preferred once both ends agree).
- Streaming windows: sender can keep sending; receiver acks periodically.
- Crash recovery (`ZSKIP`, `ZCRC` for partial-file resume) — implement, but mark as "best effort" in the README.

### Engine integration package

```go
// internal/files/bbs/bbs.go (in the engine repo)
type Mode string
const (
    ModeXModem Mode = "xmodem"
    ModeYModem Mode = "ymodem"
    ModeZModem Mode = "zmodem"
    ModeAuto   Mode = "auto"   // detect ZModem; fall back to HTTPS message
)

func Upload(ctx, session *telnet.Session, mode Mode, ownerID int64) (*files.File, error)
func Download(ctx, session *telnet.Session, mode Mode, fileID int64) error
```

- The session must be put into "binary mode" before the transfer: send IAC WILL/DO BINARY (RFC 856). After the transfer, restore line mode.
- The blob store is the source/sink: ZModem `Sink.Create` returns an `io.Writer` that the blob store's `Put` is reading via a pipe.

## Schema changes

None in the engine repo. The libraries have no schema.

## Implementation tasks (engine repo)

1. Add a placeholder `internal/files/bbs` package with the public interface and stubs.
2. Wire the `--bbs` flag into the `upload`/`download` commands.
3. Implement telnet BINARY-mode toggling helpers in `internal/net/telnet`.
4. Add ZModem auto-detect: at the start of a download, send the ZModem trigger sequence; race a small read window against the player's response.
5. Once `go-zmodem` is feature-complete enough, replace the stub with real wiring.

## Implementation tasks (per library)

For each of `go-xmodem`, `go-ymodem`, `go-zmodem`:

1. `git init` a new repo. Add `LICENSE`, `README.md`, `go.mod`, `Makefile`.
2. Implement the protocol against the spec:
    - XModem: XMODEM.TXT (Forsberg).
    - YModem: YMODEM.TXT (Forsberg).
    - ZModem: zmodem.doc (Forsberg, 1988).
3. Build the demo CLI under `cmd/<protocol>`.
4. In-tree unit tests: framing, CRC, retry logic.
5. `lrzsz` interop tests under build tag `lrzsz`: pipe stdin/stdout through `sx`/`rx`/`sb`/`rb`/`sz`/`rz` subprocesses.
6. GitHub Actions: `go test`, `golangci-lint`, `check-headers`, and (on Linux) `lrzsz` interop with `apt install lrzsz` step.
7. Tag `v0.1.0` and publish.

## Testing

- Per library: `go test ./...` plus `go test -tags lrzsz ./...`.
- Engine integration: a manual test plan using `lrzsz`'s `rz`/`sz` commands from inside Mudlet or a terminal-multiplexed telnet client.
- Fuzz: short fuzzing pass on each library's frame parser.

## Acceptance criteria

1. Each library compiles standalone with `go build ./...`.
2. Each library's CLI demo can send a file to and receive a file from `lrzsz`'s peer (`sx`/`rx`, `sb`/`rb`, `sz`/`rz`).
3. The engine's `upload --bbs zmodem` and `download --bbs zmodem` commands work from a session in a real ZModem-capable client.
4. ZModem auto-detect chooses ZModem when the client supports it and otherwise prints a fallback message pointing at the HTTPS path from M6.
5. All three libraries have a tagged release (`v0.1.0`).
6. All new files (in both engine and library repos) carry the MIT header.

## Risks & open questions

- **Telnet BINARY-mode subtleties**: IAC sequences must still be escaped in BINARY mode. The XModem/YModem/ZModem framing has no notion of IAC. Solution: the engine wraps the raw stream with an "IAC-escape" stream that doubles IAC bytes on write and unescapes on read. This is engine-side glue; the libraries themselves remain protocol-pure.
- **TLS + binary mode**: TLS is transparent to the bytes, so this works without modification. Verify in a manual test.
- **Client compatibility**: not all telnet clients can do ZModem. Mudlet, SyncTERM, NetRunner, and various BBS clients can. Document a "tested clients" list in the README.
- **Crash recovery completeness**: ZModem's resume protocol is complex. Initial implementation can omit resume support; document the limitation.
- **License attribution**: although the libraries are MIT, the spec documents are not. We re-implement against the public spec — no copying.
- **Repo path stability**: once published, the module path is forever. Confirm `github.com/vaelen/*` is the intended canonical path before tagging.
- **Versioning across the three libraries**: keep them independent — each has its own semver. The engine pins specific versions of each.
