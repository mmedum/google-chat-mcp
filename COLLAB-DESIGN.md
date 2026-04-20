# Google Chat MCP — Design Proposal

**Status:** Draft — decisions below are proposed, open for pushback.
**Audience:** maintainers of `mmedum/google-chat-mcp` and other Partisia
engineers who may contribute. Eventual open-source users once the design
settles.

---

## 1. Overview

`google-chat-mcp` exposes Google Chat as an MCP tool surface for any
MCP-compatible client (Claude Code, Claude web/Desktop/mobile, opencode,
Cursor, Continue, Goose, etc.). It runs in two modes from a single
codebase because no single transport reaches every client we care about:

- **Stdio mode** — installed locally, spawned as a subprocess. This is
  the *only* way to reach clients that run MCP servers as subprocesses
  (Claude Code, opencode, Cursor, Continue, Goose), and it's the
  minimum-setup path for individual users: no hosting, no TLS, no
  ngrok, no OAuth-proxy layer, no public hostname. Tokens live on the
  user's laptop.
- **HTTPS mode** — single-tenant self-hosted server. The *only* way to
  reach clients that phone home from vendor infrastructure and can't
  speak stdio (Claude web/Desktop/mobile), which they do via the
  custom-connector OAuth flow.

Neither mode replaces the other. Together they cover every MCP client
in use:

| Client | Stdio mode | HTTPS mode |
| --- | --- | --- |
| Claude Code | ✅ | ✅ (if configured for HTTP MCP) |
| opencode, Cursor, Continue, Goose | ✅ | ✅ where the client supports HTTP MCP |
| Claude Desktop | ✅ (local stdio) | ✅ (custom connector) |
| Claude web | ❌ | ✅ (custom connector) |
| Claude mobile | ❌ | ✅ (custom connector, inherited from account) |

Authentication is user-OAuth against Google. Each deployment brings its
own Google OAuth client, marked Internal to the deployer's Workspace; no
Google verification or CASA assessment is required at single-tenant
scale. A multi-tenant SaaS variant is out of scope for the current
iteration and tracked separately as M6.

## 2. Goals

- Cover the natural surface of user-level chat operations: reading DMs /
  spaces / threads, sending messages (including thread replies),
  reactions, member lookups, space metadata, self-identity, and
  space-scoped search.
- Work in every MCP client Partisia engineers use: stdio-spawning CLIs
  for Phase-1 work, custom connectors for Phase-2 surfaces.
- Remain client-agnostic: no hard-coded redirect URLs for a specific
  MCP host, no message-body mutation that leaks the client's identity.
- Be deployable by any team or individual without rebuilding the
  container or forking the repo — the OAuth client identity is
  per-deployment configuration.
- Preserve the operational posture already built into the repo: Docker
  packaging, Fernet-encrypted refresh tokens at rest, structlog +
  redaction processor, Prometheus metrics, SQLite audit log with nightly
  prune, retry-with-backoff honouring `Retry-After`, respx-backed tests
  with an 80% coverage gate.
- Zero Google approval requirement for single-tenant deployments.

## 3. Non-goals

- Sending as a Chat *app* / bot (user OAuth only).
- Chat card v2 interactive UIs (app-auth only; separate product surface).
- Admin-level operations: impersonation, audit exports, org-wide
  settings.
- Real-time push / subscriptions — polling only.
- Unbounded cross-space search. `search_messages` is space-scoped, with
  an optional small explicit list of spaces as an extension.
- `update_message` / `delete_message`. Deferred pending demand.
- Postgres migration. SQLite remains sufficient at single-tenant scale.
- Multi-tenant hosted SaaS. Explicit M6, deferred.

## 4. Architecture

```
MCP client ──(stdio | HTTPS)──► google-chat-mcp
                                     │
                                     ├── transport layer
                                     │      ├── stdio: FastMCP stdio transport
                                     │      └── HTTPS: FastMCP GoogleProvider / OAuthProxy
                                     │
                                     ├── tool handlers (src/tools/)
                                     │      each wraps invoke_tool():
                                     │      rate-limit → auth lookup → call → audit + metrics
                                     │
                                     ├── chat_client.py — shared httpx.AsyncClient
                                     │      10s timeout, exp-backoff retry on 5xx/429,
                                     │      Pydantic-validated JSON
                                     │
                                     ├── storage.py — SQLite (WAL): audit log + People-API
                                     │                email cache
                                     ├── rate_limit.py — in-memory token bucket, 60/min
                                     │                   per user sub
                                     └── observability.py — structlog JSON + Prometheus
```

