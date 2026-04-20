# google-chat-mcp

[![CI](https://github.com/mmedum/google-chat-mcp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/mmedum/google-chat-mcp/actions/workflows/ci.yml)
[![Release](https://github.com/mmedum/google-chat-mcp/actions/workflows/release.yml/badge.svg)](https://github.com/mmedum/google-chat-mcp/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/mmedum/google-chat-mcp?sort=semver)](https://github.com/mmedum/google-chat-mcp/releases/latest)
[![Container image](https://img.shields.io/badge/ghcr.io-mmedum%2Fgoogle--chat--mcp-2ea44f?logo=docker)](https://github.com/mmedum/google-chat-mcp/pkgs/container/google-chat-mcp)
[![License: Apache 2.0](https://img.shields.io/github/license/mmedum/google-chat-mcp)](./LICENSE)
[![Python](https://img.shields.io/badge/python-3.14-blue)](https://www.python.org/downloads/)

A production-grade MCP server that exposes Google Chat to any MCP-compatible
client. Read, search, and reply to spaces and DMs through natural-language
prompts in your MCP client of choice.

Two transports ship in this repo:

- **Stdio** (recommended for individual users) — install the CLI with
  `uv tool install google-chat-mcp` (or `pip install`), run a one-time
  OAuth login against your own Google account, then launch the server as a
  subprocess under Claude Code, opencode, Cursor, etc.
- **Streamable HTTP** (shared / hosted deployments) — self-host in Docker
  for teams; the MCP client connects over HTTPS and walks the OAuth flow
  per-user against the operator's Google app.

Per-user OAuth against Google Workspace or consumer Google accounts. No
service accounts, no domain-wide delegation, no publishing step.

## Tool surface

| Tool | Purpose | Required scope |
|---|---|---|
| `list_spaces` | List DMs, group chats, named spaces. Optional `limit` and `space_type` | `chat.spaces.readonly` |
| `find_direct_message` | Resolve an email to a DM space ID (creates one on miss) | `chat.spaces.readonly` + `chat.spaces.create` |
| `send_message` | Post a text message. Optional `thread_name` reply; `dry_run` previews the payload without posting | `chat.messages.create` |
| `get_messages` | Read recent messages, newest-first. Senders resolved via People API (24h cache) | `chat.messages.readonly` |
| `get_space` | Fetch one space by ID | `chat.spaces.readonly` |
| `list_members` | Humans + groups in a space; humans resolved to email via People API | `chat.memberships.readonly` + `directory.readonly` |
| `whoami` | Authenticated user's identity (sub, email, display name) via OIDC `/userinfo` | `openid email profile` |
| `get_thread` | All messages in one thread, oldest-first | `chat.messages.readonly` |
| `get_message` | One message by resource name, with reaction summaries inline | `chat.messages.readonly` |
| `add_reaction` | Add a Unicode-emoji reaction to a message (idempotent) | `chat.messages.reactions` |
| `remove_reaction` | Delete by resource name, or by `(message, emoji, user)` via server-side filter | `chat.messages.reactions` |
| `list_reactions` | Paginated reactions on a message | `chat.messages.readonly` |
| `search_messages` | Client-side exact / regex scan of one space; always pass `space_id` and `created_after` | `chat.messages.readonly` |

Three MCP **Resources** are dual-exposed for host-UI inclusion:

- `gchat://spaces/{space_id}` — same shape as `get_space`
- `gchat://spaces/{space_id}/messages/{message_id}` — same shape as `get_message`
- `gchat://spaces/{space_id}/threads/{thread_id}` — same shape as `get_thread`

### Dry-run on `send_message`

Set `dry_run: true` on any `send_message` call to preview the exact JSON body
the server would post. The tool returns `rendered_payload` and does NOT hit
the Chat API. Intended for ungated Agent-SDK loops and MCP clients running
with `bypassPermissions` — preview, inspect, then re-invoke without
`dry_run` to actually post. Rate-limit and audit still fire on the dry run.

### Granular-consent errors

Google's January 2026 granular-consent rollout lets users toggle individual
scopes at grant time. When a tool call fails because a scope is missing,
the server returns a structured `ToolError` naming the exact scope:

```
Missing required OAuth scope: https://www.googleapis.com/auth/chat.messages.reactions.
Re-run `google-chat-mcp login` (stdio) or re-consent in your MCP client (HTTPS).
```

---

## Stdio mode — individual users

### 1. One-time GCP setup (~15 minutes)

See [`docs/gcp-setup.md`](docs/gcp-setup.md) for the full walkthrough. Summary:

1. Create a Google Cloud project.
2. Enable the **Google Chat API**, **People API**, and **Google OIDC / userinfo**.
3. Configure the OAuth consent screen (upload your own app name, logo, contact email).
4. Add the [v2 scopes](#tool-surface) listed above.
5. Add yourself as a test user if the consent screen is in "Testing" state.
6. Create an **OAuth 2.0 Client ID** of type **Desktop app** and download `client_secret.json`.

### 2. Install and log in

```bash
uv tool install google-chat-mcp         # or `pip install google-chat-mcp`
google-chat-mcp login --client-secret ./client_secret.json
```

The login command:
- Prints the authorization URL to stdout (so it works on headless machines).
- Opens your system browser (or falls back to "paste this URL" if it can't).
- Receives the callback on `127.0.0.1:<random>`, exchanges the code (PKCE + state throughout).
- Stores tokens at `~/.config/google-chat-mcp/tokens.json` (0600, Fernet-encrypted).

Log out with `google-chat-mcp logout` — revokes the refresh token at
Google and deletes local files.

### 3. Wire into your MCP client

Point your client at the installed binary. Both names work:

```
mcp-server-google-chat        # primary, matches Anthropic's mcp-server-* convention
google-chat-mcp               # alias, for discoverability
```

Example Claude Code entry:

```json
{
  "mcpServers": {
    "google-chat": {
      "command": "mcp-server-google-chat"
    }
  }
}
```

See [`docs/gcp-setup.md`](docs/gcp-setup.md) for the full one-time flow.

---

## HTTPS mode — shared / hosted deployment

For teams who want a shared deployment (one Google app, many users). Requires
a public HTTPS hostname, Docker, and a reverse proxy in front of port 8000.

### 1. Required env

```bash
export GCM_BASE_URL="https://chat-mcp.example.com"
# CSV of OAuth-callback URLs your MCP client(s) use. One entry per client.
#   Claude: https://claude.ai/api/mcp/auth_callback,https://claude.com/api/mcp/auth_callback
#   Cursor: https://cursor.com/oauth/mcp/callback
export GCM_ALLOWED_CLIENT_REDIRECTS="https://your-client.example.com/oauth/callback"
```

### 2. Secrets

```bash
mkdir -p secrets
printf '%s' 'paste client id here'     > secrets/google_client_id
printf '%s' 'paste client secret here' > secrets/google_client_secret
python -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())' \
    > secrets/fernet_key
python -c 'import secrets; print(secrets.token_urlsafe(48))' \
    > secrets/jwt_signing_key
python -c 'import secrets; print(secrets.token_hex(32))' \
    > secrets/audit_pepper
chmod 600 secrets/*
```

Docker Compose picks these up as `/run/secrets/GCM_<name>` inside the container.

### 3. Run

```bash
docker compose up -d
docker compose logs -f mcp
```

Or pull a published image instead of building locally:

```bash
docker pull ghcr.io/mmedum/google-chat-mcp:latest
# or pin a version: ghcr.io/mmedum/google-chat-mcp:0.2.0
```

The image is multi-arch (`linux/amd64` + `linux/arm64`), published with SBOM
and SLSA provenance attestations on every `v*.*.*` tag.

### 4. Connect your MCP client

Add a custom connector at `https://chat-mcp.example.com/mcp`. The client
initiates OAuth; users grant scopes once, and the client stores the MCP
bearer token for subsequent tool calls.

### Transport-security notes (HTTPS)

FastMCP 3.x enforces these per the MCP spec (2025-06-18). You shouldn't
need to touch them, but don't disable them:

- **`Origin` header validation** on every request (DNS-rebinding defense).
- **Localhost-only bind for dev** (`127.0.0.1`). Use `0.0.0.0` only behind
  a real reverse proxy.
- **`MCP-Protocol-Version: 2025-06-18`** header required; server returns
  400 on invalid/missing version.
- Authentication (the FastMCP-issued JWT via `GoogleProvider`) is
  mandatory — never expose the HTTP endpoint unauthenticated.

### Local development with a tunnel (HTTPS mode)

MCP custom connectors call your `/mcp` endpoint from the public internet,
so pure `localhost` doesn't work end-to-end for HTTPS-mode dev. Front it
with Cloudflare Tunnel / ngrok / Tailscale Funnel.

```bash
# In one terminal
cloudflared tunnel --url http://localhost:8000
# Add the printed URL to your Google OAuth client's redirect list,
# then in another terminal:
export GCM_BASE_URL="https://<random>.trycloudflare.com"
uv run python -m src.server
```

Quick-tunnel URLs rotate on restart; use a named tunnel bound to a domain
you own for a stable hostname.

---

## Deployer invariants

- **No image rebuild.** Each deployer supplies their own Google app
  credentials at runtime (mounted secrets in HTTPS, `client_secret.json`
  in stdio). Pull the published image or package; configure; run.
- **No centralized deployment.** Each deployer owns their Google app,
  their tokens, and their rollout cadence. Compromises of a specific
  deployment's credentials or tokens are the deployer's responsibility —
  see [`docs/runbook.md`](docs/runbook.md) for rotation procedures.
- **No hardcoded client-specific logic.** `allowed_client_redirects`
  defaults to empty; operators configure it per their MCP client. The
  server is intentionally client-agnostic.

## Architecture

```
MCP client ──(stdio OR HTTP)──► FastMCP
                                 ├── Tools + Resources (see table above)
                                 ├── chat_client — shared httpx.AsyncClient with retry/backoff
                                 ├── SQLite (audit_log, user_directory cache)
                                 ├── Rate limiter (60/min per user)
                                 └── Auth resolver (transport-specific)
                                     ├── HTTPS: FastMCP.GoogleProvider (PKCE, state, JWT issuance)
                                     └── stdio: local Fernet-encrypted token store
```

`src/server.py` is the HTTPS entry (builds the `GoogleProvider`). `src/stdio.py`
is the stdio entry (loopback login + local token store). Both hand the shared
`build_app(settings, resolver=, auth=)` composition root identical tool and
resource registration. See [`CLAUDE.md`](CLAUDE.md) for more.

## Local development

```bash
uv sync --extra dev
uv run pytest                  # full suite, 80% coverage gate
uv run ruff check .
uv run ty check
uv run python -m src.server    # HTTPS (needs GCM_* env)
uv run python -m src.stdio login --client-secret ./client_secret.json
uv run mcp-server-google-chat  # stdio serve
```

Pre-commit hooks: `uv run pre-commit install` (includes `gitleaks`).

## Operations

[`docs/runbook.md`](docs/runbook.md): missing-scope errors, rotation
procedures for Fernet key / GCP client secret / refresh tokens, recovery
from common mis-states.

## Security

See [`SECURITY.md`](SECURITY.md) for how to report vulnerabilities.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
