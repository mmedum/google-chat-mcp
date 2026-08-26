# Security model

This document captures the threat model, trust boundaries, and the
security-relevant invariants that hold across the codebase. It's the
authoritative reference for "is this safe?" questions raised in PR
review or by deployers evaluating the project.

Both deployment modes (stdio and HTTPS — see the README for the decision
helper) are covered below; each has its own row in the trust-boundary
table and its own set of at-rest assumptions.

## Trust boundaries

| Boundary | Crosses what | Trust assumption |
|---|---|---|
| MCP client ↔ HTTPS server | Public internet (TLS) | Client authenticates via FastMCP-issued HS256 JWT; server trusts only signed bearer tokens. |
| MCP client ↔ stdio subprocess | OS process boundary (stdin/stdout) | "User is the process owner" — the MCP client and stdio subprocess share an OS user; no auth between them. |
| Server ↔ Google APIs | TLS, OAuth client-credentialed | httpx default `verify=True` against bundled certifi CA. No TLS-skip path anywhere in the tree. |
| Server ↔ disk | File system, 0700 dir / 0600 files | Token store + audit pepper + Fernet key co-located under `~/.config/google-chat-mcp/`; encryption-at-rest is defense-in-depth for backup leaks, NOT against an attacker with directory read. |
| Stdio host user ↔ loopback OAuth listener | `127.0.0.1:<random>` for ~seconds during `login` | Kernel-routed loopback socket; PKCE + state enforced by `google-auth-oauthlib`. Co-resident processes can't bind-race. |

## Assets

- **Google OAuth refresh tokens** — high value, long-lived, Fernet-encrypted at rest.
- **FastMCP JWT signing key** (HTTPS only) — leak enables MCP-layer bearer-token forgery. Min 32 chars; validated at config-parse.
- **Fernet at-rest key** — leak decrypts stored refresh tokens. Validated as 44-char base64 at config-parse.
- **Audit HMAC pepper** — leak (combined with audit DB) enables `user_sub` re-identification.
- **Google access tokens in flight** — short-lived but include scope union; attacker capture enables impersonation until expiry.
- **Message content + member emails** — flow through tool results, returned to MCP client verbatim.

## Adversary classes

- **Network attacker (HTTPS)** — can attempt JWT forgery, redirect-URI abuse, replay. Mitigated by `jwt_signing_key.min_length=32` + GoogleProvider's PKCE + state.
- **Malicious co-resident process (stdio host)** — can't bind-race the loopback socket; can only attack via env-var write. The `chat_api_base` / `people_api_base` env path is gated behind `GCM_DEV_MODE=1`. The `GCM_CONFIG_DIR` env path is gated behind `GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME=1`.
- **Malicious MCP client** — can issue any tool call under the user's valid OAuth. All scope-restricted tools are bounded by Google's per-scope authorization. **Server-side does not police what the client asks for** — that's the trust model.
- **Malicious upstream Google response** — every value we read is constrained at the model boundary: `_ID`-pattern resource names, `EmailStr`, `datetime` coercion, and required-field presence. Unknown *keys* are accepted and reported (`schema_drift`, `mcp_schema_drift_total`) rather than rejected — they are inert, since no handler reads them, and rejecting them cost two total outages. Tool results pass a second `extra="forbid"` boundary (`_Strict`) before reaching the caller.
- **Prompt-injection from chat content** — message bodies are returned verbatim to the MCP client. The LLM harness is the trust boundary; the server cannot defend against an LLM that follows instructions found in chat. Documented as a known limitation.

## Out of scope

