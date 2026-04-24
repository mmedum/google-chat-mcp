# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.1] - 2026-04-24

Packaging-only release. No API changes.

### Added
- PyPI distribution via [Trusted Publishing](https://docs.pypi.org/trusted-publishers/)
  (OIDC, no API token). Install with `uv tool install google-chat-mcp`
  or `pipx install google-chat-mcp`. Git-install
  (`uv tool install git+https://github.com/mmedum/google-chat-mcp@vX.Y.Z`)
  continues to work for pre-release / dev installs.
- `[project.urls]` and PyPI `classifiers` in `pyproject.toml` so the
  project page renders with Homepage, Issues, Changelog links and
  faceted-search metadata (Production/Stable, Python 3.12–3.14,
  Communications::Chat, Typed).
- `keywords` in `pyproject.toml` for PyPI search.

### Changed
- `license = { text = "Apache-2.0" }` migrated to PEP 639 SPDX form
  `license = "Apache-2.0"` in `pyproject.toml`; `license-files` added
  so the wheel ships `LICENSE` verbatim. `[build-system] requires`
  tightened to `hatchling>=1.27` (the first version that emits
  Metadata 2.4 with the SPDX license expression correctly).
- README's stdio install section now leads with
  `uv tool install google-chat-mcp` (PyPI) and keeps the git-install
  URL as a secondary pre-release path.

## [1.0.0] - 2026-04-21

Initial stable release. The tool surface (21 tools + 3 resources), I/O
shapes, and scope set graduated from v0.4.0 are now semver-stable per
the policy in [README](README.md#versioning-and-support): breaking
changes get a major-version bump and at least one minor-version
deprecation warning before removal.

No code changes from v0.4.0 — this is a tag-only release. The v0.4.0
artifact (Docker image, SBOM, SLSA provenance, wheel) is the same
binary you get at v1.0.0; the version string changes and the contract
is now stable.

## [0.4.0] - 2026-04-21

Closes the last high-value gap in the per-user-OAuth Chat API surface
(`update_space`) and widens Python support for mainstream deployer
installs. Ships alongside two scope-correctness fixes surfaced by a
pre-release audit, a Code of Conduct, and a versioning policy — the
final pre-1.0 maturity pass.

### Added

- **`update_space`** — rename a space or edit its description via
  `spaces.patch`. Accepts any combination of `display_name` (1-128 chars)
  and `description` (≤150 chars); at least one must be set. Supports
  `dry_run=true` for preview-without-post, same parity contract as
  `send_message` / `update_message`. Tool surface is now 21 tools.
  (`src/tools/update_space.py`, `src/chat_client.py::update_space`,
  `src/models.py::UpdateSpaceInput` + `UpdateSpaceResult`).

### Changed (breaking for deployers)

- **New `chat.spaces` umbrella scope required** for `update_space`.
  Google's `spaces.patch` accepts only the umbrella under user OAuth —
  the granular `chat.spaces.create` / `chat.spaces.readonly` we already
  hold do **not** cover patch. Existing deployers must re-consent (stdio:
  `google-chat-mcp logout && google-chat-mcp login`; HTTPS: re-consent
  in your MCP client) or `update_space` will 403 with the scope-named
  re-auth prompt. `chat.spaces` is in Google's **restricted tier**,
  joining `chat.messages` (added in v0.3.2) — Internal Workspace apps
  skip verification; Externally-published apps' existing CASA review
  covers the new scope (single CASA per Cloud project, no additional
  fee). See `docs/runbook.md` for the opt-out path.
- **Python 3.12+ required.** `requires-python` widened from
  `>=3.14,<3.15` to `>=3.12,<3.15`; `[tool.ruff] target-version` lowered
  to `py312` to match. CI exercises 3.12 / 3.13 / 3.14 in a matrix. The
  shipped Docker image stays on `python:3.14-slim`; the widening
  benefits `uv tool install` on mainstream distros (Ubuntu 24.04 ships
  3.12 default; RHEL 9 + Debian 13 have 3.12/3.13 available).

### Fixed

- **`find_direct_message` no longer masks missing-scope 403s, and now
  names the correct scope.** Pre-fix, the create-on-miss path
  (`spaces.setup`, requires `chat.spaces.create`) wrapped every
  `ChatApiError` into a generic "is the user in your Workspace
  directory?" `ToolError` — hiding the scope-specific re-auth prompt a
  deployer without `chat.spaces.create` would need. Now the handler
  detects missing-scope on the create path and raises a `ToolError`
  naming `chat.spaces.create` directly, rather than relying on
  `invoke_tool`'s generic wrapper (which would have named the
  pre-flight tag `chat.spaces.readonly` — wrong scope, misleading
  re-auth prompt). (`src/tools/find_direct_message.py`).
- **`list_reactions` scope tag corrected** from
  `chat.messages.readonly` (restricted tier) to `chat.messages.reactions`
  (sensitive tier). The narrower scope also permits
  `spaces.messages.reactions.list`, and using it in the missing-scope
  re-auth prompt keeps deployers who declined the restricted umbrella
  inside the sensitive tier. No granted-scope change.
  (`src/tools/list_reactions.py`).

### Documented

- **`CODE_OF_CONDUCT.md`** — Contributor Covenant 3.0 with the default
  enforcement ladder; reports go to the maintainer via email or GitHub
  security advisory.
- **`README.md`** gains a "Versioning and support" section: tool names
  and I/O shapes are semver-stable from v1.0; breaking changes get a
  major bump and at least one minor-version deprecation warning.
- **`docs/runbook.md`** — restricted-tier scope section now covers both
  `chat.messages` and `chat.spaces`; adds a "sender_email / display_name
  are null on non-self users" section documenting the People API
  limitation that was previously implicit.
- **`docs/gcp-setup.md`** — scope paste-list includes `chat.spaces`;
  restricted-tier note updated.

### Internal

- Renamed `_is_missing_scope_error` → `is_missing_scope_error`
  (`src/tools/_common.py`). The function was already imported by four
  modules across the codebase; dropping the leading underscore makes
  the cross-module import an explicit public API instead of a
  private-namespace reach.

## [0.3.3] - 2026-04-21

Security release. Closes 2 High and 5 Medium findings from a comprehensive
security audit, plus a long tail of low-severity hardening. Subsumes the
unreleased v0.3.2 feature content; `0.3.3` is the first tagged artifact
for the entire v0.3.x train (7 new tools + 3 new OAuth scopes). See
`docs/security.md` for the threat model and the full set of
security-relevant invariants.

### Security — High

- **`GCM_CHAT_API_BASE` / `GCM_PEOPLE_API_BASE` token-exfil closed**
  (`src/config.py`). Pre-fix, these env-overridable Settings fields
  accepted plain `http://` and any host — an attacker with env-write on
  the host could redirect every Chat API call to themselves and capture
  the user's Google access token from the `Authorization` header. Now
  the validator requires `https://*.googleapis.com` unless the explicit
  `GCM_DEV_MODE=1` env gate is set (integration-test use only).
- **Path-traversal in resource-name regexes closed** (`src/models.py`).
  The shared `_ID = r"[A-Za-z0-9._-]+"` admitted bare `..` segments;
  httpx normalized them via RFC 3986 before sending, so
  `delete_message("spaces/T/messages/..")` resolved to
  `DELETE /v1/spaces/T` — wrong-resource call with the audit log
  recording the intended target. Tightened to require ≥1 alphanumeric
  per segment.

### Security — Medium

- **`emoji` parameter constrained** to block AIP-160 filter injection in
  `remove_reaction`'s lookup path (`src/models.py`). Pre-fix, `"` in the
  emoji could break out of `emoji.unicode = "{value}"` and broaden the
  filter to delete the wrong reaction.
- **`allowed_client_redirects` validator tightened** (`src/config.py`).
  Rejects bare-TLD hosts, multi-`*` wildcards, and `*` in TLD position;
  preserves the documented single `*.subdomain` pattern.
- **Weak-key rejection at config-parse** (`src/config.py`).
  `jwt_signing_key.min_length=32`, `fernet_key` exactly 44 chars (real
  Fernet shape). Catches operator typos before mid-OAuth-flow crashes.
- **`DirectoryCache.put` gated on `users/{numeric}` shape**
  (`src/storage.py`). The single-write path now silently drops bot/app/
  contact-derived IDs — matches the bulk `put_many` invariant the
  docstring already promised.
- **Concurrent-writer race on `fernet.key` / `audit_pepper` closed**
  (`src/stdio.py`). Pre-fix, two `login` invocations could both observe
  "no key", both generate, and both write — losing one user's session
  silently. Replaced with `tempfile.mkstemp` + `os.link` for atomic
  exclusive create-or-read.

### Security — Low (defense-in-depth)

- Stdio `cmd_login` hard-fails when `user_sub` is unresolvable from
  both id_token and OIDC `/userinfo` — drops the literal `"stdio-user"`
  fallback that would have polluted audit logs.
- Stdio resolver pre-flight scope check (`src/tools/_common.py`):
  `granted_scopes` from tokens.json compared against `required_scope`
  before the upstream API call. Matches HTTPS's reactive-via-403 shape.
- `GCM_CONFIG_DIR` outside `~/` requires `GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME=1`
  — closes the silent chmod-0700 footgun.
- Stdio Fernet/JWT placeholder constants made deterministic-public (no
  longer `Fernet.generate_key()` per import) — `_STDIO_FERNET_PLACEHOLDER`
  and `_STDIO_JWT_PLACEHOLDER` are recognizable literals so any
  accidental real use fails loudly.
- `_atomic_write_bytes` now opens the temp with `O_CREAT|O_TRUNC` at
  final perms in one syscall — closes the create-then-chmod window.
- `asyncio.Lock` around stdio resolver's refresh+save — serializes
  Google token rotation across concurrent tool calls.
- `chat_client._request` rejects 3xx responses (was misclassified as
  success and returned `{}`).
- Log redaction walks nested dicts — `logger.info("x", payload={"access_token": ...})`
  no longer leaks plaintext.

### Added

Seven new tools across the v0.3.x train, three new OAuth scopes (two
sensitive, one restricted); one re-consent round covers all three.
Feature content unchanged from the unreleased v0.3.2 cut.

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

[Unreleased]: https://github.com/mmedum/google-chat-mcp/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/mmedum/google-chat-mcp/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/mmedum/google-chat-mcp/compare/v0.4.0...v1.0.0
[0.4.0]: https://github.com/mmedum/google-chat-mcp/compare/v0.3.3...v0.4.0
[0.3.3]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.1...v0.3.3
[0.2.1]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/mmedum/google-chat-mcp/releases/tag/v0.2.0
