# Runbook

Operational procedures for the google-chat-mcp server. HTTPS-mode procedures
assume you are on the host running `docker compose`; stdio-mode procedures
are run by the end user in their own shell.

## Missing-scope errors after Google's granular-consent rollout

Symptom: tool call returns `ToolError` with text
`Missing required OAuth scope: <url>. Re-run ...`.

**Cause:** Google's January 2026 granular-consent feature lets users toggle
individual OAuth scopes at grant time. A user who declined one scope
(or the admin narrowed the consent screen) triggers this when a tool that
needs it is called.

**Fix:**

- Stdio: `google-chat-mcp logout && google-chat-mcp login --client-secret
  ./client_secret.json`. The login flow requests the full scope set; the
  user accepts the missing one on the consent screen.
- HTTPS: tell the user to revoke the server at
  https://myaccount.google.com/permissions (option A of "Revoking a user",
  below), then re-connect the MCP client to re-do the OAuth flow.

## Stdio: forgot `--client-secret` / can't find `client_secret.json`

`google-chat-mcp login` without `--client-secret` (and no `GCM_CLIENT_SECRET`
env var) exits with `error: --client-secret is required ...`.

Re-download Desktop-app credentials from Google Cloud Console → APIs &
Services → Credentials → your OAuth 2.0 Client ID → "DOWNLOAD JSON". Pass
that path on login.

## Stdio: lost `~/.config/google-chat-mcp/fernet.key`

Without the matching Fernet key, `tokens.json` cannot be decrypted. Symptoms:
`google-chat-mcp` or a tool call reports
`Cannot decrypt tokens.json. Either the Fernet key changed or the file is corrupt`.

**Fix:** `google-chat-mcp logout` (it tolerates the decrypt failure and
deletes both files anyway), then `google-chat-mcp login --client-secret
./client_secret.json`. A fresh Fernet key is generated on first login.

## Fernet key compromised (HTTPS mode)

Stored refresh tokens are encrypted at rest with Fernet. If the key leaks:

1. Generate a new key: `python -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())' > secrets/fernet_key`.
2. `docker compose stop mcp`.
3. Wipe the old encrypted store (tokens encrypted under the old key are
   now unreadable anyway): `docker run --rm -v google-chat-mcp_mcp_data:/v alpine sh -c 'rm -rf /v/oauth_store/*'`.
4. `docker compose up -d mcp`.
5. Users re-auth on their next MCP-client interaction.

Seamless rotation (no forced re-auth) requires a custom script that
decrypts with the old key and re-encrypts with the new one — out of scope
for this release. See "Rotating the Fernet key" below for the long form.

## GCP client secret compromised

The client secret in `secrets/google_client_secret` (HTTPS) or inside
`client_secret.json` (stdio) must be rotated at Google and locally.

1. Google Cloud Console → your OAuth 2.0 Client ID → "Reset Secret".
2. Update locally:
   - HTTPS: `printf '%s' 'new-secret' > secrets/google_client_secret && docker compose restart mcp`.
   - Stdio: re-download `client_secret.json` with the new secret; each
     user runs `google-chat-mcp logout && google-chat-mcp login
     --client-secret <new-path>`.
3. The old client secret is immediately invalid at Google — any refresh
   attempts fail until users re-login.

## Revoke an individual refresh token

### Stdio (the user themselves)

`google-chat-mcp logout` — POSTs the refresh token to Google's revoke
endpoint and deletes local files.

### HTTPS (admin, suspected compromise)

1. Ask the user to revoke at https://myaccount.google.com/permissions (fastest path; works without admin access).
2. If the user is unavailable, rotate the JWT signing key (option 1 under "Revoking a user" → Option B below) to invalidate every issued MCP bearer. All users reconnect on next call; this is a blast-radius trade-off.

## Onboarding the first user

1. Deploy the server with GCP project + secrets in place (see [README](../README.md)).
2. In your MCP client, add a custom connector pointing at `https://<your-host>/mcp`.
3. The client opens the OAuth consent screen — because the OAuth app is **Internal**,
   only users on your Workspace domain can proceed.
4. After grant, the client stores the MCP bearer and can call the tools.

There is no admin-side user registration. Every Workspace user self-onboards
through the OAuth flow on their first tool call.

## Revoking a user

### Option A — user-initiated (recommended)

