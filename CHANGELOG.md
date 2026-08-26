# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- **Dependencies.** `fastmcp` 3.4.6 → 3.4.7, `ty` 0.0.69 → 0.0.73, `ruff`
  0.16.2 → 0.16.3, `pre-commit` 4.6.1 → 4.6.2. In CI, `setup-uv` 9 → 10 and
  `setup-buildx-action` 4.2 → 4.3.

  fastmcp 3.4.7 is a security release for the HTTPS transport: it fixes client
  assertion validation for OAuth proxy deployments served at a bare origin.
  The declared `fastmcp ~= 3.2` range is unchanged, so this moves the lockfile
  and the image, not the package metadata.

### Fixed
- **Docker images stopped picking up Debian security updates.** The runtime
  stage runs `apt-get upgrade`, but nothing in that layer's hash changes when
  Debian publishes a fix, so a warm build cache replayed a stale package set.
  Images shipped `util-linux` 2.41-5 with four HIGH CVEs (CVE-2026-53612
  through CVE-2026-53615) for as long as the fix sat unapplied. The stage now
  rebuilds every time, on CI and on release.

- **Released images were never scanned.** The release workflow had no Trivy
  step, and CI only scans the architecture it runs on — so the arm64 half of
  every published image had been scanned by nothing. Both architectures are
  now scanned before the release tags are applied.

- **`apt-get upgrade` could skip a security update and still report success.**
  It holds back any upgrade needing a new package pulled in. Now uses
  `--with-new-pkgs`.

## [1.4.1] - 2026-08-09

Maintenance only — no behaviour changes, no tool changes. Nothing here forces
an upgrade. Take it if you want the refreshed dependency set: `structlog` moves
to 26.1.0 in the package metadata, and the published image is rebuilt on uv
0.12.

Patch rather than minor: the twenty-one tools and three resources are
untouched, log output is byte-identical (the processor chain in
`src/observability.py` is unchanged and stable across the structlog bump), and
no configuration key was added, renamed, or given a new default.

Docker users on the README's quickstart should note the fix below — the compose
file had been pinning a 0.2 image, and that is corrected on `main` regardless
of this tag.

### Changed
- **Dependency refresh.** `structlog` 25.5.0 → 26.1.0 and `ty` 0.0.31 → 0.0.69,
  plus a relock that moved `starlette` 1.5.0 → 1.6.0 and `virtualenv` 21.7.2 →
  21.7.3. The Docker builder's uv image goes 0.11 → 0.12, and
  `pypa/gh-action-pypi-publish` moves to v1.14.2.

  structlog numbers releases by year, so 26.1.0 is not a semver major. Its one
  behavioural change here is that the module-global registry mapping output
  files to write locks became a `WeakKeyDictionary`, so a file no longer stays
  reachable for the life of the process just because a logger once wrote to it.
  Our sinks are unaffected either way — `PrintLogger` still holds its file
  strongly, and both production callers pass `sys.stdout` or `sys.stderr`. The
  release notes describe this as the loggers holding weak references to their
  files; the code does not, and it is worth reading rather than trusting if
  this ever looks load-bearing.
- **`ty` 0.0.31 → 0.0.69 stranded 12 of the 15 `ty: ignore` directives.** The
  newer checker no longer reports `missing-argument` on Pydantic model
  construction, nor the three `invalid-argument-type`/`unknown-argument` cases
  in `src/`. `unused-ignore-comment` is only a warning but still exits 1, so
  the dead suppressions would have failed CI; they are removed and three remain
  load-bearing. Four `# type: ignore[...]` comments in the mypy spelling went
  with them — there is no mypy in this project, and ty ignores the coded form,
  so they had never suppressed anything.
- `ci.yml` reads the uv version from one `UV_VERSION` env key instead of
  repeating it across four jobs, and it now tracks the Dockerfile's uv tag.
  Dependabot's `github-actions` ecosystem bumps a `uses:` SHA but never a
  `with:` input, so those four literals were a pin nothing watched — CI had
  been resolving the lockfile on a different uv minor than the image is built
  with.