Everything below the transport boundary is shared between stdio and
HTTPS modes. The two modes differ only in how user access tokens are
acquired (§9).

## 5. Key decisions

### 5.1 User OAuth, not Chat app

Messages must appear as the user, not as a bot. Consequences:

- Scopes are user Chat scopes (§6).
- Reactions work: app-auth cannot add reactions.
- DMs work without the "bot in every space" problem.
- Google stamps user-OAuth messages with an unsuppressible "via
  &lt;AppName&gt;" attribution badge. This is the "AI helped send this"
  signal we want, delivered for free.

The server posts message bodies verbatim. No client identity, suffix,
or prefix is appended server-side. Attribution is the job of the Google
badge, not the tool.

### 5.2 Direct send, with a `dry_run` escape hatch

`send_message` posts directly with no separate draft step. Rationale:

- Most MCP clients render a tool-approval dialog with the full payload
  before executing. A `draft_message` tool would be a redundant second
  approval on the same action.
- Google Chat has no native Drafts surface to push to — "draft" would
  just mean "return the payload without POSTing", which is the
  approval-dialog contents.

`send_message` takes an optional `dry_run: bool`. When true the tool
renders the resolved payload (mentions, markdown, thread target) and
returns it without POSTing. This covers ungated contexts — Agent-SDK
loops, `--permission-mode bypassPermissions`, clients that don't gate
tool calls with approval.

### 5.3 Two transports from one codebase

**Goal:** reach every MCP client with a single implementation, by
paying the cost of two transports once and amortizing it across every
tool. See §1 for why neither transport alone suffices.

The tool layer, chat client, storage, rate limiter, and observability
processors are all transport-agnostic. Stdio and HTTPS modes share them
and differ only at the edges:

- **Stdio:** entry point uses `FastMCP.run(transport="stdio")`.
  Bypasses `GoogleProvider` and Claude-facing OAuth. User tokens are
  acquired via a local CLI login flow (§9.1).
- **HTTPS:** entry point mounts Streamable HTTP at `/mcp` with
  `GoogleProvider` / `OAuthProxy` (§9.2).

Audit log and rate limiter are active in HTTPS mode by default, and
opt-in (or off) in stdio mode where there is only one user.

### 5.4 App display name: `MCP for Google Chat`

Google brand guidelines disallow the "Google" word in app names but
explicitly allow the "for Google X" attribution pattern. `MCP for Google
Chat` is that pattern; it is valid at every phase and survives to
public launch without a rename.

### 5.5 Bring-your-own OAuth client

Each deployment supplies its own Google OAuth client ID and secret.

- **Stdio mode**: user points `login` at their own `client_secret.json`.
- **HTTPS mode**: operator mounts the client ID/secret via Docker
  secrets or env vars; configuration is per-deployment rather than
  baked into the image. This is the M5 work item — see §10.

Under this model each deployer's OAuth client is Internal to their own
Workspace, so Google approval (verification, brand review, CASA
assessment) is not triggered. Only M6 — a central multi-tenant client
— would touch Google's review machinery.

### 5.6 Runtime

Python 3.14, FastMCP `~= 3.2`, Pydantic v2, httpx, structlog, Fernet
(cryptography). Versions pinned in `pyproject.toml` and
`.python-version`. Dev tooling: ruff, ty (0.0.x beta, pinned exactly),
pytest + respx, pre-commit.

## 6. OAuth scopes

| Scope | Purpose | Classification |
| --- | --- | --- |
| `chat.spaces.readonly` | List / inspect spaces | Sensitive |
| `chat.messages.readonly` | Read messages / threads / attachments | **Restricted** |
| `chat.messages.create` | Send messages (incl. thread replies) | Sensitive |
| `chat.messages.reactions` | Add / remove reactions | Sensitive |
| `chat.memberships.readonly` | List members of a space | Sensitive |
| `chat.spaces.create` | Materialize a DM from `find_direct_message` when none exists | Sensitive |
| `directory.readonly` | Resolve user email → display name (People API) | Sensitive |
| `openid`, `email`, `profile` | Self-identity | Standard |

The umbrella `chat.messages` scope currently in use is replaced by the
narrower split above (`chat.messages.create` + `chat.messages.reactions`).
This triggers a one-time re-consent on existing deployments — flagged
in the M2 release notes.

