# Architecture

This document describes how `google-chat-mcp` is put together: the
composition-root pattern that lets one codebase serve two transports,
the request-flow through tools and resources, and the deliberate design
decisions that contributors should know before touching auth, OAuth, or
message handling.

## Two transports, one composition root

Both transports share a single builder, `src/app.py::build_app`. Tool and
resource registration is transport-agnostic; each entry point supplies
the transport-specific auth wiring and then hands off.

- [`src/app.py`](../src/app.py) — `build_app(settings, *, resolver=None, auth=None) -> FastMCP`.
  Registers all 21 tools and 3 resources, wires the `ToolContext`
  lifespan. Unit-tested in `tests/test_app.py`.
- [`src/server.py`](../src/server.py) — HTTPS entry. Builds
  `GoogleProvider`, calls `build_app(settings, auth=provider)`, mounts
  `/healthz` / `/readyz` / `/metrics`, runs the HTTP transport.
  Unit-tested in `tests/test_server.py`; full-stack exercised in
  `tests/test_integration_https.py`.
- [`src/stdio.py`](../src/stdio.py) — stdio entry. argparse CLI
  (`login`, `logout`, default `serve`); loopback OAuth and local token
  store; calls `build_app(settings, resolver=<local-resolver>)`.
  Full-stack exercised in `tests/test_integration_stdio.py` via a real
  subprocess.

## Request flow

```
MCP client ──(HTTPS OR stdio)──► src/app.py::build_app
                                  │
                                  ├── @mcp.tool handlers in src/tools/
                                  │      └── each wraps invoke_tool() in tools/_common.py:
                                  │           rate-limit → resolver() → timed call → metrics + audit row
                                  │           (auth resolver: HTTPS = FastMCP dep; stdio = local closure)
                                  │
                                  ├── @mcp.resource handlers in src/resources/
                                  │      └── same chat_client backends as the get_* tools
                                  │
                                  ├── src/chat_client.py — single shared httpx.AsyncClient
                                  │      └── 10s timeout, exp-backoff retry on 5xx/429, Pydantic-validated JSON
                                  │
                                  ├── src/storage.py — SQLite (WAL): audit_log (user_sub HMAC-hashed by default) + user_directory (email cache)
                                  ├── src/rate_limit.py — in-memory token bucket, 60/min per user sub
                                  └── src/observability.py — structlog (stdout HTTPS / stderr stdio) + prometheus_client registry

HTTPS only:
  ├── fastmcp.GoogleProvider (OAuthProxy subclass; PKCE + state + MCP JWT issuance)
  │      └── client_storage = FernetEncryptionWrapper(DiskStore) — Fernet-encrypted refresh tokens on disk
  └── custom_route: /healthz, /readyz, /metrics

stdio only:
  └── ~/.config/google-chat-mcp/{tokens.json, fernet.key, audit_pepper}
         ├── tokens.json: Fernet-encrypted OAuth credentials (0600)
         ├── fernet.key: per-installation encryption key (0600)
         └── audit_pepper: HMAC-SHA256 key for audit_log user_sub hashing (0600)
```

## Design decisions

These are deliberate choices — changing any of them warrants a
PR-level discussion, not an incremental refactor.

### HTTPS OAuth delegates to `GoogleProvider`

`fastmcp.server.auth.providers.google.GoogleProvider` (an `OAuthProxy`
subclass) handles the full upstream Google OAuth dance and issues the
MCP-layer JWT. There is no hand-rolled PKCE, no custom
`/oauth/callback`, and no `users` table storing `mcp_bearer_hash`.
Refresh tokens are persisted by FastMCP via
`FernetEncryptionWrapper(DiskStore)`.

### Stdio OAuth delegates to `google-auth-oauthlib`

`src/stdio.py` uses
`google_auth_oauthlib.flow.InstalledAppFlow` for the loopback-desktop
flow (RFC 8252 §6 — PKCE, state, browser, token exchange). Refresh on
expired uses `google.oauth2.credentials.Credentials.refresh()`. The
trust model is "the user is the process owner"; tokens live `0600`
under `~/.config/google-chat-mcp/`. No `GoogleProvider` on this path,
and no hand-rolled crypto.

### No hardcoded client-specific redirects

`allowed_client_redirects` defaults to empty. Operators configure
`GCM_ALLOWED_CLIENT_REDIRECTS` with whichever MCP client's OAuth
callback URLs they intend to support. The server is intentionally
client-agnostic — no built-in Claude, Cursor, or other
vendor-specific defaults.

### No server-side message-body mutation

`send_message_handler` posts `payload.text` verbatim — no suffix,
prefix, or client-identity tag. What the caller passes is what
Google Chat receives.

### No centralized deployment

Each deployer (HTTPS operator or stdio user) owns their own Google
Cloud project, OAuth consent screen, client credentials, and rollout
cadence. No shared install, no upstream service operated by the
maintainers.

### Schema drift must be observable, not fatal

Chat-API response models (`_ChatBase`) set `extra="allow"`. An unknown
field is kept, reported once per process on the `schema_drift` log
event, and counted in `mcp_schema_drift_total{location}`.

This reverses an earlier rule that said drift must fail the call.
That rule cost two total outages: Google adds response fields without
notice — AIP-180 explicitly permits it — and every such field lands on
*every* row of its resource, so `forbid` turned a routine
backwards-compatible change into an every-tool failure, twice. The
detection it promised never materialised: both events were found by a
user, not by us.

What still fails loudly: `name`, `sender`, `create_time` and
`thread` have no defaults, so removing or retyping one of those raises.
**Fields that do have defaults — `text`, `displayName`, `member` — do
not.** If Google renames `text`, every message body comes back empty and
the call succeeds. The signal in that case is the unknown key arriving
under its new name: `schema_drift` fires, the counter moves, and
`doctor` reports it. That is an observation, not a failure, and it is
why `doctor` needs to actually run.

**Tool I/O keeps `extra="forbid"`** (`_Strict`). That side is our own
contract, and rejecting an unrecognised key from a calling model is a
real safety property — a misspelled `dry_run` must not silently post
for real. Do not relax it because the response tier was relaxed; the
two tiers face opposite directions.

When Google adds a field you want to *use*, declare it on the model as
usual. See `docs/runbook.md`.

### Stdout hygiene in stdio serve mode

When `src/stdio.py` runs `serve`, stdout is reserved for MCP JSON-RPC
frames. structlog writes to stderr in that mode. `print()` is fine in
`login` and `logout` (not MCP subcommands) but banned in `serve`.
Guarded by a real-subprocess test in `tests/test_integration_stdio.py`.

### Missing-scope errors are structured

Helpers in `src/tools/_common.py` wrap missing-scope 403s as a
`ToolError` naming the exact scope URL the user still needs to grant:

- `is_missing_scope_error(exc)` — detects the 403-shaped
  missing-scope case.
- `format_missing_scope_message(scope)` — renders the user-facing
  message naming the scope and the re-consent path (stdio vs. HTTPS).

See the README "Granular-consent errors" section for the user-facing
error shape.

## Security model

See [`docs/security.md`](security.md) for trust boundaries, assets,
adversary classes, the security-relevant invariants the code enforces,
and operator responsibilities. Read that before relaxing any of:

- the `_ID` regex
- the `chat_api_base` validator
- the `allowed_client_redirects` validator
- the Fernet / JWT key-length checks
- the `_redact_value` walker