### Fixed
- **The Docker quickstart deployed a 0.2 image.** `compose.yml` and the
  README pinned `ghcr.io/mmedum/google-chat-mcp:0.2` while `release.yml` had
  been publishing `1.4`, `1.4.0` and `latest` — the tag had not moved in five
  minor releases, so anyone following "docker compose up -d" in the README got
  a server from the 0.2 era. Both now pin `:1.4`, which tracks patches within
  the current minor without ever jumping a major on its own.

  The release checklist in `CONTRIBUTING.md` never mentioned this pin, which is
  how it rotted. It does now, and `ci.yml` fails when the compose tag disagrees
  with the newest CHANGELOG heading, so a forgotten bump breaks the release
  commit instead of shipping a stale quickstart.
- Trivy pinned to v0.73.0 (was v0.70.0). The container scan was three minors
  behind, on the job that gates the image.

### Security
- **The gitleaks version was written in two places and only one was watched.**
  The `.pre-commit-config.yaml` rev sat on v8.21.2 while upstream reached
  v8.30.1 — nine releases of detection rules — because Dependabot covered
  `github-actions`, `uv` and `docker` but not `pre-commit`. Worse, the copy
  that actually gates merges is `ci.yml`'s `gitleaks` job, which curled the
  tarball by literal version: invisible to *every* ecosystem, since no
  ecosystem parses a URL inside a `run:` step. Bumping only the hook would have
  left the gate on the old ruleset while appearing fixed.

  Both are now current and there is one source of truth: the CI job reads the
  rev out of `.pre-commit-config.yaml`, and a new `pre-commit` ecosystem in
  `.github/dependabot.yml` moves that rev. The upgraded scan passes clean over
  the whole tree.
- `docs/security.md` gains an **Accepted advisories** section, starting with
  the `diskcache` pickle advisory (CVE-2025-69872 / PYSEC-2026-2447) that
  `ci.yml` has suppressed since April. The suppression's comment cited a
  "project risk assessment" that had never been written down anywhere; now it
  points at one. No dependency changed — this records reasoning that was
  already load-bearing.

## [1.4.0] - 2026-08-08

Upgrade if you run the stdio transport. Two warnings appeared on stderr on
every session while meaning nothing, in the one channel `docs/runbook.md` tells
stdio operators to watch — stdio has no `/metrics` endpoint, so stderr is the
only place degraded People lookups show up. Routine noise there teaches people
to ignore it.

Minor rather than patch: no tool is removed, renamed or reshaped, but
`tokens.json` changes what it records, a scope check that had never fired
starts enforcing, and denied calls start producing audit rows. See the rollback
caveat below before downgrading.

### Fixed
- **Two warnings on stderr that never meant anything.** Both fired on `serve`,
  not just `doctor`, so they reached every stdio user's client log on every
  session.
  - `UserWarning: directory "/run/secrets" does not exist` — that path exists
    only in the Docker deployment, but `secrets_dir` was passed unconditionally.
    It is now passed only when the directory is really there.
  - `Not all requested scopes were granted by the authorization server, missing
    scopes email, profile` — Google accepts `email` and `profile` on the
    authorization request and reports them back as their `userinfo.*` URLs.
    google-auth compares the two lists verbatim on every token refresh and
    warned about scopes that had in fact been granted. The aliases are now
    translated to the URL form Google echoes back, so the mismatch is gone
    rather than muted: a scope that was genuinely withheld still warns.