Restricted vs. sensitive classification is invisible at single-tenant
scale (each deployer's Internal OAuth client is not reviewed) but
matters for a hypothetical M6. Keeping the narrow split now preserves
that option.

`chat.spaces.create` is load-bearing for `find_direct_message`, which
creates a DM on the fly when the caller has never messaged the target
user. An alternative is to downgrade `find_direct_message` to
lookup-only and have `send_message` materialize the DM on first send;
that path drops the scope and simplifies the consent screen. See §11.

**Granular consent (Jan 2026):** Google now lets users toggle
individual scopes at grant time. Tools that hit a missing scope return
a structured error naming the scope, so the user knows what to
re-authorize.

## 7. Tool surface

| Tool | Purpose |
| --- | --- |
| `whoami` | Signed-in user's email + display name |
| `list_spaces` | DMs, group DMs, named spaces; supports `limit`, `space_type`, pagination |
| `get_space` | Metadata for one space (display name, type, create time) |
| `list_members` | Members of a space, with roles and email hydration |
| `find_direct_message` | Resolve a user email to a DM space (creates if absent; see §6) |
| `get_messages` | Recent messages, newest first, sender-hydrated; pagination |
| `get_message` | Single message by resource name, reactions hydrated |
| `get_thread` | All messages in one thread, ordered |
| `search_messages` | Space-scoped text search; exact + regex modes (§8) |
| `send_message` | Post to space / DM; optional `thread_name`, `dry_run` |
| `add_reaction` | Unicode emoji + message resource name |
| `remove_reaction` | By reaction name or (emoji, user) match |
| `list_reactions` | Reactions on a message (inline in `get_message` when small) |

Tool descriptions explicitly tell the model when to prefer each — e.g.
"`search_messages`: always pass a target space and `createTime` lower
bound; for org-wide history, direct the user to the Chat web UI". This
is the single highest-leverage thing for getting good model behaviour.

## 8. Search caveat

The Google Chat API has no full-text search endpoint. The Chat web /
mobile UI uses a private Caribou/Dynamite backend authenticated via
browser session cookies, which is not accessible from OAuth clients
and would violate Google's ToS to use. Cloud Search excludes Chat;
Vault is admin-only e-discovery; Gemini Enterprise indexes Chat but on
a separate paid add-on surface.

`search_messages` therefore implements client-side filtering:

1. Require a target space (or a small explicit list, ≤5).
2. Page through `spaces.messages.list` within the requested time
   window.
3. Filter in-process (case-insensitive substring or regex).
4. Return matches with a few lines of surrounding context.

Unbounded org-wide search is a non-goal (§3).

## 9. Auth flows

### 9.1 Stdio mode

1. `google-chat-mcp login --client-secret path/to/client_secret.json`.
2. Server spins up a local listener on `127.0.0.1:<random>`, opens the
   browser to Google's OAuth consent screen using the user's own
   client.
3. On callback, stores the refresh token Fernet-encrypted at
   `~/.config/google-chat-mcp/tokens.json` (mode 0600), with a
   per-installation key stored alongside.
4. `google-chat-mcp` (no args) runs the stdio MCP server, reading
   tokens + client secret on startup.
5. `logout` wipes tokens and revokes via Google's revoke endpoint.

Paths are configurable via env vars (`GCM_TOKENS_PATH`,
`GCM_CLIENT_SECRET`) so CI and containers can inject both.

### 9.2 HTTPS mode

Two OAuth relationships run stacked:

```
[MCP client] ──OAuth 2.1──► [our server] ──OAuth 2.0──► [Google]
     ↑          bearer JWT                refresh token
```

- **Upstream (server ↔ Google):** standard user-OAuth. Refresh tokens
  Fernet-encrypted at rest; access tokens re-minted per call.
- **Downstream (MCP client ↔ server):** the server is itself an OAuth
  2.1 authorization server per the MCP spec. Handled by FastMCP's
  `GoogleProvider` / `OAuthProxy` — PKCE S256, dynamic client
  registration (RFC 7591), JWT bearer tokens signed with a server-side
  key, the standard metadata endpoints (`/.well-known/*`,
  `/authorize`, `/token`, `/register`).

CORS allowlist and allowed-client-redirect configuration are
per-deployment (`GCM_ALLOWED_CLIENT_REDIRECTS`). Public HTTPS hostname
required; ngrok with a reserved domain is the documented dev path.

Single-tenant isolation: the deployer configures an allowed
email-domain list, and `/authorize` rejects callbacks whose Google
ID-token `email` doesn't match. A stray outside visitor can't finish
the handshake even if they find the URL.

## 10. Phased plan

Six milestones. Each leaves the repo in a working, improved state.

### M1 — Stdio mode (2–3 weeks)

**Goal:** unblock Claude Code, opencode, Cursor, Continue, and Goose —
the stdio-spawning clients Partisia engineers use day-to-day — and
give individual users a working install with no hosting, no TLS, no
ngrok, and no OAuth-proxy setup.

- Stdio entry point using FastMCP's stdio transport.
- CLI `login` / `logout` commands with `127.0.0.1:<random>` OAuth
  callback.
- Fernet-encrypted local token store at `~/.config/google-chat-mcp/`.
- Config loader that reads `--client-secret` (CLI) or
  `GCM_CLIENT_SECRET` (env).
- Packaged for `uv tool install` / `pip install`.
- GCP project setup guide for individual users, tested against a fresh
  Workspace account.
- Existing tools work unchanged over stdio; HTTPS path unchanged.

**Exit:** `uv tool install google-chat-mcp` → one-time Google consent
→ works in Claude Code / opencode / Cursor.

### M2 — Scope narrowing (half a day)

**Goal:** shrink the OAuth consent screen to what the server actually
uses, and avoid the restricted `chat.messages` umbrella so M6 stays
reachable without an unnecessary CASA-scope dependency.

- Split `chat.messages` umbrella into `chat.messages.create`; add
  `chat.messages.reactions` when the reaction tools land (M3) rather
  than up-front.
- Keep `chat.spaces.create` for `find_direct_message`'s create-on-demand
  fallback (see §6 and §11 for the alternative).
- Release note: existing users see a one-time re-consent prompt on next
  login.

**Exit:** narrower consent screen; no behavioural change.

### M3 — Reactions, threads, self-identity, search (1–2 weekends)

**Goal:** fill in the chat operations a user would reasonably expect
but the server doesn't yet cover — reactions, thread drill-down,
single-message lookup, self-identity, and space-scoped search — so the
tool surface matches §7.

- `whoami`, `get_thread`, `get_message`, `add_reaction`,
  `remove_reaction`, `list_reactions`, `search_messages`.
- Each as a new file under `src/tools/` with respx-backed tests that
  preserve the 80% coverage gate.

**Exit:** full tool surface per §7.

### M4 — Pagination polish (a weekend)

**Goal:** let callers page through long result sets without silent
truncation, while keeping the current cap-at-100 default so existing
callers don't notice.

- Expose `pageToken` / `pageSize` on `list_spaces` and `get_messages`;
  return Chat's native continuation token to callers that opt in.
- Back-compat: when the params are omitted, behaviour matches today
  (cap at 100), so existing callers are unaffected.

**Exit:** no silent truncation for callers that opt in; no break for
callers that don't.

### M5 — Configurable OAuth for HTTPS mode (1–2 weeks)

**Goal:** let any Partisia team (or external org) run the published
container against their own Google OAuth client without forking the
repo. After M5 the image is genuinely redeployable rather than
re-runnable only against one deployer's secrets layout.

- Invert the HTTPS-mode OAuth-client configuration the same way M1 did
  for stdio: the client ID / secret come from per-deployment config
  rather than a fixed secrets layout.
- Deployers plug in their own Google credentials without rebuilding the
  image.

**Exit:** the published container is usable by any team against their
own Google client without a fork.

### M6 — Multi-tenant hosted SaaS (deferred)

**Goal:** serve users from orgs the operator doesn't own — which is
the only scenario that triggers Google's review machinery. Pursued
only with a specific business case, because the ongoing compliance
cost is real.

Central GCP project +
OAuth client (External user type), Google OAuth verification (~3–5
business days for sensitive scopes), annual CASA audit for
`chat.messages.readonly`, privacy policy, domain verification, optional
Marketplace listing, multi-tenant token storage with strict tenant
isolation, on-call and incident response.

**Size ranking:** M1 ≫ M2 < M3 < M4 < M5 ≪ M6.

## 11. Open questions

- **`chat.spaces.create` — keep or drop?** Currently load-bearing for
  `find_direct_message`'s create-on-demand behaviour. Alternative:
  downgrade that tool to lookup-only and let `send_message` materialize
  the DM on first send (Chat API supports this server-side). That
  removes the scope and simplifies the consent screen, at the cost of a
  small DX regression in the "first DM ever" case. Leaning: keep for
  M1–M4, revisit during M5.
- **Cross-space search bound.** `search_messages` is space-scoped by
  default but accepts a small explicit list of spaces (cap e.g. 5). Is
  that list an acceptable extension, or should we hold strictly to
  single-space-only?
- **Package name on PyPI** — `google-chat-mcp`, `mcp-for-google-chat`,
  `partisia-chat-mcp`. Must match the CLI entry point.
- **Semantic search in `search_messages`.** Adding embeddings
  (`sentence-transformers` or similar) is a heavy dependency for modest
  gain. Leaning: drop from M3, revisit only if asked.
- **Attachment handling** — read-only in v1 (return URLs), or full
  upload? Leaning: read-only for v1.
- **Shipping cadence** — tagged releases to PyPI from M3 onward, or
  install-from-git until M5? Leaning: PyPI from M3.
- **Long-term repo home** — stays at `mmedum/google-chat-mcp`, or moves
  to a Partisia-owned namespace once the collaboration solidifies?
  Matters for PyPI ownership, CODEOWNERS, and trust signals for
  external users. Doesn't block the contribution model in §13.

## 12. Risks

- **BYO setup friction.** If the GCP project setup guide is bad, nobody
  adopts. Mitigation: treat it as a first-class deliverable in M1,
  test against a fresh Workspace account, and optionally drive the
  automatable steps through a `setup` CLI that wraps `gcloud`.
- **Workspace admin policies block Internal OAuth clients.** Some orgs
  require admin allowlisting. Mitigation: document the Admin Console
  steps alongside the user-side setup.
- **Scope narrowing triggers a re-consent prompt.** M2 prompts existing
  users on next login. Mitigation: version bump + release notes.
- **Google Chat API schema drift.** Mitigation: Pydantic `extra="forbid"`
  on response models surfaces drift loudly; runbook covers the fix
  (add the new optional field rather than relax validation).
- **Abuse potential** — an AI harness mis-sending under user identity.
  Mitigation: rely on the client's tool-approval UI to gate
  `send_message`; offer `dry_run` for ungated contexts; the Google "via
  &lt;AppName&gt;" badge lets recipients identify AI-assisted messages.
- **Restricted-scope verification blocks public launch** — only
  relevant if M6 is ever pursued; not blocking M1–M5.

## 13. Contribution model

Collaborators with push access work in short-lived feature branches on
`origin`, one PR per milestone chunk. Rationale:

- Single source of truth for CI, Actions secrets, Dependabot, and
  review.
- Review stays in one repo; discussion doesn't fragment across
  fork/upstream.
- No divergence risk: feature branches rebase cleanly onto `main` as
  fixes ship.

Branch naming: `<initials>/<milestone>-<short-topic>`, mirroring the
repo's existing convention (`mmedum/docs/ngrok-local-dev`,
`mmedum/ci/harden-actions`). Examples: `jg/m1-stdio-entry`,
`jg/m2-scope-split`.

