# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**google-chat-mcp** is a FastMCP 3.x HTTP server that exposes Google Chat as MCP tools for any MCP-compatible client (Claude custom connectors, Cursor, Continue, etc.). Self-hosted in Docker. Per-user OAuth against a Google Workspace Internal-type app; no app verification, no service account.

Six tools: `list_spaces`, `find_direct_message`, `send_message`, `get_messages`, `get_space`, `list_members`. Scope is deliberately small — see `README.md` "Out of scope" before proposing expansions. `list_spaces` accepts optional `limit` and `space_type`; `list_members` resolves humans to email via the shared People-API cache and returns Google Groups passthrough. `send_message` posts the body verbatim — no server-side suffix is appended.

## Commands

All commands assume `uv` is installed and the working directory is the repo root.

```bash
uv sync --extra dev                           # install runtime + dev deps, creates/updates uv.lock
uv run pytest                                 # full suite with coverage (80% gate)
uv run pytest --no-cov tests/test_chat_client.py::test_retries_on_5xx_then_succeeds  # single test
uv run ruff check .                           # lint
uv run ruff format .                          # format in place
uv run ty check                               # type check (strict mode off; ty is 0.0.x beta)
uv run python -m src.server                   # run server locally (requires GCM_* env set)
docker compose up -d                          # prod-style run; reads secrets from ./secrets/
```

Pre-commit hooks: `uv run pre-commit install`.

## Architecture

Composition root is `src/server.py`. Everything else is pure library.

```
MCP client ──HTTP──► src/server.py
                  │
                  ├── fastmcp.GoogleProvider  (OAuthProxy subclass)
                  │      ├── handles PKCE, state, token refresh, MCP-layer bearer issuance
                  │      └── client_storage = FernetEncryptionWrapper(DiskStore) — Fernet-encrypted refresh tokens on disk
                  │
                  ├── @mcp.tool handlers in src/tools/  (list_spaces, find_direct_message, send_message, get_messages, get_space, list_members)
                  │      └── each wraps invoke_tool() from tools/_common.py:
                  │           rate-limit → auth lookup (get_access_token) → timed call → metrics + audit row
                  │
                  ├── src/chat_client.py — single shared httpx.AsyncClient
                  │      └── 10s timeout, exp-backoff retry on 5xx/429, Pydantic-validated JSON
                  │
                  ├── src/storage.py — SQLite (WAL) for audit_log + user_directory (email cache from People API)
                  ├── src/rate_limit.py — in-memory token bucket, 60/min per user sub
                  ├── src/observability.py — structlog JSON stdout + prometheus_client registry
                  │
                  └── custom_route: /healthz, /readyz, /metrics
```

Key things NOT in the repo but often asked for:
- **No custom OAuth code.** `GoogleProvider` handles the full upstream dance and issues the MCP-layer JWT. Do not reintroduce a `users` table with `mcp_bearer_hash`, a custom `/oauth/callback`, or hand-rolled PKCE — `fastmcp.server.auth.providers.google.GoogleProvider` already does all of it.
- **No hardcoded client-specific redirects.** `allowed_client_redirects` defaults to empty; operators configure `GCM_ALLOWED_CLIENT_REDIRECTS` with their MCP client's OAuth callback(s). Don't reintroduce client-specific defaults (Claude, Cursor, etc.) — the server is intentionally client-agnostic.
- **No server-side message-body mutation.** `send_message_handler` posts `payload.text` verbatim — no suffix, no prefix, no client identity appended. Keep it that way.
- **Pydantic `extra="forbid"` on Chat-API response models** is intentional. Schema drift surfaces as validation errors rather than silent drops. The fix is to add the new optional field to `src/models.py`, not to relax to `extra="ignore"`. Runbook (`docs/runbook.md`) covers this.

## Tooling pins

- Python 3.14 (locked in `.python-version`; pyproject pins `>=3.14,<3.15`)
- FastMCP `~= 3.2` (current 3.2.4)
- `ty == 0.0.31` (pinned exactly — it's 0.0.x beta, every patch can have breaking changes; no strict mode)
- `ruff ~= 0.15`
- Pydantic v2: tool I/O models use `extra="forbid"` + `strict=True`; Chat API response models use `extra="forbid"` only so schema drift still surfaces

## Secrets

Never commit secrets. Production mounts Docker secrets at `/run/secrets/GCM_<name>`; local dev reads from `GCM_*` env vars. Missing secret → `Settings()` construction raises. Required (host file path / container path / env var name):

- `./secrets/google_client_id` → `/run/secrets/GCM_google_client_id` → `GCM_GOOGLE_CLIENT_ID`
- `./secrets/google_client_secret` → `/run/secrets/GCM_google_client_secret` → `GCM_GOOGLE_CLIENT_SECRET`
- `./secrets/fernet_key` → `/run/secrets/GCM_fernet_key` → `GCM_FERNET_KEY` (Fernet key for encrypting refresh tokens at rest)
- `./secrets/jwt_signing_key` → `/run/secrets/GCM_jwt_signing_key` → `GCM_JWT_SIGNING_KEY` (FastMCP JWT signing)

The `GCM_` prefix on the container mount is load-bearing: pydantic-settings applies `env_prefix` to `secrets_dir` lookups too, not just env vars. Keep `compose.yml`'s secret names in sync with that prefix.

## Tests

Pytest + pytest-asyncio + respx. `tests/conftest.py` provides:
- autouse `_env` fixture that seeds the `GCM_*` vars per-test (Settings always validates)
- `db`, `chat_client`, `tool_ctx` — fresh instances per test
- `mock_access_token` — patches `src.tools._common.get_access_token` to return a fake upstream token; use this in every test that touches a tool handler

`src/server.py` is excluded from coverage (composition root — it's wiring; needs integration tests, not unit tests). Add those in a follow-up PR once there's a harness.