- **`tokens.json` recorded the requested scopes under the key `granted_scopes`.**
  `credentials.scopes` is only ever the list we asked for — it is never
  reconciled against the token response — so the stored set always contained
  every scope in `GOOGLE_OAUTH_SCOPES`. The pre-flight scope check in
  `invoke_tool` compares against that set, which meant it could never fire, and
  a scope the user had declined surfaced only as an upstream 403. Login now
  stores `credentials.granted_scopes`, what Google actually returned.
  - Existing `tokens.json` files keep working and need no re-login: the alias
    translation above is applied when the stored scopes are read, so the refresh
    warning stops immediately. The pre-flight check starts reflecting reality at
    the next `google-chat-mcp login`.
  - A missing, empty or malformed stored scope list is now reported as *unknown*
    rather than as *nothing granted*. The latter made the pre-flight check
    reject every tool call locally; it now defers to Google's 403, matching the
    HTTPS transport. A hand-edited `"granted_scopes": "openid email"` (a string
    where a list belongs) is treated the same way instead of being compared one
    character at a time.
  - Every refresh response now updates the stored scopes. A `tokens.json`
    written before this fix corrects itself on the next refresh rather than
    waiting for a re-login, and a scope revoked in Google account settings stops
    being honoured locally.
  - **Rollback caveat:** if Google returns no scope list at login, this version
    stores an empty one. Version 1.3.0 and earlier read that as *nothing
    granted* and refuse every tool call. Re-run `google-chat-mcp login` after
    downgrading.
- **A locally-denied tool call left no trace.** The pre-flight scope check
  raised before the rate limiter, the latency metric and the audit write, so a
  denial recorded nothing — while the identical denial caught as an upstream 403
  was audited as `missing_scope`. The check now runs inside the instrumented
  block and records the same way. It had never fired in a release, so no
  existing audit history is affected.

## [1.3.0] - 2026-08-08

Upgrade if you read messages or members. On 1.2.0 and earlier, `get_messages`,
`get_thread` and `list_members` returned an **empty list** — not an error —
whenever Google's People API refused an email lookup, which happens whenever
`directory.readonly` was never granted. An agent reads that as "this space is
empty" and says so confidently. Minor, not major: no tool removed or renamed,
no input shape changed.

No data migration, and no existing table is touched: the retention fix is in
the query. The one schema change is a new `schema_migrations` bookkeeping
table, created automatically on first start.

### Fixed
- **`get_messages`, `get_thread` and `list_members` returned an empty list
  whenever the People API refused a lookup.** Email resolution runs after the
  Chat API call, and a 403 (`directory.readonly` not granted), 429, 5xx or a
  transport timeout propagated out of the per-row enrichment — where each of
  these gathers with `return_exceptions=True`, so the row was dropped. With
  every lookup failing, every row was dropped and the tool returned `[]`. A
  calling model reads that as *this space is empty*, not *lookups are broken*:
  the same silent-emptiness failure as the `markupSyntax` outage, where
  `search_messages` reported zero matches over a scan that never parsed a row.
  - `get_message` returns a single message rather than a list, so the same
    failure surfaced there as a hard `ToolError` naming the Chat API — wrong
    and misleading, since the message itself had been fetched successfully.
  - `remove_reaction`'s `(message, emoji, user_email)` shape resolves each
    reactor's email the same way, and a failed lookup made it skip that reactor
    and report `removed: false` — which the tool documents as "already gone",
    so a calling model stops looking. It now raises instead whenever any
    reactor's address failed to resolve: an unresolved reactor means a match
    cannot be ruled out, so an absence must not be reported as established.
  - Enrichment now degrades to `email: null` and keeps the row, which is what
    `docs/runbook.md` already described. New `mcp_people_lookup_failures_total`
    counter makes the degradation visible; the previous behaviour had no metric
    at all.
- **A `Retry-After` header could park a tool call for hours.** `Retry-After` was
  honoured verbatim, bypassing the 30s backoff ceiling that applied to the
  computed path, so a 429 carrying `Retry-After: 3600` slept for an hour per
  attempt — up to three times in one call, with no output and no error. It is
  now bounded on both sides by the exponential schedule: capped at 30s, and
  floored at the attempt's base delay so that `0` or a negative value cannot
  spend every attempt in microseconds against an upstream that just asked for
  backpressure. Non-numeric values (the header also permits an HTTP-date) fall
  back to exponential backoff.
