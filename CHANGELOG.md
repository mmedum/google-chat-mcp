# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.1] - 2026-04-21

Adds membership mutation + people resolution. **Two new sensitive-tier OAuth
scopes** in this release; deployers re-consent once.

### Added
- `add_member(space_id, user_email, dry_run)` — invite a user to a space via
  `spaces.members.create`. 409 `ALREADY_EXISTS` from Google surfaces as a
  `ToolError` naming the user (not an idempotent success — the existing
  membership_name belongs to the original inviter and would mislead callers).
- `remove_member(membership_name, dry_run)` — delete a membership by full
  resource name. Idempotent: double-delete returns `removed=false` on 404
  NOT_FOUND or 403 PERMISSION_DENIED. Missing-scope 403s are excluded from
  the idempotent path so callers still see the re-auth prompt. There is no
  email-filter shape — non-self People API resolution is unreliable (see
  the runbook's People API caveats), so an email-based lookup would
  silently miss the target.
- `search_people(query, limit, sources)` — hybrid lookup over Workspace
  directory (`people:searchDirectoryPeople`) + caller's contacts
  (`people:searchContacts`). Runs both sources in parallel via
  `asyncio.gather` by default; sources tagged per hit. Workspace-profile
  hits back-fill the DirectoryCache so later `get_messages` /
  `list_members` resolve `sender_email` without another People API call.
  Contact-ID hits surface but do NOT back-fill — different namespace,
  would poison `users/{id}` lookups.

### Changed (breaking for deployers)
- **OAuth scopes**: two new entries in `GOOGLE_OAUTH_SCOPES`.
  - `https://www.googleapis.com/auth/chat.memberships` (sensitive tier) —
    `add_member` + `remove_member`.
  - `https://www.googleapis.com/auth/contacts.readonly` (sensitive tier) —
    `search_people` consumer-Gmail fallback.

  Every HTTPS deployer updates the OAuth consent screen; every user
  re-consents on next MCP call. Stdio users re-run `google-chat-mcp logout &&
  google-chat-mcp login`.
- **Internal Workspace apps (`External → Internal` in the OAuth consent
  screen) skip Google's sensitive-tier verification entirely.** Deployers
  publishing internally — the primary audience — don't file paperwork;
  just declare the scopes.

### Documented
- `docs/runbook.md`: new "search_people: directory sharing must be enabled
  by Workspace admin" and "consumer Gmail fallback path" sections. Admin
  action (`admin.google.com → Apps → Google Workspace → Directory →
  Directory sharing`) is required for `DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE`
  to return non-empty results for non-admin users.
- `docs/gcp-setup.md`: updated scope list.

## [0.3.0] - 2026-04-20

Adds two space-creation tools on the existing `chat.spaces.create` scope.
**No new OAuth scope** — deployers don't re-consent.

### Added
- `create_group_chat(member_emails, dry_run)` — unnamed multi-person DM
  (`spaceType=GROUP_CHAT`). `member_emails` excludes the caller; 2-20
  members (self-imposed UX cap; Google's real limit is 49).
- `create_space(member_emails, display_name, dry_run)` — named space
  (`spaceType=SPACE`); 1-20 initial members; `display_name` required.
- **Integration test harness** (merged in PR #13 ahead of this release):
  HTTPS and stdio transports now exercised end-to-end in CI; stdout-hygiene
  regression guard covers the full stdio serve path.

### Documented
- `docs/runbook.md` — People API non-self resolution caveats. Non-self
  Workspace users return `email=null, display_name=null` in practice;
  affects `remove_reaction`'s filter path and `sender_email` nullability
  throughout the read-side tools.

### Internal
- `ChatClient.create_dm` is now a thin delegate over an internal
  `_setup_space` + pure `_build_setup_space_body` helper. `displayName`
  is included in the request body only when `space_type == "SPACE"`;
  Google 400s otherwise.

## [0.2.1] - 2026-04-20

Patch release — release-infrastructure improvements, ops hygiene, and a
GHCR description fix. No application-level or tool-surface changes.

### Fixed
- GHCR package page now displays the repository description. The
  multi-arch index was missing the
  `org.opencontainers.image.description` annotation (labels only land
  on per-arch image configs, not on the index GHCR reads). Release
  workflow now emits annotations at both manifest and index level.

### Changed
- **Release builds skip QEMU.** `release.yml` runs a matrix of native
  per-arch jobs (`ubuntu-latest` for amd64, `ubuntu-24.04-arm` for
  arm64) with push-by-digest + a dedicated `docker-merge` job that
  assembles the manifest list. Wall-clock: ~5-8 min → ~3-4 min per
  release.
- **`compose.yml` defaults to the published image**
  (`ghcr.io/mmedum/google-chat-mcp:0.2`). `docker compose up -d` from
  a fresh clone pulls the release artefact instead of rebuilding.
  Commented `build:` block kept for local dev.
- **Gitleaks scope narrowed to the PR/push diff** (was: full history
  on every CI run). PR-iteration scans go from ~5 min (hitting the
  timeout) to <30 s.
- **Dependabot** now covers `uv` (pyproject + uv.lock) and `docker`
  (base images) alongside the existing `github-actions` ecosystem.

### Security
- Release workflow verifies SBOM + provenance attestations landed on
  the multi-arch index after push. Catches silent regressions in
  buildx referrer-following rather than shipping un-attested images.

### Added
- `CONTRIBUTING.md` at repo root; `.github/pull_request_template.md`;
  issue forms for bug reports and feature requests; issue-config that
  redirects security reports to GitHub Security Advisories.
- README badges for CI status, latest release, container image,
  license, and Python version.

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

[Unreleased]: https://github.com/mmedum/google-chat-mcp/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/mmedum/google-chat-mcp/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/mmedum/google-chat-mcp/releases/tag/v0.2.0
