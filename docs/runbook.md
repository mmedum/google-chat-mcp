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

### Option B — admin-forced, all users

There is no reliable per-user wipe in v1: the OAuth proxy stores its state keyed by JWT `jti`, not by Google `sub`, so you cannot cleanly pick a single user's record from the key-value store without a lookup that this server doesn't expose.

Two supported admin actions:

1. **Rotate the JWT signing key.** This invalidates every issued MCP bearer. All users reconnect on their next call. Works immediately and requires no per-user identification.

    ```bash
    python -c 'import secrets; print(secrets.token_urlsafe(48))' > secrets/jwt_signing_key
    docker compose restart mcp
    ```

2. **Nuke the entire OAuth state.** Forces everyone to re-grant scopes in Google as well. More disruptive; use only when you want to cut all upstream refresh tokens.

    ```bash
    docker compose stop mcp
    docker volume inspect google-chat-mcp_mcp_data  # confirm the volume you're about to touch
    docker run --rm -v google-chat-mcp_mcp_data:/v alpine sh -c 'rm -rf /v/oauth_store/*'
    docker compose up -d mcp
    ```

Export the user's audit-log activity first if you need it for record-keeping:

```bash
docker compose exec mcp sqlite3 /var/lib/google-chat-mcp/app.sqlite \
  "SELECT timestamp, tool_name, target_space_id, success FROM audit_log \
   WHERE user_sub = '<sub>' ORDER BY timestamp DESC LIMIT 100;"
```

For targeted per-user revocation, prefer Option A.

## Rotating the Fernet key

Stored refresh tokens are encrypted at rest with Fernet. Rotate on suspected
compromise (or on whatever cadence your policy demands).

**v1 does not support in-place rotation.** Rotating the key without re-encrypting the existing store invalidates every persisted upstream token; all users reconnect through OAuth on their next call. For a small single-workspace deployment this is usually acceptable — it's an interruption, not a data loss.

Procedure:

1. Generate the new key: `python -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())'`.
2. Overwrite `secrets/fernet_key` with the new value.
3. `docker compose stop mcp`.
4. Wipe the old encrypted store so the container doesn't try to decrypt with the new key and crash: `docker run --rm -v google-chat-mcp_mcp_data:/v alpine sh -c 'rm -rf /v/oauth_store/*'`.
5. `docker compose up -d mcp`.
6. Users re-auth on their next Claude interaction.

If you need seamless rotation (no forced reconnects), you'll have to write a re-encryption script yourself: iterate every file in `/var/lib/google-chat-mcp/oauth_store`, decrypt with the old key, re-encrypt with the new key, then swap keys. py-key-value's `FernetEncryptionWrapper` stores values as individual tokens so it's tractable. This is out of scope for v1.

## Backup and restore

**v1 does not set up backups for you.** The app writes SQLite + KV store to the `mcp_data` Docker volume; nothing in this repo copies that volume anywhere else. Set up your own backup on the host — `docker run --rm -v google-chat-mcp_mcp_data:/src -v /your/backup/path:/dst alpine tar czf /dst/$(date +%F).tgz -C /src .` from cron is enough for most deployments.

Restore, assuming you have a tarball:

```bash
docker compose stop mcp
docker run --rm -v google-chat-mcp_mcp_data:/dst -v /your/backup/path:/src alpine \
  sh -c 'rm -rf /dst/* && tar xzf /src/<date>.tgz -C /dst'
docker compose up -d mcp
```

Audit-log and directory-cache are in `app.sqlite`; OAuth state is in `oauth_store/`. Restore both together — they reference each other by user sub.

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