- **`prune_audit_log` deleted up to 24h more history than the retention window.**
  The cutoff was bound as `datetime.isoformat()` while rows store SQLite's
  `CURRENT_TIMESTAMP` format. `TIMESTAMP` carries NUMERIC affinity, so the two
  were compared lexicographically — and `T` (0x54) sorts after the stored space
  (0x20), making every row that shared the cutoff's calendar date compare as
  older. Under the default 90-day retention each daily prune silently discarded
  an extra day of audit records. The cutoff is now formatted to match the
  stored representation, which also keeps `idx_audit_log_timestamp` usable.
- **A failing audit write replaced the tool's actual result.** The audit row is
  written in `invoke_tool`'s `finally`, and an exception escaping a `finally`
  supersedes the value or exception in flight — so a locked or full SQLite file
  turned a completed call into an unrelated `OperationalError`. For a write
  tool that is the duplicate hazard again: the write landed, the caller saw a
  database error, and a retry would repeat it. Audit failures are now logged
  (`audit_write_failed`), counted (`mcp_audit_write_failures_total`) and
  swallowed — fail-open, but not silently, since nothing else would reveal that
  `audit_log` had stopped recording.
- `get_message` now normalises `last_update_time` to UTC, matching `timestamp`.
  A naive value from Google previously passed through untouched.
- **`doctor` reported a clean bill of health for shapes it never sampled.** It
  checked spaces, messages and memberships only, so drift in the reaction
  models (`list_reactions`, `add_reaction`) or the OIDC payload (`whoami`)
  passed unnoticed — the failure `doctor` exists to catch, in the tool meant to
  catch it. It now samples both, and honours `GCM_CHAT_API_BASE` /
  `GCM_PEOPLE_API_BASE` instead of always checking Google's production URLs
  regardless of where the server points — via `Settings`, so the
  `*.googleapis.com` restriction that protects the access token still applies.
  `--spaces 0` is rejected rather than issuing a request with `pageSize=0`.
- **`doctor` reported schema drift on every run.** `_UserInfoResponse` did not
  model `given_name`, `family_name`, `locale` or `hd`, which Google returns for
  any account with the `profile` scope and any Workspace account respectively.
  A health check that always fails is the same as no health check. Those claims
  are now declared, and `userinfo.email` is a plain `str` so an address
  pydantic rejects cannot fail `whoami` for the account it belongs to.
- **`doctor` crashed instead of reporting an unreachable upstream.** Its guard
  covered parsing, not fetching, so a missing `openid` scope or a message
  deleted mid-run killed the process — the same exit code as real drift, with
  the findings already collected thrown away. Every fetch now degrades to a
  reported problem, as does a revoked or expired refresh token, which no fetch
  guard could have covered because it fails before the first request.
  - Exit codes now distinguish the cases: `0` clean, `1` schema drift, `2`
    could not check (auth failed, or an upstream call did not answer). "We
    could not look" was previously printed under a `SCHEMA DRIFT` banner with
    remediation telling the operator to edit `src/models.py`.
- A `Retry-After` of `nan` or `inf` reached `asyncio.sleep`, which rejects
  non-finite delays on Python 3.13+ and corrupts the timer heap before that.
  `float()` accepts both, so the value is now range-checked.
- `google-chat-mcp logout` left `audit_pepper` on disk. No stored data was
  affected — stdio sets `audit_hash_user_sub=False`, so the pepper has never
  been read and audit rows hold the raw sub either way — but logout should
  remove every local secret, not only the two that decrypt tokens. The SQLite
  database is still kept: it holds the audit log and email cache, which are
  records rather than secrets.

### Changed
- People API resolution is consolidated into a single `resolve_person_cached`
  that owns the cache lookup, the fetch and the degrade. The cache-check →
  fetch → cache-put sequence had been copy-pasted three ways, which is why the
  fix above needed applying twice and still missed `remove_reaction`.
- Message enrichment resolves one lookup per *unique sender* rather than per
  message. A 50-message thread between three people previously issued 50
  concurrent People API calls, since all of them missed the cold cache before
  any result was written back.
