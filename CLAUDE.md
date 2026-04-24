# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**google-chat-mcp** is a FastMCP 3.x server that exposes Google Chat as MCP tools and resources. Two transports ship:

- **HTTPS** (`src/server.py`) — self-hosted in Docker; FastMCP's `GoogleProvider` handles the MCP-layer JWT + upstream OAuth proxy; compose file + mounted secrets.
- **stdio** (`src/stdio.py`) — per-user CLI (`google-chat-mcp login / logout / serve`; `mcp-server-google-chat` primary alias per Anthropic convention); loopback OAuth on `127.0.0.1:<random>` + Fernet-encrypted local token store at `~/.config/google-chat-mcp/`.

Both entry points share `src/app.py::build_app(settings, resolver=, auth=)` — tool and resource registration is transport-agnostic. Per-user OAuth throughout; no service account, no domain-wide delegation, no centralized app (each deployer owns their Google app, their tokens, their rollout).

Twenty-one tools, three resources:

- Tools (read-side): `list_spaces`, `find_direct_message`, `get_messages`, `get_space`, `list_members`, `whoami`, `get_thread`, `get_message`, `list_reactions`, `search_messages` (space-scoped, client-side exact/regex), `search_people` (hybrid Workspace directory + caller contacts lookup; back-fills the email cache as a side effect).
- Tools (write-side): `send_message` (optional `dry_run: true` previews the payload without posting), `update_message` (text-only edit via `updateMask=text`; restricted-tier scope), `delete_message` (idempotent on 404 / non-scope 403; restricted-tier scope), `add_reaction`, `remove_reaction` (by resource name OR server-side-filtered `(message, emoji, user)`), `create_group_chat` (unnamed multi-person DM; 2-20 members; `dry_run`), `create_space` (named space; 1-20 members; `display_name` required; `dry_run`), `add_member` (invite by email; idempotent-by-nature on Google's side; `dry_run`), `remove_member` (delete by resource name; idempotent).
- Resources: `gchat://spaces/{id}`, `gchat://spaces/{id}/messages/{id}`, `gchat://spaces/{id}/threads/{id}` — same content shape as the matching `get_*` tools.

`send_message` posts the body verbatim — no server-side suffix is appended. Missing-scope 403s from Google are wrapped as a `ToolError` that names the exact scope URL (see `is_missing_scope_error` + `format_missing_scope_message` in `src/tools/_common.py`).

## Commands

All commands assume `uv` is installed and the working directory is the repo root.

```bash
uv sync --extra dev                           # install runtime + dev deps, creates/updates uv.lock
uv run pytest                                 # full suite with coverage (80% gate)
uv run pytest --no-cov tests/test_chat_client.py::test_retries_on_5xx_then_succeeds  # single test
uv run ruff check .                           # lint
uv run ruff format .                          # format in place
uv run ty check                               # type check (strict mode off; ty is 0.0.x beta)
uv run python -m src.server                   # HTTPS transport (requires GCM_* env set)
uv run google-chat-mcp login --client-secret ./client_secret.json  # stdio: one-time OAuth
uv run mcp-server-google-chat                 # stdio transport (serve as MCP subprocess)
docker compose up -d                          # HTTPS prod-style run; reads secrets from ./secrets/
```

Pre-commit hooks: `uv run pre-commit install`.

## Architecture

See [`docs/architecture.md`](docs/architecture.md) for the
composition-root pattern (`src/app.py::build_app`), the request-flow
diagram, the per-transport file layout, and the deliberate design
decisions contributors must not undo (no hand-rolled OAuth, no
server-side message-body mutation, no centralized deployment, etc.).
Read it before touching auth wiring, OAuth, or message handling.

Threat model and trust boundaries live in
[`docs/security.md`](docs/security.md). Operational procedures
(rotation, recovery) live in [`docs/runbook.md`](docs/runbook.md).

## Tooling pins

- Python 3.14 (locked in `.python-version`; pyproject pins `>=3.14,<3.15`)
- FastMCP `~= 3.2` (current 3.2.4)
- `ty == 0.0.31` (pinned exactly — it's 0.0.x beta, every patch can have breaking changes; no strict mode)
- `ruff ~= 0.15`
- Pydantic v2: tool I/O models use `extra="forbid"` + `strict=True`; Chat API response models use `extra="forbid"` only so schema drift still surfaces

## Secrets

Never commit secrets. Production mounts Docker secrets at `/run/secrets/GCM_<name>`; local dev reads from `GCM_*` env vars. Missing secret → `Settings()` construction raises. Secret fields are `pydantic.SecretStr`; read them via `.get_secret_value()`. Required (host file path / container path / env var name):

- `./secrets/google_client_id` → `/run/secrets/GCM_google_client_id` → `GCM_GOOGLE_CLIENT_ID`
- `./secrets/google_client_secret` → `/run/secrets/GCM_google_client_secret` → `GCM_GOOGLE_CLIENT_SECRET`
- `./secrets/fernet_key` → `/run/secrets/GCM_fernet_key` → `GCM_FERNET_KEY` (Fernet key for encrypting refresh tokens at rest)
- `./secrets/jwt_signing_key` → `/run/secrets/GCM_jwt_signing_key` → `GCM_JWT_SIGNING_KEY` (FastMCP JWT signing)
- `./secrets/audit_pepper` → `/run/secrets/GCM_audit_pepper` → `GCM_AUDIT_PEPPER` (HMAC-SHA256 key for hashing `user_sub` in audit_log; required when `GCM_AUDIT_HASH_USER_SUB` is true, the default)

The `GCM_` prefix on the container mount is load-bearing: pydantic-settings applies `env_prefix` to `secrets_dir` lookups too, not just env vars. Keep `compose.yml`'s secret names in sync with that prefix.

Set `GCM_AUDIT_HASH_USER_SUB=false` to disable hashing and store raw Google subs in `audit_log` (audit rows become joinable with other identity-keyed systems at the cost of leaking a stable user ID if the DB is exposed).

## Tests

Pytest + pytest-asyncio + respx. `tests/conftest.py` provides:
- autouse `_env` fixture that seeds the `GCM_*` vars per-test (Settings always validates)
- `db`, `chat_client`, `tool_ctx` — fresh instances per test
- `mock_access_token` — patches `src.tools._common.get_access_token` to return a fake upstream token; use this in every test that touches a tool handler

`src/app.py::build_app` is unit-tested in `tests/test_app.py` (tool registration, MCP annotations, server identity, resource templates). The two composition roots are covered via `tests/test_server.py` (direct unit tests for `build_auth` + `main`) and two integration harnesses:

- `tests/test_integration_https.py` — ASGI-in-process driver for `/healthz`, `/readyz`, `/metrics`, and one tool call through `fastmcp.Client`, wired with a stub `TokenVerifier`.
- `tests/test_integration_stdio.py` — spawns `python -m src.stdio serve` as a real subprocess under fastmcp's `StdioTransport`, with a stdlib `HTTPServer` stub for Chat API calls. `GCM_TEST_AUTH_STUB=1` on `cmd_serve` swaps the loopback-refresh resolver for a fixed stub so no real OAuth is needed. Any `print()` or misdirected structlog on stdout would break the JSON-RPC handshake before the test's first assertion — that's the stdout-hygiene regression guard.
