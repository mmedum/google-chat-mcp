# google-chat-mcp

A production-grade MCP server that exposes Google Chat to Claude custom connectors.
Read incoming messages and reply to individuals or spaces without leaving Claude.

- **Transport:** streamable HTTP (Claude custom-connector compatible)
- **Identity:** per-user OAuth 2.0 against Google Workspace (Internal user type)
- **Hosting:** self-hosted Docker on your own server

## Prerequisites

Before you start, you need:

- A **Google Workspace domain** you administer (Internal-type OAuth apps bypass Google's app verification but restrict auth to users on your domain)
- A **Google Cloud project** where you can enable APIs and create OAuth clients
- **Docker + Docker Compose** on a host you control
- A **public HTTPS hostname** pointing at the host (TLS terminated by your reverse proxy; port 8000 behind it)
- A **Claude account** that supports custom connectors

## Tools

| Tool | Purpose |
|---|---|
| `list_spaces` | List DMs, group chats, and named spaces the authenticated user belongs to |
| `find_direct_message` | Resolve a user email to a DM space ID (creates the DM if none exists) |
| `send_message` | Post a text message. Optional `thread_name` replies to an existing thread. Server appends `— Claude` |
| `get_messages` | Read recent messages, newest first. Sender email resolved via People API (cached 24h) |

## One-time GCP setup (~15 minutes)

The Workspace admin performs this once for the organization.

1. Create a Google Cloud project (e.g. `google-chat-mcp`).
2. Enable the **Google Chat API** and the **People API**.
3. Configure OAuth consent screen:
   - User type: **Internal**
   - App name: `Google Chat MCP`
   - Support email + developer contact: your admin address
   - Add these scopes:
     - `openid`, `email`, `profile`
     - `https://www.googleapis.com/auth/chat.messages`
     - `https://www.googleapis.com/auth/chat.messages.readonly`
     - `https://www.googleapis.com/auth/chat.spaces.readonly`
     - `https://www.googleapis.com/auth/chat.spaces.create`
     - `https://www.googleapis.com/auth/chat.memberships.readonly`
     - `https://www.googleapis.com/auth/directory.readonly`
4. Create an **OAuth 2.0 Client ID**:
   - Application type: **Web application**
   - Authorized redirect URI: `https://<your-mcp-host>/oauth/callback`
   - Save the Client ID and Client Secret.

Not required: Chat app publishing, service accounts, domain-wide delegation, Pub/Sub, Google app verification.

## Deploy

### 1. Prepare secrets

```bash
mkdir -p secrets
printf '%s' 'paste client id here'     > secrets/google_client_id
printf '%s' 'paste client secret here' > secrets/google_client_secret
python -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())' \
    > secrets/fernet_key
python -c 'import secrets; print(secrets.token_urlsafe(48))' \
    > secrets/jwt_signing_key
chmod 600 secrets/*
```

### 2. Set required env

```bash
export GCM_BASE_URL="https://chat-mcp.example.com"
```

Optional overrides:

| Var | Default | Purpose |
|---|---|---|
| `GCM_LOG_LEVEL` | `INFO` | `DEBUG`/`INFO`/`WARNING`/`ERROR` |
| `GCM_RATE_LIMIT_PER_MINUTE` | `60` | Per-user token-bucket capacity |
| `GCM_ALLOWED_CLIENT_REDIRECTS` | `https://claude.ai/api/mcp/auth_callback,https://claude.com/api/mcp/auth_callback` | Post-auth redirect whitelist (CSV). Both Claude domains are included by default — Claude migrated from `.ai` to `.com` during 2025–2026, so both are accepted. |

### 3. Run

```bash
docker compose up -d
docker compose logs -f mcp
```

TLS termination and a public hostname are out of scope for this project — put a reverse proxy (Caddy / nginx / traefik) in front of port 8000.

### 4. Connect Claude

In Claude, add a **custom connector**:

- Server URL: `https://chat-mcp.example.com/mcp`
- Authorization: OAuth (Claude will initiate the flow).

On first use, Claude redirects you to Google, you grant the scopes, and Claude stores the bearer token for subsequent tool calls.

## What you can ask Claude

Once the connector is attached, natural-language prompts compose the four tools:

- "What's new in the #eng space since this morning?" → `list_spaces` + `get_messages(since=...)`
- "Send 'Ship it' to alice@example.com on Chat." → `find_direct_message` + `send_message`
- "Reply to the last message in #launch with 'Done.'" → `get_messages` + `send_message(thread_name=...)`
- "Summarise my unread DMs from today." → `list_spaces` + `get_messages` per DM

Claude always asks before sending. The server appends `— Claude` to every outbound message so recipients know it wasn't typed by hand.

## Local development

```bash
uv sync --extra dev
cp .env.example .env      # or export GCM_* vars in your shell
uv run pytest
uv run ruff check .
uv run ty check
uv run python -m src.server
```

Pre-commit hooks install with `uv run pre-commit install`.

## Architecture at a glance

```
Claude  ──HTTP─► FastMCP app (port 8000)
                 ├── GoogleProvider  ◄─upstream OAuth─► Google (Chat, People APIs)
                 │      └── disk-backed KV store (Fernet-encrypted refresh tokens)
                 ├── Tools: list_spaces / find_direct_message / send_message / get_messages
                 │      └── shared httpx.AsyncClient (retry/backoff)
                 ├── SQLite (audit_log + user_directory email cache)
                 ├── Rate limiter (60/min per user, in-memory)
                 └── /healthz /readyz /metrics
```

FastMCP's `GoogleProvider` (an `OAuthProxy` subclass) handles PKCE, state, upstream token refresh, and issuing the MCP-layer JWT that Claude stores. Refresh tokens never leave the server unencrypted.

## Operations

See [`docs/runbook.md`](docs/runbook.md) for: onboarding, revoking a user, rotating the Fernet key, restoring from backup, reading metrics, and known failure modes.

## Out of scope for v1

Reactions, edits, deletes, card v2 formatting, membership management, space creation (beyond DM create), cross-space search, real-time push, app-as-bot identity, Postgres migration.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