- The tool name reaches enrichment through a `current_tool` context variable set
  by `invoke_tool`, rather than being passed down by each handler. Every handler
  previously spelled its own name twice — once to `invoke_tool`, once to the
  enrichment call — with nothing keeping the two in sync.
- **Migrations now run once instead of on every startup.** Applied filenames are
  recorded in a new `schema_migrations` table (created automatically; no
  existing table is touched). Previously every `.sql` file was re-executed on
  each boot, which forced them all to be idempotent and made a migration that
  *transforms* data impossible to express. Every migration must still be
  replay-safe — see `Database.migrate`.
- **`search_people` failed outright on a personal contact with an unusual
  address.** The `CONTACTS` source returns the caller's own address book —
  cards typed by a human, not addresses issued by Workspace — so one saved as
  `bob@nas.local`, `admin@router` or with a trailing dot failed
  `PersonHit.email`'s strict `EmailStr` and turned the whole call into
  `Internal error.`, hiding every other hit. Email fields on tool *results* are
  now typed `str`: `ChatMessage.sender_email`, `MessageDetails.sender_email`,
  `Member.email`, `PersonHit.email` and `WhoamiResult.email`.
  - Only `PersonHit` had a reachable failure; the other four resolve through
    the Workspace directory or the OIDC `/userinfo` claim, which in practice
    hold addresses on verified, owned domains. They are relaxed for consistency
    — and because Google documents no format contract for the field we read
    (`EmailAddress.value` is described only as "The email address"), so
    `EmailStr` on our side asserted a guarantee the upstream never made. Note
    also that the Chat API's `User` object carries no email at all; every
    address here is a People API or OIDC lookup, so Chat guarantees nothing
    about them either.
  - Validating these on the way *out* protected nobody: the client receives a
    JSON string either way. Same reasoning as `extra="allow"` on the response
    models — an upstream we do not control must not be able to turn its own
    data into our outage. `EmailStr` is unchanged on *inputs*, where rejecting
    a hallucinated address from a calling model is the point.
  - Two visible consequences: the output schema loses `"format": "email"` on
    those five fields (advisory in JSON Schema), and addresses are no longer
    silently normalised — `EmailStr` had been rewriting
    `Alice <alice@example.com>` to `alice@example.com` and stripping
    surrounding whitespace.
- **The directory cache is read once per call, not once per row.** A page of
  senders or members resolves concurrently, so the per-row read opened one
  SQLite connection — and one OS thread — per row, every one of them missing
  before any could write back. At the 200-member cap that was 200 of each to
  read rows sitting in a single file: measured 74ms and 202 threads, now 7ms
  and none. A warm cache also now costs zero People API requests, where before
  it cost one per row on the first call.

## [1.2.0] - 2026-08-08

Upgrade immediately if you are on 1.0.x: every message- and membership-touching
tool is broken against the live Google Chat API, and no client-side workaround
exists. Writes are the dangerous case — they succeeded and still reported
`Internal error.`, so retries may have posted duplicates.

### Fixed
- **Every message- and membership-touching tool was broken against the live
  Chat API.** Google added `markupSyntax` (on messages) and `affiliation` (on
  memberships); with `extra="forbid"` on the response models, every message and
  every membership failed validation. Both fields are now accepted as optional
  `str`.
  - Reads: `get_messages`, `get_message`, `get_thread` and `list_members`
    raised on every call in every space; `search_messages` returned zero
    matches over a full scan.
  - **Writes, and this is the damaging part: the write succeeded and the tool
    still reported `Internal error.`** `send_message` and `update_message`
    parse the create/patch response, which carries `markupSyntax` — so the
    message was posted or edited, then validation threw. A caller seeing an
    error would reasonably retry, posting twice. `add_member` parses the same
    membership shape and has the same hazard. If you ran 1.0.x recently, check
    affected spaces for duplicates.
  - Space-shaped responses never drifted, so `list_spaces`, `get_space`,
    `create_space`, `create_group_chat`, `update_space` and `whoami` kept
    working — which is what made this look like a permissions or network
    problem rather than schema drift.
