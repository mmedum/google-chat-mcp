# Runbook

Operational procedures for the google-chat-mcp server. Keep this file short and
scannable; every procedure assumes you are on the host running `docker compose`.

## Onboarding the first user

1. Deploy the server with GCP project + secrets in place (see [README](../README.md)).
2. In Claude, add a custom connector pointing at `https://<your-host>/mcp`.
3. Claude opens the OAuth consent screen — because the OAuth app is **Internal**,
   only users on your Workspace domain can proceed.
4. After grant, Claude stores the MCP bearer and can call the four tools.

There is no admin-side user registration. Every Workspace user self-onboards
through the OAuth flow on their first tool call.

## Revoking a user

### Option A — user-initiated (recommended)

The user visits `https://myaccount.google.com/permissions`, finds the "Google Chat
MCP" app, and removes it. Google invalidates the refresh token immediately.

### Option B — admin-forced

From the host, wipe the user's entry from the OAuth proxy's key-value store:

```bash
docker compose exec mcp sh -c 'ls /var/lib/google-chat-mcp/oauth_store'
# identify the user's JWT jti or OAuth client record
docker compose exec mcp rm -f /var/lib/google-chat-mcp/oauth_store/<file>
docker compose restart mcp
```

Audit-log their activity before you delete anything:

```bash
docker compose exec mcp sqlite3 /var/lib/google-chat-mcp/app.sqlite \
  "SELECT timestamp, tool_name, target_space_id, success FROM audit_log \
   WHERE user_sub = '<sub>' ORDER BY timestamp DESC LIMIT 100;"
```

## Rotating the Fernet key

Stored refresh tokens are encrypted at rest with Fernet. Rotate quarterly or
on suspected compromise.

1. Generate the new key: `python -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())'`.
2. Stop the container: `docker compose stop mcp`.
3. Write a one-shot re-encryption script that iterates every file in
   `/var/lib/google-chat-mcp/oauth_store`, decrypts with the old key, re-encrypts
   with the new key. py-key-value's `FernetEncryptionWrapper` stores values as
   single tokens; decrypt/re-encrypt loop is ~20 lines.
4. Replace `secrets/fernet_key` with the new key.
5. `docker compose up -d mcp`.

If you skip step 3, existing users will see their sessions invalidated and have
to reconnect — acceptable for a small deployment; document which.

## Restoring from backup

The app writes SQLite + KV store to the `mcp_data` named volume.
`/var/backups/chat-mcp/` should hold a daily copy.

```bash
docker compose stop mcp
docker run --rm -v mcp_data:/v -v /var/backups/chat-mcp:/b alpine \
  sh -c 'rm -rf /v/* && cp -a /b/<date>/* /v/'
docker compose up -d mcp
```

Audit-log and directory-cache are in `app.sqlite`; OAuth state is in `oauth_store/`.
Both must be restored together (they reference each other by user sub).

## Known failure modes

### Pydantic `extra="forbid"` validation errors on Chat API responses

We intentionally set `extra="forbid"` on Chat-API response models so that Google
silently adding fields surfaces as an error instead of a silent drop. If you see:

```
pydantic.ValidationError: 1 validation error for _ChatSpaceResponse
<newfield>
  Extra inputs are not permitted ...
```

The fix is to add the new optional field to the relevant model in
`src/models.py`, ship a new version, and redeploy. This is expected; the
tradeoff is explicit.

### 401 Unauthorized on every tool call immediately after a deploy

FastMCP's OAuth Proxy issues JWTs signed with `GCM_JWT_SIGNING_KEY`. If the
signing key changes, every existing token becomes invalid. Users reconnect
automatically on the next call (Claude re-initiates OAuth). Expected when you
rotate the signing key.

### `find_direct_message` fails with 403 or 404

The target user must be in the same Workspace domain as the authenticated
caller. External users, deleted accounts, and users without Chat enabled will
all 404. Surface the error to the human rather than silently retrying.

### Rate-limit rejections (`mcp_rate_limit_hits_total > 0`)

The default is 60 tool calls/minute per user. If legitimate usage is hitting
the cap, raise `GCM_RATE_LIMIT_PER_MINUTE`. Keep in mind Google's own quotas
(spaces.messages.create: 60/min per user by default) — raising ours won't help
past that ceiling.

## Reading the metrics

Scrape `GET /metrics`. The metrics that tend to move first:

| Metric | What it tells you |
|---|---|
| `mcp_tool_calls_total{status="error"}` | Rising = tools failing. Check `mcp_google_api_calls_total{status_code}` to find which upstream call broke. |
| `mcp_tool_latency_seconds` (P95) | Rising tail usually means Google-side slowness; check `mcp_google_api_latency_seconds`. |
| `mcp_rate_limit_hits_total` | Hot user or runaway loop. |
| `mcp_active_users` | Flat-to-zero during business hours = server is isolated from Claude; check `/readyz` and the reverse proxy. |

## Health endpoints

- `/healthz` — process is up. Always 200 if the container is serving.
- `/readyz` — DB reachable. 503 means SQLite is locked or the volume dropped.

Point your uptime monitor at `/readyz`.