Review cadence: PRs reviewed within 24h when practical. Milestone
ownership assigned per-milestone — one owner at a time on any given
area to avoid stepping on each other's work.

External contributors without push access use the standard fork-and-PR
workflow.

## 14. References

**Prior art worth consulting during implementation:**

- [`siva010928/multi-chat-mcp-server`](https://github.com/siva010928/multi-chat-mcp-server)
  — MIT-licensed Python MCP covering ~90% of the target tool surface in
  §7. Useful as a reference for tool signatures, the non-obvious
  `thread.name` vs `thread.threadKey` distinction in
  `spaces.messages.create` replies, and a YAML-driven search-mode
  registry. Anti-patterns there to *not* copy: OAuth codes printed to
  stdout, module-global token state via `sys.modules`, single
  hardcoded token path per provider, hidden cap-at-30 pagination,
  no `remove_reaction`.

**External:**

- [Google Chat API: Send a message](https://developers.google.com/workspace/chat/create-messages)
- [Google Chat: Authenticate and authorize (scopes)](https://developers.google.com/workspace/chat/authenticate-authorize)
- [Google Chat API: Create a reaction](https://developers.google.com/workspace/chat/create-reactions)
- [Sensitive scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/sensitive-scope-verification)
- [Restricted scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/restricted-scope-verification)
- [Granular OAuth consent for Chat apps (Jan 2026)](https://workspaceupdates.googleblog.com/2026/01/granular-oauth-consent-google-chat-apps.html)
- [FastMCP `GoogleProvider` / `OAuthProxy` (upstream)](https://gofastmcp.com/integrations/google)