- `search_messages` no longer drops unvalidatable messages silently. It still
  skips them — one bad row must not fail the search — but counts them in the
  new `unparsed` field of the result and logs the first one per call. A
  non-zero `unparsed` means the result is incomplete, which is what made the
  drift above look like an empty space rather than a broken parser.
- Schema drift is now diagnosable from the logs on *every* tool, not just
  `search_messages`. `invoke_tool` names the drifted field paths on the
  `tool_unhandled` event; previously the caller got `Internal error.` and the
  field name reached nobody, because the log chain has no `format_exc_info`.
  Paths only, never values — `str(ValidationError)` embeds `input_value=...`,
  which would carry message content past the key-based log redaction.

- **Write tools no longer fail after the write lands.** `send_message`,
  `update_message`, `add_member`, `create_space`, `create_group_chat`,
  `update_space` and `find_direct_message` parsed a full response model — 8 to
  27 fields — to read one or two identifiers they re-validate anyway. Any drift
  in the other fields turned a completed write into `Internal error.` They now
  read only what they return. If a result genuinely cannot be built, the error
  says the write SUCCEEDED and must not be retried. `create_space` and
  `create_group_chat` were the worst case and went unlisted before: a retry
  makes a second space.
- **`send_message` could post twice on a transient upstream error.** The client
  retries 429/5xx, and a 5xx can arrive *after* Google created the message — so
  the retry posted a second copy. Unrelated to schema drift; it predates all of
  it. Each send now carries a client-assigned `messageId`, generated once per
  call rather than per attempt, so Google rejects the retry as `ALREADY_EXISTS`
  and the client fetches the message that actually landed. Verified live: user
  OAuth accepts `messageId`, a repeat returns 409, and
  `spaces/{space}/messages/{messageId}` reads the message back.
- **Closed enums removed from response models.** `_ChatUser.type`,
  `_ChatSpaceResponse.type`, `_ChatMembershipResponse.state` and `.role` were
  `Literal`s on fields present in every row, so a value Google adds to any of
  those enums was another total outage waiting — the same failure as an added
  field, from a different direction. They are `str` on the wire now and narrowed
  to the closed set at the tool boundary, with anything unrecognised bucketed
  into the existing `*_UNSPECIFIED` member, logged, and counted.

### Added
- **`google-chat-mcp doctor`** — validates live Chat API responses against the
  models using the token already on disk, names any drifted field paths
  (including nested ones, as dotted paths), and exits non-zero, so it works as
  a cron job. Nothing detected drift before a user did; this closes that. No
  shared credential is involved — each deployer checks with their own token.
- `mcp_schema_drift_total{location}` — incremented on every response that
  doesn't match our models, so `rate()` stays non-zero while the models are
  stale rather than self-resolving after the first hit. The matching log line
  is deduped per process; the counter deliberately is not. `location` is a
  model/field path, never a value. Alert on any non-zero rate.

### Changed
- **Chat API response models accept unknown fields instead of rejecting them**
  (`_ChatBase`: `extra="forbid"` → `extra="allow"`). An unknown field is kept,
  logged once per process as `schema_drift` with its `Model.field` location, and
  counted in `mcp_schema_drift_total`. This reverses the rule in
  `docs/architecture.md`, which cost two total outages: Google adds response
  fields without notice — AIP-180 explicitly permits it — and each lands on
  *every* row of its resource, so rejecting them failed every tool at once. The
  detection that rule promised never materialised; both events were found by a
  user. The invariant is now **drift must be observable**, not fatal.

  What still fails loudly: `name`, `sender`, `create_time` and `thread` have no
  defaults, so removing or retyping one raises. Fields that *do* have defaults —
  `text`, `displayName`, `member` — do not: a renamed `text` yields empty
  message bodies and a successful call. The signal there is the new key
  arriving, which fires `schema_drift`, moves the counter, and shows up in
  `doctor`. Observation, not failure — which is why `doctor` has to actually be
  run.

  **Tool I/O keeps `extra="forbid"`.** That side is our own contract, and
  rejecting an unrecognised key from a calling model is a real safety property
  — a misspelled `dry_run` must not silently post for real.
