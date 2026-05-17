# Milestone 11 — End-to-end integration testing

## Goal

A comprehensive end-to-end integration suite that exercises the full server stack as a real telnet/HTTPS client would: login → engage → use feature → disengage. Each prior milestone's "integration test" wishlist accumulates here so the suite grows as the engine grows.

This milestone is **continuously updated**: when a milestone ships, its scenario-level test ideas get appended to the "Tracked scenarios" section below, even if the implementation slips to a later sweep. M11 is the place to land them and the index of what's been covered.

## Dependencies

- Whichever milestones the scenarios under test depend on. M11 itself depends on M1 (TCP/TLS), M2 (world), and the M5.7 engagement primitive at minimum.

## Scope

### Test framework

`internal/integration` already provides:

- `startEngageServer` — spins a full server on an ephemeral port with the fake LLM backend.
- `dialClient` / `loginNew` / `send` / `expect` / `unread` helpers for scripting a session.

M11 extends the framework with:

- `startFullServer` — same as `startEngageServer` but also wires the M6+ services (mail/boards/files), the HTTP listener on a second ephemeral port, and the M6.3/M6.4 menu handlers. Returns the HTTPS URL alongside the TCP address.
- `httpsClient(t, srv) *http.Client` — TLS client with `InsecureSkipVerify` and the server's self-signed root injected (once M10 ships, this uses the real provider).
- `mailFor(srv, username) []mail.Mail` — direct service-layer read for inbox assertions.
- `boardsOf(srv, username) []boards.Board` — same for boards.
- `filesIn(srv, area) []files.File` — same for files.

### Tracked scenarios

Each scenario is one test function in `internal/integration` (or under it). Sources from prior milestones:

**From M6 (mail/boards/files)**
- End-to-end mail between two sessions, each engaging a terminal to send and read; reply links via MSGID/REPLY.
- Multi-post threaded board conversation across three sessions; `bbthread` returns the full chain in order; every post carries `tearline`/`origin_line`/`seen_by`/`path`.
- HTTPS upload via the live listener, then `download`.
- Concurrent uploads/downloads (no token reuse, no truncated files).
- Two `[[ftn.network]]` blocks configured: posts to a fidonet board vs an fsxnet board stamp distinct MSGID origaddrs with independent serial counters.
- Unrecognised `^a` kludge round-trips through the `kludges` column without loss.

**From M6.1 (admin Lua APIs)**
- Lua script seeds three boards across two networks; `wintermute.board.list()` and the in-world `bb` listing match.
- `wintermute.mail.broadcast` inflates every active account's inbox by 1, all with `from_name="<system>"` and `from_id=NULL`.
- `wintermute.ftn.network.set_default("fidonet")` clears the previous default in one tx.

**From M6.2 (file admin + Dropbox + upload-mail)**
- Fresh-install scenario asserts the `dropbox` row exists and a default file area is browsable.
- Upload while owner is offline → owner gets a system mail with the upload details on next login.
- `@cleanup-files` reaps expired tokens and orphan blobs in one shot.

**From M6.3 (menu engagement)**
- Player engages a menu terminal, navigates Mail → Compose → send → back → disengage, all without raw commands.
- Per-object menu opt-in: a "boards-only" terminal exposes only the Boards entry.
- Unicode chrome on UTF-8 sessions; ASCII chrome on a session that negotiated a non-UTF-8 encoding.

**From M6.4 (admin menu)**
- Admin engages an admin-enabled terminal, performs one mutation in every subsection (Users / Mail / Boards / Files / Objects / Rooms / FTN / System), each lands in the DB and emits an audit slog line.
- Non-admin engaged at the same object never sees the Admin entry.
- Self-demotion is refused when the actor is the only admin.

**From M7 (autonomous NPC loop)** *(to be expanded when M7 lands)*
- Two-tier gate model: a "react=no" gate skips the response model entirely.
- Per-NPC token budget enforcement: after exhaustion, the NPC degrades to silence rather than calling the model.
- Tool dispatch round-trip: an NPC issues a tool call that mutates the world and the result is fed back into a final response.

**From M8 (player-tier scripting)** *(to be expanded when M8 lands)*
- Sandboxed script cannot read `mail` rows it doesn't own.
- Sandboxed script CPU budget exhaustion aborts cleanly without killing the host.

**From M9 (XYZModem libraries)** *(to be expanded when M9 lands)*
- ZModem upload from `lrzsz` over a real telnet session lands in the blob store.
- XModem download to `lrx` round-trips byte-for-byte.

**From M10 (TLS configuration)**
- `tls.mode="self-signed"` provides a working HTTPS handshake for the file-transfer client; upload+download succeed end-to-end over HTTPS.
- Telnet TLS port and HTTPS file-transfer port present the same cert.
- (Gated by build tag and external state: `tls.mode="autocert"` against the Let's Encrypt staging endpoint provisions and renews.)

### Process for keeping this list current

- Every milestone's "Implementation tasks" section ends with: "Append scenario-level test ideas to `docs/milestones/11-end-to-end-integration-testing.md` under *Tracked scenarios*."
- When a scenario is implemented and lands in `internal/integration`, the entry above is marked `[done]` with the test function name.
- A scenario that proves obsolete (e.g. the feature it tests was scoped out) gets struck through with a one-line note.

## Out of scope

- Unit tests next to code (`*_test.go` in each package) — those are owned by their feature's milestone, not M11.
- Performance/load tests. (Worth doing eventually behind a build tag; not blocking.)
- Chaos tests (network drops, disk full, OOM). Same as above.

## Implementation tasks

1. Extend `internal/integration` with the framework helpers listed under *Test framework*.
2. Land the M6 / M6.1 / M6.2 scenarios under *Tracked scenarios* as their owning milestones complete.
3. Add the M6.3 / M6.4 / M10 scenarios when each ships.
4. As M7 / M8 / M9 land, expand their scenario subsections and implement.
5. Wire `make test-integration` as a separate Make target that runs only the `internal/integration` suite (already implicit in `make test`, but a dedicated target makes selective re-runs easy).

## Acceptance criteria

For each milestone whose scenarios are tracked above:

1. Every scenario listed under that milestone has either a `[done]` test function or an explicit `[deferred — reason]` annotation.
2. Each `[done]` test runs as part of `make test` (or `make test-integration`) and passes on a clean CI run.
3. The suite's runtime stays under a sensible upper bound (target: 60 s total on a developer laptop without `-race`).

Milestone-level "M11 itself is done" is intentionally fuzzy: it's a living document. The closest hard line: by the time M10 ships, every milestone from M6 through M10 has its scenarios either implemented or explicitly deferred with a documented reason.

## Risks & open questions

- **Test flakiness**: scenarios that drive real network sockets can flake on busy CI. Use generous timeouts; prefer deterministic helpers (`expect("foo", N*time.Second)`) over fixed sleeps.
- **Fake-LLM coverage gaps**: scenarios that depend on the gate-model decision in M7 need scripted fake responses. Keep the fake backend's scripting API expressive enough for the M7 scenarios.
- **HTTPS in tests**: requires the M10 TLS provider's self-signed mode + a TLS-aware test client. Defer those tests until M10 lands; until then, the file-transfer scenarios continue to use plain HTTP via httptest as in M6.
- **Race detector**: integration runs are expensive under `-race`. Gate the full suite under `-race` to a nightly CI job, not every PR.
