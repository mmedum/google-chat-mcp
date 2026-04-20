# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-04-20

Ground-up v2 rewrite. Two transports (HTTPS + stdio), 13 tools + 3 resources,
per-user OAuth end-to-end. First public release with a published Docker image.

### Added
- **Stdio transport.** `mcp-server-google-chat` + `google-chat-mcp
  login/logout/serve` via `google-auth-oauthlib.InstalledAppFlow`.
  Fernet-encrypted tokens at `~/.config/google-chat-mcp/`; 0600 files inside
  a 0700 parent.
- **HTTPS transport.** FastMCP's `GoogleProvider` handles upstream OAuth and
  issues the MCP-layer JWT; refresh tokens Fernet-encrypted at rest.
- **Shared app builder** `src/app.py::build_app` — transport-agnostic. Tools
  and resources register once; `src/server.py` and `src/stdio.py` are thin
  composition roots.
- **Thirteen tools:** `whoami`, `list_spaces` (with `space_type` filter),
  `find_direct_message`, `get_space`, `list_members`, `get_messages`,
  `get_thread`, `get_message`, `send_message` (with `dry_run`),
  `add_reaction`, `remove_reaction` (by name or by
  `(message_name, emoji, user_email)`), `list_reactions`, `search_messages`
  (exact substring and regex, mutually exclusive).
- **Three resources:** `gchat://spaces/{id}`,
  `gchat://spaces/{id}/messages/{id}`, `gchat://spaces/{id}/threads/{id}`.
- **Typed missing-scope errors** — Google's insufficient-scope 403s wrapped
  as a `ToolError` naming the exact scope URL so MCP clients can drive a
  re-auth prompt.
- **SQLite audit log** with configurable retention; periodic prune.
- **Prometheus metrics** on the HTTPS transport: tool invocations, upstream
  API calls, active users, rate-limit hits.
- **GHCR Docker image** published on tag: `ghcr.io/mmedum/google-chat-mcp`,
  multi-arch (`linux/amd64`, `linux/arm64`), SBOM + provenance attestations.
- **Documentation:** `docs/gcp-setup.md` (one-time GCP walkthrough),
  `docs/runbook.md` (operator procedures), `SECURITY.md`.

### Changed (breaking for deployers)
- **OAuth scopes narrowed.** Drop the umbrella `chat.messages`; add
  `chat.messages.create` + `chat.messages.reactions`. Every deployer must
  re-consent.
- **New required secret `GCM_AUDIT_PEPPER`** (HTTPS mode) — HMAC key for
  `audit_log.user_sub` hashing. Set `GCM_AUDIT_HASH_USER_SUB=false` to
  opt out and store raw Google subs.
- **`GCM_ALLOWED_CLIENT_REDIRECTS` defaults to empty.** Operators configure
  their MCP client's OAuth callback explicitly; no client-specific defaults.
- **Composition root moved** from `src/server.py` to
  `src/app.py::build_app`. Downstream importers must update.
- **`send_message` posts body verbatim.** No server-side suffix or identity
  injection.

### Security
- Secret fields in `Settings` are `pydantic.SecretStr`; accidental
  `log.info(settings=...)` or `model_dump()` masks them.
- Observability redaction widened: `id_token`, `state`, `code`, `email`,
  `user_sub`, `sub`, cookies, JWT signing + Fernet + audit pepper keys.
- Audit-log `user_sub` HMAC-SHA256-hashed with a per-deployment pepper
  (default on HTTPS).
- Stdio config dir + all files tightened: 0700 parent, 0600 secrets,
  0700 audit-DB subdir.
- `.github/workflows/ci.yml` gates on `gitleaks`, `hadolint`, `trivy`,
  `pip-audit`, ruff, ty, pytest with an 80 % coverage floor.

### Fixed (surfaced during live testing)
- Stdio stdout hygiene — structlog now routes through the configured
  stream; previously it wrote to stdout and corrupted JSON-RPC frames on
  any error-path log line.
- `remove_reaction` filter path — Chat API 500s on
  `user.name = "users/{email}"`; handler now server-filters on emoji and
  resolves each reactor via People API (concurrent via `asyncio.gather`,
  deduped through `DirectoryCache`).
- `add_reaction` — Chat API returns 409 on duplicate
  `(emoji, user, message)` rather than a silent no-op; handler catches the
  409 and looks up the existing reaction to present the documented
  idempotent contract.
- `find_direct_message(user_email)` — gained `EmailStr` at the MCP boundary
  so invalid inputs fail fast rather than bubbling to Google as 400.
- `OAUTHLIB_RELAX_TOKEN_SCOPE=1` — now set in both `cmd_login` and
  `cmd_serve` via a shared helper; `Credentials.refresh()` no longer emits
  strict-check warnings every ~55 minutes.
- Migrations now ship inside the wheel (`src/migrations/`); fresh installs
  no longer crash on first `serve`.

## [0.1.0]

Initial scaffold; not intended for deployment.

[Unreleased]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/mmedum/google-chat-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/mmedum/google-chat-mcp/releases/tag/v0.1.0