- Operator phishing their own Workspace users (per-deployer trust model — the deployer owns their Google app).
- Compromise of the host OS / root (stdio "user is process owner" axiom).
- Compromise of Google's OAuth server.
- DOS / rate-limit evasion / regex DOS (project doesn't claim DOS resistance).
- Prompt-injection of the MCP client's LLM via tool result content (client-side concern).

## Accepted advisories

Advisories with no upstream fix, suppressed in CI so the audit job stays
meaningful. `ci.yml`'s `pip-audit` step points here. Revisit each on an
upstream release.

- **`diskcache` 5.6.3 — CVE-2025-69872 / PYSEC-2026-2447** (medium, CVSS 4.0
  5.2). diskcache serializes with pickle, so anyone who can write to the cache
  directory gets code execution in the reading process. Every release through
  5.6.3 is affected and there is no fixed version, so this cannot be resolved
  by upgrading.

  Reachable only on the HTTPS transport: `DiskStore` in `src/server.py` backs
  the OAuth token store at `kv_store_path`. The stdio transport never
  constructs it. The `FernetEncryptionWrapper` around that store does **not**
  mitigate it — diskcache unpickles the file before the Fernet layer sees the
  value, so encryption protects the token at rest and nothing else.

  Accepted because reaching it requires write access to `kv_store_path` inside
  the container, which is already "compromise of the host OS / root" above.
  It buys an attacker who holds that nothing they did not already have.

## Security-relevant invariants (enforced by code)

These properties are guaranteed by Pydantic validation and config-parse
checks. Tests in `tests/test_config.py`, `tests/test_models.py`, and
`tests/test_storage.py` pin them.

1. **`chat_api_base` / `people_api_base` must point at `*.googleapis.com`** in production. Override requires `GCM_DEV_MODE=1` env var.
2. **Resource-name segments must contain ≥1 alphanumeric char** — closes path-traversal via `..` segments that httpx would otherwise normalize per RFC 3986.
3. **Reaction `emoji` rejects `"`, `\`, whitespace** — closes AIP-160 filter injection in `remove_reaction`'s lookup path.
4. **`allowed_client_redirects` validator hardening** — rejects non-`https://` schemes, bare-TLD hosts (`https://com/...`), wildcard-in-TLD patterns (`https://*` / `https://example.*`), and any non-leading `*` placement. Single leading `*.subdomain` wildcards bound to a real ≥2-label suffix (the documented FastMCP pattern) are still accepted.
5. **`jwt_signing_key.min_length=32`, `fernet_key.length=44`** — config-parse rejects trivially weak/malformed keys.
6. **`DirectoryCache.put` silently drops non-`users/{numeric}` writes** — bots/apps/contact IDs can't poison the cache that drives `sender_email` resolution.
7. **`_load_or_create_fernet_key` is concurrent-safe** — atomic temp-file + `os.link` ensures racing `login` invocations converge on the same key.
8. **`GCM_CONFIG_DIR` outside `~/` requires explicit opt-in** — prevents accidental chmod-0700 of arbitrary directories.
9. **stdio `user_sub` cannot be empty/sentinel** — `cmd_login` hard-fails if neither id_token nor /userinfo yields a sub; downstream audit + rate-limit + add_reaction recovery depend on a real Google sub.
10. **stdio scope check is pre-flight** — `granted_scopes` from tokens.json compared against the scopes the tool accepts — `required_scope` plus any `also_accepts` — before the upstream API call; an unknown or unparseable set defers to Google's 403 rather than rejecting locally, and a denial is audited either way. Defence in depth only — Google enforces scopes regardless. (This check never fired before the fix in the Unreleased CHANGELOG entry: the stored value was the *requested* set.)
11. **3xx responses raise `ChatApiError`** — `_request` only treats 2xx as success; 3xx no longer silently masked as empty result.
12. **Log redaction walks nested dicts** — sensitive keys are masked at any depth, not just top-level event keys.

## Deployer responsibilities

The server can't enforce these — they're operator-side controls:

- **Don't share the Fernet key across deployments.** A compromised disk in one environment leaks tokens for all environments sharing the key. See `docs/runbook.md` rotation procedure.
- **Don't set `GCM_AUDIT_HASH_USER_SUB=false` mid-deployment.** Mixing hashed and raw rows in `audit_log.user_sub` makes the column unjoinable across the rotation point.
- **Don't enable `GCM_DEV_MODE=1` outside test environments.** This switches off the upstream-URL safety check.
- **For HTTPS deployers: enumerate MCP clients in `GCM_ALLOWED_CLIENT_REDIRECTS`.** Avoid `*` wildcards; the validator now refuses the riskiest shapes but a single subdomain wildcard pattern (`*.client.example.com`) is still permitted.
