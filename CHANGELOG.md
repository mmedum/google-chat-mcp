# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.2] - 2026-04-21

Closes the v0.3.x train: seven new tools across space creation, membership
mutation, people resolution, and message lifecycle. Ships everything from
the unreleased [0.3.0] (space creation) and [0.3.1] (membership + people
search) cuts as a single version — no intermediate tags. **Three new OAuth
scopes** total (two sensitive, one restricted); one re-consent round
covers all three.

### Added
- `create_group_chat(member_emails, dry_run)` — unnamed multi-person DM
  (`spaceType=GROUP_CHAT`). `member_emails` excludes the caller; 2-20
  members (self-imposed UX cap; Google's real limit is 49). No new scope
  — uses the existing `chat.spaces.create`.
- `create_space(member_emails, display_name, dry_run)` — named space
  (`spaceType=SPACE`); 1-20 initial members; `display_name` required.
  Same scope as above.
- `add_member(space_id, user_email, dry_run)` — invite a user to a space
  via `spaces.members.create`. In practice Google returns HTTP 200 with
  the existing membership record on duplicate adds (idempotent-by-nature);
  the older 409 `ALREADY_EXISTS` path is still wrapped as a `ToolError`
  for Workspace editions that surface it. See the runbook for the
  operator-facing framing.
- `remove_member(membership_name, dry_run)` — delete a membership by
  full resource name. Idempotent: double-delete returns `removed=false`
  on 404 NOT_FOUND or 403 PERMISSION_DENIED. Missing-scope 403s are
  excluded from the idempotent path so callers still see the re-auth
  prompt. There is no email-filter shape — non-self People API
  resolution is unreliable (see the runbook's People API caveats), so
  an email-based lookup would silently miss the target.
- `search_people(query, limit, sources)` — hybrid lookup over Workspace
  directory (`people:searchDirectoryPeople`) + caller's contacts
  (`people:searchContacts`). Runs both sources in parallel via
  `asyncio.gather` by default; sources tagged per hit. Workspace-profile
  hits back-fill the `DirectoryCache` so later `get_messages` /
  `list_members` resolve `sender_email` without another People API call.
  Contact-ID hits surface but do NOT back-fill — different namespace,
  would poison `users/{id}` lookups.
- `update_message(message_name, text, dry_run)` — replace the text of a
  message you previously sent (`spaces.messages.patch` with
  `updateMask=text`). Text-only edits — cards / attachments stay
  untouched. 1-4096 chars (cap mirrors `send_message` for project-wide
  consistency).
- `delete_message(message_name, dry_run)` — delete a message by full
  resource name. Idempotent: double-delete returns `deleted=false` on
  404 / non-scope 403 (mirrors `remove_member` shape). Missing-scope
  403s still raise.
- **Integration test harness** (merged in PR #13 ahead of this release):
  HTTPS and stdio transports now exercised end-to-end in CI; stdout-
  hygiene regression guard covers the full stdio serve path.

### Changed (breaking for deployers)
- **OAuth scopes**: three new entries in `GOOGLE_OAUTH_SCOPES`.
  - `https://www.googleapis.com/auth/chat.memberships` (sensitive tier) —
    `add_member` + `remove_member`.
  - `https://www.googleapis.com/auth/contacts.readonly` (sensitive tier) —
    `search_people` consumer-Gmail fallback.
  - `https://www.googleapis.com/auth/chat.messages` (**restricted tier**) —
    `update_message` + `delete_message`. Re-introduces the umbrella
    scope that v0.2.0 explicitly dropped — Google's narrower
    `.create` / `.readonly` scopes don't authorize patch + delete.

  Every HTTPS deployer updates the OAuth consent screen; every user
  re-consents on next MCP call. Stdio users re-run `google-chat-mcp logout &&
  google-chat-mcp login`.
- **Internal Workspace apps (`External → Internal` in the OAuth consent
  screen) skip Google's verification entirely** — both sensitive AND
  restricted tiers. Deployers publishing internally — the primary
  audience — don't file paperwork; just declare the scopes. Public-
  published apps with the `chat.messages` scope need annual CASA review;
  the runbook covers the deployer trade-off.

### Documented
- `docs/runbook.md`: new "People API non-self resolution caveats" section
  — non-self Workspace users return `email=null, display_name=null` in
  practice; affects `remove_reaction`'s filter path and `sender_email`
  nullability throughout the read-side tools.
- `docs/runbook.md`: new sections covering `search_people` operational
  quirks — the External directory sharing admin toggle that gates
  `searchDirectoryPeople`, the consumer-Gmail `CONTACTS`-only fallback,
  and the `add_member` idempotent-by-nature behavior (HTTP 200 with
  existing record rather than 409). Runbook is the authoritative source
  for admin-console paths; see those entries for exact navigation.
- `docs/runbook.md`: new section on the `chat.messages` restricted-tier
  scope — CASA-review trade-off for public-published apps; Internal-app
  deployers skip.
- `docs/gcp-setup.md`: updated scope list with all three v0.3.x additions.

### Internal
- `ChatClient.create_dm` is now a thin delegate over an internal
  `_setup_space` + pure `_build_setup_space_body` helper. `displayName`
  is included in the request body only when `space_type == "SPACE"`;
  Google 400s otherwise.
- `DirectoryCache.put_many` + `workspace_user_id` helper — bulk writer
  keyed on `users/{id}` with a regex gate that filters contact-ID
  resource names before any cache write.
- `ChatClient._patch` helper alongside `_post` / `_delete`; pure
  `_build_update_message_body` builder for dry/real parity on
  `update_message`.

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

[Unreleased]: https://github.com/mmedum/google-chat-mcp/compare/v0.3.2...HEAD
[0.3.2]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.1...v0.3.2
[0.2.1]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/mmedum/google-chat-mcp/releases/tag/v0.2.0