The user visits `https://myaccount.google.com/permissions`, finds your
Google app (whatever name you set on the OAuth consent screen), and
removes it. Google invalidates the refresh token immediately.

### Option B — admin-forced, all users

There is no reliable per-user wipe in this release: the OAuth proxy stores its state keyed by JWT `jti`, not by Google `sub`, so you cannot cleanly pick a single user's record from the key-value store without a lookup that this server doesn't expose.

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

**This release does not support in-place rotation.** Rotating the key without re-encrypting the existing store invalidates every persisted upstream token; all users reconnect through OAuth on their next call. For a small single-workspace deployment this is usually acceptable — it's an interruption, not a data loss.

Procedure:

1. Generate the new key: `python -c 'from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())'`.
2. Overwrite `secrets/fernet_key` with the new value.
3. `docker compose stop mcp`.
4. Wipe the old encrypted store so the container doesn't try to decrypt with the new key and crash: `docker run --rm -v google-chat-mcp_mcp_data:/v alpine sh -c 'rm -rf /v/oauth_store/*'`.
5. `docker compose up -d mcp`.
6. Users re-auth on their next MCP-client interaction.

If you need seamless rotation (no forced reconnects), you'll have to write a re-encryption script yourself: iterate every file in `/var/lib/google-chat-mcp/oauth_store`, decrypt with the old key, re-encrypt with the new key, then swap keys. py-key-value's `FernetEncryptionWrapper` stores values as individual tokens so it's tractable. This is out of scope for this release.

## Backup and restore

**This release does not set up backups for you.** The app writes SQLite + KV store to the `mcp_data` Docker volume; nothing in this repo copies that volume anywhere else. Set up your own backup on the host — `docker run --rm -v google-chat-mcp_mcp_data:/src -v /your/backup/path:/dst alpine tar czf /dst/$(date +%F).tgz -C /src .` from cron is enough for most deployments.

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
automatically on the next call (the MCP client re-initiates OAuth). Expected
when you rotate the signing key.

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
| `mcp_active_users` | Flat-to-zero during business hours = server is isolated from any MCP client; check `/readyz` and the reverse proxy. |

## Live MCP-client smoke test (post-deploy)

After any significant upgrade, walk through at least one client per
transport. Automated tests cover wire-shape regression
(`tests/test_wire_shapes.py`), but client-specific quirks surface only
against a live server.

Suggested matrix:

| Transport | Client | Minimum checks |
|---|---|---|
| stdio | Claude Code (`mcp-server-google-chat`) | `whoami`; `send_message dry_run=true`; `send_message` real; `get_thread` |
| stdio | opencode | same |
| stdio | Cursor | same |
| HTTPS | Any MCP client that supports OAuth custom connectors | full OAuth flow end-to-end + the same four tool calls |

Record any client-specific quirk (tool-name length limits, custom-URI
rejections, permission prompts) here so the next upgrade knows.

## Health endpoints

- `/healthz` — process is up. Always 200 if the container is serving.
- `/readyz` — DB reachable. 503 means SQLite is locked or the volume dropped.

## Security model

For the threat model, trust boundaries, and the full set of
security-relevant invariants enforced in code, see
[`docs/security.md`](./security.md). That doc is the authoritative
reference for "is this safe?" questions in PR review and for deployers
evaluating the project.

Operator highlights (rules the server can't enforce for you):

- **Don't share `fernet_key` across deployments** — a compromised disk
  in one environment leaks tokens for all environments sharing the key.
  See "Rotating the Fernet key" above.
- **Don't enable `GCM_DEV_MODE=1` outside test environments** — that
  switches off the upstream-URL safety check and would let a host-side
  attacker exfiltrate access tokens via `GCM_CHAT_API_BASE`.
- **Don't enable `GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME=1` outside test
  environments** — that switches off the chmod-0700 safety check on
  arbitrary directories.
- **Don't flip `GCM_AUDIT_HASH_USER_SUB` mid-deployment** — old hashed
  rows + new raw rows can't be joined cleanly across the rotation point.

## People API resolution caveats

Several read-side tools resolve `users/{id}` → email + display name via
Google's People API (`people.get`). In practice, **only the authenticated
user (self) resolves reliably**; non-self Workspace users almost always
come back with `emailAddresses=null` and `names=null`, even when the
caller has `directory.readonly` granted.

**What this affects:**

- `list_members` and `get_messages` — `email` / `sender_email` and
  `display_name` / `sender_display_name` are frequently `null` for
  anyone other than the caller. Treat nullability as the common case,
  not the edge case.
- `remove_reaction` by `(message, emoji, user_email)` — the tool
  server-filters on emoji and resolves each reactor's email via People
  API. When People API returns `null` for a reactor, the email-match
  step silently skips that reactor, and the tool can report
  `removed=false` even when the target reaction is present. If you
  hit this, fall back to the direct-delete shape: pass the full
  `reaction_name` (fetch it via `list_reactions`).

**Why it happens:** Google scopes People-API visibility to the
directory membership of the caller's own contacts + their own
profile. Workspace directory visibility doesn't help here — that's a
separate API (`admin.directory.users`) with a heavier scope we don't
request.

**Don't try to "fix" it:** widening to the Admin Directory scope would
require a Workspace-admin install and doesn't belong in a per-user
tool. The right response is clear nullability in the docs (done
above), and for destructive paths that depend on a reliable email
match (`remove_member` in v0.3.1), only offering the by-resource-name
shape.

## search_people returns zero DIRECTORY hits for Workspace users

Symptom: `search_people` for a known-present Workspace user returns an
empty list or only `CONTACTS`-tagged hits, even though the target is
clearly in the caller's domain directory. Typical error in server
logs: `people.searchDirectoryPeople returned 403: The G Suite domain
admin has disabled external directory sharing`.

**Cause:** `people:searchDirectoryPeople` is gated by two *separate*
admin settings — don't confuse them:

1. **Directory sharing → Contact sharing (internal)** — controls
   whether domain members can see each other in directory search.
   Enabling this is necessary but not sufficient for our MCP server.
2. **External directory sharing** — governs whether third-party
   OAuth apps (which is what our Cloud-project OAuth client is, from
   Google's perspective) can read directory data on behalf of
   authenticated users. The default **"Authenticated user basic
   profile fields"** option only lets the app read the caller's OWN
   profile — all profile info of other users in the organization is
   withheld, which surfaces as zero-hit queries.

**Fix — pick one (ranked by security posture, cleanest first):**

1. **Allow-list the specific OAuth client.** `admin.google.com →
   Security → API controls → App access control → Google Services →
   Contacts API → "Restricted but trust this specific app"` and paste
   the Cloud project's OAuth client ID. Surgical — only your MCP
   server gets directory access; other OAuth apps stay restricted.
2. **Widen external directory sharing.** `admin.google.com → Apps →
   Google Workspace → Directory → Directory settings → External
   directory sharing → Share all info` (or "Share only domain
   profiles" if a narrower disclosure is preferred). Broadest fix;
   opens directory reads to any OAuth app granted `directory.readonly`.
3. **Accept the limitation.** `DIRECTORY` source returns empty for
   non-self queries; CONTACTS fallback covers people the caller has
   personally corresponded with. Document for callers.

Propagation is typically 1-5 minutes. Verify with a fresh
`search_people` call — `sources_succeeded` should now contain
`"DIRECTORY"` and `people` should contain the teammate.

**Workaround for non-admins:** fall back to `search_people` with
`sources=["CONTACTS"]` — the caller's personal contacts + "other
contacts" auto-populated from Chat interactions. Coverage is narrower
(only people the caller has actually corresponded with) but works
regardless of Workspace-level directory-sharing posture.

## search_people on consumer Gmail accounts

Consumer `@gmail.com` accounts have no Workspace directory.
`DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE` / `..._CONTACT` both 403 for
these callers. `search_people` transparently drops the DIRECTORY
source and returns only `CONTACTS` hits — inspect
`sources_succeeded` on the result to confirm.

No admin action is available. If a consumer-Gmail caller needs to
resolve a Workspace user they've never corresponded with, the email
has to be pasted manually — there is no other upstream path that
respects the per-user privacy boundary.

## Restricted-tier scopes (chat.messages + chat.spaces)

Symptom: question from a deployer considering whether to publish their
app externally — do the restricted-tier scopes used by `update_message`
/ `delete_message` / `update_space` trigger Google's annual CASA
security assessment?

**Cause:** two scopes sit in Google's **restricted** tier, the strictest
verification tier:

- `https://www.googleapis.com/auth/chat.messages` — used by
  `update_message` and `delete_message`. Added in v0.3.2.
- `https://www.googleapis.com/auth/chat.spaces` — used by `update_space`.
  Added in v0.4.0. Google lists only this umbrella for `spaces.patch`
  under user OAuth; the granular `chat.spaces.create` / `.readonly`
  scopes we also hold do **not** cover the patch method.

For an Externally-published app (visible to all Google users), Google
requires an annual third-party Cloud Application Security Assessment
(CASA) on top of the standard sensitive-tier verification. The audit
cost is non-trivial — usually several thousand USD per year per Cloud
project — and recurs. A single CASA covers the full restricted-scope set
granted to that Cloud project, so adding a second restricted scope
doesn't double the audit fee.

**Who's affected:** only deployers who set the OAuth consent screen
to **External** AND publish to **In production** AND want to support
Google users outside their own organization. The exact triggers:

- **Internal app** (Workspace org-only): not affected. No verification
  required, no CASA. Just declare the scopes and ship.
- **External app in Testing mode** (≤100 test users): not affected.
  Users see the unverified-app warning and click through.
- **External app published In production**: CASA review applies — file
  with Google's verification team, expect 6-12 weeks.

**If you don't need the restricted scopes:** drop the restricted-scope
tools from your install. Remove `CHAT_MESSAGES` and/or `CHAT_SPACES`
from `GOOGLE_OAUTH_SCOPES` in `src/config.py`, drop the matching tool
registrations in `src/app.py`, and the remaining surface stays on
sensitive-tier scopes. The opt-out groupings:

- Drop `CHAT_MESSAGES` → remove `update_message` + `delete_message`.
- Drop `CHAT_SPACES` → remove `update_space`.

This is the supported opt-out path; both groups are modular.

## sender_email / display_name are null on non-self users

Symptom: `get_messages` / `get_thread` / `get_message` / `list_members`
return entries with `sender_email: null` and `sender_display_name: null`
for users other than the caller themselves, even though
`directory.readonly` is granted and the People API returns 200 OK.

**Cause:** Google's `people.get(people/{id})` endpoint reliably resolves
only `people/me`. Passing an arbitrary `users/{id}` (translated to
`people/{id}`) returns a shell response with no `emailAddresses` /
`names` fields populated — regardless of scope. This is a People API
behavioral limitation, not a scope gap. The tools degrade gracefully to
`null` rather than failing the whole call.

**The correct non-self resolver is `search_people`.** It hits
`people.searchDirectoryPeople` (Workspace directory) and
`people.searchContacts` (caller contacts), which DO return populated
email + name fields. The same call back-fills the local directory cache
so subsequent `get_messages` / `list_members` calls resolve the same
user without another upstream round-trip.

**Recommended agent flow:**

1. If you need an email for a user you've seen in a message or
   membership list, run `search_people(query="<partial name or email>")`
   first. The back-fill makes the next read call cheap.
2. Treat `sender_email: null` as "not resolved yet," not "user has no
   email." Retry via `search_people` if the value matters to your flow.

**Not a bug:** the degrade is intentional. `search_people` is a separate
tool because agents often don't need the resolution at all (e.g. reading
a DM's messages when the caller already knows who sent them).

## add_member returns a membership_name when the user is already present

Symptom: `add_member` on a user who's already a member of the target
space returns a successful `AddMemberResult` with a populated
`membership_name` rather than the `ToolError("already a member")`
the unit tests exercise.

**Cause:** Google's Chat `spaces.members.create` is idempotent-by-nature
in practice — duplicate adds return HTTP 200 with the *existing*
membership record rather than 409 `ALREADY_EXISTS`. The 409 path is
documented and reachable (older Workspace editions, some edge cases),
so the handler still wraps it into a `ToolError`, but the common-case
observation is that Google just returns the existing record.

**Not a bug:** the handler code correctly handles both shapes. If you
care about detecting the idempotent case specifically, compare the
returned `membership_name`'s membership ID against the user's known
Google numeric ID before + after — a no-change ID is the signal, not
the response status. In most agent flows this distinction doesn't
matter; treat a successful `add_member` as "user is in the space now",
whether they were there a second ago or not.

Point your uptime monitor at `/readyz`.
