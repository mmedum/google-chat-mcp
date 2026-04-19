# google-chat-mcp

A production-grade MCP server that exposes Google Chat to any MCP-compatible
client. Read incoming messages and reply to individuals or spaces through
natural-language prompts in your MCP client of choice.

- **Transport:** streamable HTTP (works with any MCP client that speaks the streamable-HTTP transport — Claude custom connectors, Cursor, Continue, etc.)
- **Identity:** per-user OAuth 2.0 against Google Workspace (Internal user type)
- **Hosting:** self-hosted Docker on your own server

## Prerequisites

Before you start, you need:

- A **Google Workspace domain** you administer (Internal-type OAuth apps bypass Google's app verification but restrict auth to users on your domain)
- A **Google Cloud project** where you can enable APIs and create OAuth clients
- **Docker + Docker Compose** on a host you control
- A **public HTTPS hostname** pointing at the host for production — TLS terminated by your reverse proxy; port 8000 behind it. For local development, a tunnel (Cloudflare Tunnel, ngrok, etc.) is enough (see [Local development with a tunnel](#local-development-with-a-tunnel) below).
- An **MCP client** that supports custom connectors over streamable HTTP with OAuth

## Tools

| Tool | Purpose |
|---|---|
| `list_spaces` | List DMs, group chats, and named spaces the authenticated user belongs to. `limit` (1-200, default 50) and `space_type` filter supported |
| `find_direct_message` | Resolve a user email to a DM space ID (creates the DM if none exists) |
| `send_message` | Post a text message. Optional `thread_name` replies to an existing thread |
| `get_messages` | Read recent messages, newest first. Sender email resolved via People API (cached 24h) |
| `get_space` | Fetch a single space by ID — display name, type, create time. Useful for resolving unnamed DM/group-chat counterparts |
| `list_members` | List members of a space. Humans resolved to email via People API; Google Groups passed through as `{kind: "GROUP", ...}` |

## One-time GCP setup (~15 minutes)

The Workspace admin performs this once for the organization.

1. Create a Google Cloud project (e.g. `google-chat-mcp`).
2. Enable the **Google Chat API** and the **People API**.
3. Configure OAuth consent screen:
   - User type: **Internal**
   - App name: `MCP for Google Chat`
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
# CSV of callback URLs your MCP client uses after OAuth. Examples:
#   Claude: https://claude.ai/api/mcp/auth_callback,https://claude.com/api/mcp/auth_callback
#   Cursor: https://cursor.com/oauth/mcp/callback
# Set whichever applies to the client(s) you'll attach to this server.
export GCM_ALLOWED_CLIENT_REDIRECTS="https://your-client.example.com/oauth/callback"
```

Optional overrides:

| Var | Default | Purpose |
|---|---|---|
| `GCM_LOG_LEVEL` | `INFO` | `DEBUG`/`INFO`/`WARNING`/`ERROR` |
| `GCM_RATE_LIMIT_PER_MINUTE` | `60` | Per-user token-bucket capacity |

### 3. Run

```bash
docker compose up -d
docker compose logs -f mcp
```

TLS termination and a public hostname are out of scope for this project — put a reverse proxy (Caddy / nginx / traefik) in front of port 8000.

### 4. Connect your MCP client

In your MCP client, add a **custom connector**:

- Server URL: `https://chat-mcp.example.com/mcp`
- Authorization: OAuth (the client will initiate the flow).

On first use, the client redirects you to Google, you grant the scopes, and the client stores the bearer token for subsequent tool calls.

## Example prompts

Once the connector is attached, natural-language prompts compose the tools:

- "What's new in the #eng space since this morning?" → `list_spaces` + `get_messages(since=...)`
- "Send 'Ship it' to alice@example.com on Chat." → `find_direct_message` + `send_message`
- "Reply to the last message in #launch with 'Done.'" → `get_messages` + `send_message(thread_name=...)`
- "Summarise my unread DMs from today." → `list_spaces` + `get_messages` per DM

Responsible clients ask before sending. Messages are posted verbatim — no client-identifying suffix is appended server-side.

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

## Local development with a tunnel

MCP custom connectors call your `/mcp` endpoint from the public internet, so a
pure `localhost` server won't work end-to-end. Front it with a tunnel for the
full development loop. Cloudflare Tunnel, Tailscale Funnel, and ngrok all work;
example below uses Cloudflare's quick tunnel (no account, no domain).

1. Install [cloudflared](https://github.com/cloudflare/cloudflared) (`yay -S cloudflared-bin` on Arch).
2. Start the tunnel in its own terminal: `cloudflared tunnel --url http://localhost:8000`. Copy the `https://<random>.trycloudflare.com` URL it prints.
3. In **Google Cloud Console** → your OAuth client → add
   `<tunnel-url>/oauth/callback` to *Authorized redirect URIs*.
4. Run the server with `GCM_BASE_URL` matching the tunnel:

    ```bash
    export GCM_BASE_URL="https://<random>.trycloudflare.com"
    uv run python -m src.server
    ```

5. In your MCP client, add a custom connector at `<tunnel-url>/mcp`.

**Quick-tunnel URLs rotate on every restart**, so step 3 repeats each
session. A named tunnel bound to a domain you own (Cloudflare, ngrok
reserved domain, Tailscale Funnel) gives you a stable hostname so Google
OAuth only needs to be configured once.

## Architecture at a glance

```
MCP client ──HTTP─► FastMCP app (port 8000)
                    ├── GoogleProvider  ◄─upstream OAuth─► Google (Chat, People APIs)
                    │      └── disk-backed KV store (Fernet-encrypted refresh tokens)
                    ├── Tools: list_spaces / find_direct_message / send_message / get_messages / get_space / list_members
                    │      └── shared httpx.AsyncClient (retry/backoff)
                    ├── SQLite (audit_log + user_directory email cache)
                    ├── Rate limiter (60/min per user, in-memory)
                    └── /healthz /readyz /metrics
```

FastMCP's `GoogleProvider` (an `OAuthProxy` subclass) handles PKCE, state, upstream token refresh, and issuing the MCP-layer JWT that the client stores. Refresh tokens never leave the server unencrypted.

## Operations

See [`docs/runbook.md`](docs/runbook.md) for: onboarding, revoking a user, rotating the Fernet key, restoring from backup, reading metrics, and known failure modes.

## Out of scope for v1

Reactions, edits, deletes, card v2 formatting, membership *management* (adding/removing members — read-only via `list_members`), space creation (beyond DM create), cross-space search, real-time push, app-as-bot identity, Postgres migration.

## License

Apache 2.0 — see [`LICENSE`](LICENSE).
