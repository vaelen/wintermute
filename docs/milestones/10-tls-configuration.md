# Milestone 10 — TLS configuration

## Goal

Production-grade TLS for both the in-world TLS port (telnet-over-TLS, M1) and the HTTPS file-transfer port (M6). One certificate source, three supported modes — Let's Encrypt via ACME (`autocert`), persistent self-signed dev certificates, and operator-supplied files. The HTTP listener that M6 currently runs as plain HTTP picks up the same cert and becomes HTTPS by default in production.

## Dependencies

- M1 (TLS listener glue exists, `[tls]` config block parsed).
- M6 (HTTPS file-transfer listener in place — currently plain HTTP).

## Scope

### Unify TLS sourcing

`internal/net/tls` already has a partial `openTLS` for telnet TLS. M10 turns that into a small `tlsprovider` that:

- Owns a `*tls.Config` shared between the telnet TLS listener and the HTTP file-transfer server.
- Implements all three `tls.mode` values declared in the M1 config:
  - `"self-signed"` — generates a long-lived dev cert on first start, persists it under `~/.wintermute/dev-cert.pem` / `dev-key.pem` (or a configurable path), reuses on subsequent starts.
  - `"files"` — reads `tls.cert_path` and `tls.key_path` and watches mtime for hot reload (SIGHUP triggers a re-read; if the files change between checks, the next handshake uses the new cert).
  - `"autocert"` — wraps `golang.org/x/crypto/acme/autocert` against `tls.hostnames`. Cache dir comes from `tls.cache_dir`; the ACME challenge listener binds to `:80` (configurable). Renewal is automatic.

### Listener wiring

- The telnet TLS listener wraps `net.Listen` with `tls.NewListener(ln, provider.Config())` (M1 already does this; M10 just plumbs the shared provider).
- The HTTP file-transfer server becomes `&http.Server{ TLSConfig: provider.Config(), … }` invoked via `srv.ServeTLS(ln, "", "")`. For `autocert` mode the listener is the autocert's HTTP-01 challenge listener on `:80` plus the file-transfer TLS listener on `cfg.Server.HTTPPort`.
- A `cfg.Server.HTTPPlain` boolean (default `false`, only respected in dev) keeps the M6-current plain-HTTP behaviour for laptops without certs.

### Operator UX

- `tls.mode = "self-signed"` is the default and works zero-config.
- `make run` continues to "just work" because self-signed generation is in-process and durable.
- `tls.mode = "autocert"` plus `tls.hostnames = ["mud.example.com"]` is enough for production; the operator opens ports 80 and `cfg.Server.HTTPPort` (and the telnet TLS port), and the engine handles the rest.

## Out of scope

- Client certificate authentication.
- Per-listener distinct certs (operators wanting fully-separate certs can run two engines behind a reverse proxy).
- HSTS / HPKP headers on the HTTPS endpoints — the file-transfer handler stays minimal; an operator can front it with a reverse proxy for header policy.
- TLS for the telnet port (the "raw" telnet listener stays plaintext for low-tech clients; encryption requires the TLS port).

## Implementation tasks

1. Extract a `internal/net/tls.Provider` interface with the three modes; today's `openTLS` becomes its `self-signed` implementation.
2. Add the autocert mode (`acme/autocert.Manager`) with cache-dir handling.
3. Add the files mode with SIGHUP-triggered reload.
4. Refactor `cmd/wintermute/main.go` so both the telnet TLS listener and the HTTP file-transfer server consume the shared provider.
5. Add a `tls_cert_info` admin Lua function: returns `{mode, not_after, hostnames}` so operators can sanity-check from inside the game.
6. Update `wintermute.example.toml` with all three modes' annotated examples.
7. Unit tests for the self-signed provider (round-trip a generated cert through `tls.Dial`).
8. Integration test that uses `tls.mode = "self-signed"` and a `tls.Config{InsecureSkipVerify:true}` client to round-trip an upload+download over HTTPS.

## Acceptance criteria

1. `tls.mode = "self-signed"` works zero-config; the generated cert persists across restarts and the engine warns once at startup if the cert is within 30 days of expiry.
2. `tls.mode = "files"` reloads on SIGHUP without dropping in-flight TLS sessions.
3. `tls.mode = "autocert"` with valid hostnames provisions a real certificate end-to-end in a staging ACME test (or against the Let's Encrypt staging endpoint behind a build tag).
4. The HTTPS file-transfer listener and the telnet TLS listener present the same certificate.
5. `make lint check-headers test` is green; the integration test asserts that an upload+download survives over `https://` with the generated cert.

## Risks & open questions

- **ACME rate limits**: the autocert mode must use the production endpoint by default but make the staging endpoint available behind a config flag for testing. Document this clearly so operators don't burn through their weekly issuance quota during setup.
- **`:80` for HTTP-01 challenges**: requires the engine to bind a privileged port or be reverse-proxied. The doc must call this out; the alternative (DNS-01) needs DNS-provider plumbing we're not ready to take on.
- **Dev cert browser warnings**: self-signed mode triggers browser warnings on the HTTPS file-transfer endpoint. Acceptable for dev; the operator can install the cert in their browser if they want clean UX.
- **Restart on cert change**: hot reload requires `tls.GetCertificate` indirection — every accepted handshake looks up the current cert. Make sure long-lived TLS sessions don't pin the *old* cert past its renewal (autocert handles this; files mode needs `GetCertificate` to consult the latest read).