- `SearchMessagesResult` gains an `unparsed: int` field (defaults to `0`, so
  existing callers are unaffected).
- Pinned GitHub Actions bumped to current releases, including two majors:
  `actions/checkout` v6 → v7.0.1 and `astral-sh/setup-uv` v8 → v9.0.0. The only
  breaking change that touches us is setup-uv's `prune-cache` now defaulting to
  false, trading Actions cache usage for less load on PyPI.
- Tool schemas now carry the field descriptions that were already written.
  `_Strict` didn't set `use_attribute_docstrings`, so every attribute docstring
  in `src/models.py` was dev-only commentary — a calling model saw
  `{"type": "integer"}` with no guidance attached, including `space_id`'s
  "the server will NOT search across spaces".

### Security
- Refreshed `uv.lock` against current advisories. CI's `audit` and `container`
  jobs had gone red on `main` without a code change: the lockfile had not moved
  since April, so newly published advisories accumulated against pinned
  versions. Notable fixes: `starlette` 1.0.0 → 1.5.0 (SSRF via UNC paths in
  `StaticFiles`; form-limit DoS), `urllib3` 2.6.3 → 2.7.0 (header leak on
  cross-origin redirect; decompression DoS), `pyjwt` 2.12.1 → 2.13.0,
  `python-multipart` 0.0.26 → 0.0.32, `pyasn1` 0.6.3 → 0.6.4 (parser DoS),
  and `pydantic-settings` 2.14.0 → 2.15.0.
- **FastMCP 3.2.4 → 3.4.6**, which the relock pulled in alongside the advisory
  fixes. Two minor versions: 3.4.0 migrated the auth stack's JWT handling to
  `joserfc`, and upstream repackaged `fastmcp` as a shim over a new
  `fastmcp-slim` distribution. Both transports run on FastMCP, so this is a
  larger change than the rest of the refresh. Verified against a live HTTPS
  deployment — OAuth metadata, RFC 9728 discovery and JWT verification all
  behave — but CI cannot cover it, because the integration test stubs
  `TokenVerifier`.
- `release.yml` now refuses to publish unless `ci` succeeded for the tagged
  commit. A tag is only a pointer, so nothing previously stopped one landing on
  a commit whose CI never ran or ran red — and every job below it writes
  somewhere irreversible, PyPI most of all. Same blind spot that let the
  lockfile sit stale for months, applied to the release path.
- `cryptography` 46.0.7 → 50.0.0, which required widening the `pyproject.toml`
  pin from `~=46.0` to `~=50.0`. Covers a PKCS#7 decryption oracle, exponential
  blowup on chains with duplicate self-signed certificates, a name-constraint
  bypass via wildcard SANs, and the statically linked OpenSSL in the wheels.
- Runtime image no longer ships `pip`. The app runs from `/app/.venv` and never
  installs at runtime, but pip's bundled `_vendor/` tree is scanned as real
  packages — it was the sole source of the `setuptools` 70.3.0 and `msgpack`
  1.1.2 findings, neither of which is a project dependency or fixable from the
  lockfile.

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

[Unreleased]: https://github.com/mmedum/google-chat-mcp/compare/v1.4.1...HEAD
[1.4.1]: https://github.com/mmedum/google-chat-mcp/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/mmedum/google-chat-mcp/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/mmedum/google-chat-mcp/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/mmedum/google-chat-mcp/compare/v1.0.1...v1.2.0
[1.0.1]: https://github.com/mmedum/google-chat-mcp/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/mmedum/google-chat-mcp/compare/v0.4.0...v1.0.0
[0.4.0]: https://github.com/mmedum/google-chat-mcp/compare/v0.3.3...v0.4.0
[0.3.3]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.1...v0.3.3
[0.2.1]: https://github.com/mmedum/google-chat-mcp/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/mmedum/google-chat-mcp/releases/tag/v0.2.0
