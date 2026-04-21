"""Configuration loaded from env + Docker secrets.

Secrets (Fernet key, Google client credentials, JWT signing key) are read from
/run/secrets/* in the container, or from `GCM_*` env vars in local dev, by
pydantic-settings. The ``secrets_dir`` is silently skipped when it doesn't
exist, so env vars take over in development.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from pathlib import Path
from typing import Annotated, Any
from urllib.parse import urlparse

from pydantic import Field, SecretStr, field_validator, model_validator
from pydantic_settings import BaseSettings, NoDecode, SettingsConfigDict

# Production deployers MUST point at Google. The env override exists for
# integration tests that spin up a local mock; gating it behind GCM_DEV_MODE
# closes a token-exfiltration vector where a host-compromise attacker could
# set GCM_CHAT_API_BASE to an attacker URL and capture every user's
# Google access token in the Authorization header.
_GOOGLE_API_PREFIXES = (
    "https://chat.googleapis.com/",
    "https://people.googleapis.com/",
)
_DEV_MODE_ENV = "GCM_DEV_MODE"


def _validate_redirect_pattern(uri: object) -> None:
    """Reject misconfigurations that turn redirect-allowlist into open-redirect.

    The default validation (https://-only) catches scheme typos but doesn't
    stop an operator from writing a wildcard pattern that grants attacker-
    controlled subdomains. Three additional rules:

    1. Patterns containing `*` in the TLD position (e.g. `https://*` or
       `https://example.*`) are rejected — too easy to write by accident.
    2. The host must have ≥2 labels — bare-TLD patterns like
       `https://com/...` are rejected as obvious typos.
    3. Single leading `*.subdomain` wildcards bound to a real ≥2-label
       suffix (e.g. `https://*.client.example.com/cb`) are accepted —
       this is the documented FastMCP pattern for fan-out clients.

    The strict checks live here at config-parse so the deployer sees a
    clear ValueError before the server boots, not a debug-level warning
    deep in the OAuth flow.
    """
    if not isinstance(uri, str) or not uri.startswith("https://"):
        raise ValueError(
            f"allowed_client_redirects entries must be absolute https:// URLs; got {uri!r}"
        )
    parsed = urlparse(uri)
    host = parsed.hostname or ""
    if "*" in host:
        # Wildcard subdomain matchers grant any attacker-registrable subdomain.
        # Reject outright — operators should enumerate their MCP clients.
        if host.startswith("*.") and host.count("*") == 1:
            # Allowed: `*.client.example.com` (single subdomain wildcard,
            # bound to a real ≥2-label suffix). Still soft-suspicious but
            # is the documented FastMCP pattern, so accept.
            suffix = host[2:]
            if "." not in suffix or suffix.endswith("."):
                raise ValueError(f"redirect wildcard `{uri}` lacks a real ≥2-label suffix")
        else:
            raise ValueError(
                f"redirect pattern `{uri}` contains an unsupported `*` placement; "
                f"only single-leading `*.` subdomain wildcards bound to a real "
                f"≥2-label suffix are accepted"
            )
    elif host.count(".") == 0:
        raise ValueError(
            f"redirect host `{host}` must have ≥2 labels (e.g. example.com); got `{uri}`"
        )


def _dev_mode_enabled() -> bool:
    """Test-only override gate. Reads ``GCM_DEV_MODE`` from the environment.

    Kept intentionally OUTSIDE Settings — Settings fields can be set via
    config files, env vars, or Docker secrets. This env-var-only gate
    prevents a deployer from accidentally turning on dev-mode via any of
    those channels; only an explicit shell-set environment variable in a
    test runner trips it.
    """
    return os.environ.get(_DEV_MODE_ENV) == "1"


class Settings(BaseSettings):
    """Runtime configuration. Non-secret values come from ``GCM_*`` env vars.

    Secret values (client credentials, Fernet key, JWT signing key) are read
    from files in /run/secrets when present, else from ``GCM_*`` env vars.
    """

    model_config = SettingsConfigDict(
        env_prefix="GCM_",
        env_file=".env",
        env_file_encoding="utf-8",
        secrets_dir="/run/secrets",
        extra="ignore",
    )

    base_url: str = Field(
        ...,
        description="Public URL of this MCP server; Google redirects here after OAuth.",
    )
    data_dir: Path = Field(
        default=Path("/var/lib/google-chat-mcp"),
        description="Writable dir for the SQLite DB + py-key-value disk store.",
    )
    log_level: str = Field(default="INFO")
    rate_limit_per_minute: int = Field(default=60, gt=0, le=10_000)
    # `NoDecode` disables pydantic-settings' default JSON-decoding of complex env
    # values so our comma-separated string format reaches `_split_csv` unchanged.
    allowed_client_redirects: Annotated[list[str], NoDecode] = Field(
        default_factory=list,
        description=(
            "Post-auth redirect whitelist (CSV of absolute https:// URLs). "
            "Set to your MCP client's callback URL — the server refuses "
            "redirects to anything outside this list."
        ),
    )
    directory_cache_ttl_seconds: int = Field(default=86_400, gt=0)
    audit_retention_days: int = Field(default=90, gt=0)
    audit_hash_user_sub: bool = Field(
        default=True,
        description=(
            "Hash the user `sub` with HMAC-SHA256 before writing to audit_log. "
            "Set to False to keep raw Google subs (joinable with external identity "
            "systems, but leaks a stable user identifier if the DB is exposed)."
        ),
    )
    http_timeout_seconds: float = Field(default=10.0, gt=0)
    http_max_retries: int = Field(default=3, ge=0, le=10)

    # Upstream base URLs — default to Google. Overridable ONLY when
    # GCM_DEV_MODE=1 is set (integration tests). In production the validator
    # rejects any value that doesn't point at *.googleapis.com, closing the
    # token-exfiltration path described in `docs/security.md`.
    chat_api_base: str = Field(default="https://chat.googleapis.com/v1")
    people_api_base: str = Field(default="https://people.googleapis.com/v1")

    # Secrets: required. Pydantic raises ValidationError if absent from both
    # /run/secrets and env. The field names must match the Docker secret filenames
    # (google_client_id, google_client_secret, fernet_key, jwt_signing_key, audit_pepper).
    google_client_id: SecretStr = Field(..., min_length=1)
    google_client_secret: SecretStr = Field(..., min_length=1)
    # Fernet keys are URL-safe base64 of a 32-byte key, always exactly 44
    # chars including the trailing `=`. Anything shorter is not a valid
    # Fernet key and will fail at first use; pinning here surfaces the
    # mistake at config-parse instead of mid-OAuth-flow.
    fernet_key: SecretStr = Field(..., min_length=44, max_length=44)
    # 32 bytes minimum for HS256 — under that, the key derivation in
    # OAuthProxy can't compensate (PBKDF2 over a 1-char input is still
    # brute-forceable). Signed JWTs forged with the recovered key would
    # let an attacker mint MCP-layer bearer tokens.
    jwt_signing_key: SecretStr = Field(..., min_length=32)
    audit_pepper: SecretStr | None = Field(
        default=None,
        description=(
            "HMAC-SHA256 key for `user_sub` hashing in audit_log. Required when "
            "audit_hash_user_sub is True (the default)."
        ),
    )

    @classmethod
    def from_env(cls) -> Settings:
        """Construct from `GCM_*` env vars + `/run/secrets/GCM_*` (HTTPS transport)."""
        return cls()  # type: ignore[call-arg]

    @classmethod
    def from_mapping(cls, values: Mapping[str, Any]) -> Settings:
        """Construct from an explicit mapping, bypassing env/secrets auto-load (stdio transport)."""
        return cls(_env_file=None, **values)  # ty: ignore[unknown-argument]

    @model_validator(mode="after")
    def _validate_audit_pepper(self) -> Settings:
        if self.audit_hash_user_sub and self.audit_pepper is None:
            raise ValueError(
                "audit_pepper is required when audit_hash_user_sub is True. "
                "Set GCM_AUDIT_PEPPER (or GCM_AUDIT_HASH_USER_SUB=false to store raw sub)."
            )
        return self

    @field_validator("chat_api_base", "people_api_base")
    @classmethod
    def _restrict_upstream_base(cls, v: str) -> str:
        if not v.startswith("https://") and not _dev_mode_enabled():
            raise ValueError(
                f"upstream base URL must use https://; got {v!r}. "
                f"Set {_DEV_MODE_ENV}=1 only in test environments."
            )
        if any(v.startswith(prefix) for prefix in _GOOGLE_API_PREFIXES):
            return v
        if _dev_mode_enabled():
            return v
        raise ValueError(
            f"upstream base URL must point at *.googleapis.com; got {v!r}. "
            f"Set {_DEV_MODE_ENV}=1 only in test environments to override."
        )

    @field_validator("allowed_client_redirects", mode="before")
    @classmethod
    def _split_csv(cls, v: object) -> object:
        if isinstance(v, str):
            v = [s.strip() for s in v.split(",") if s.strip()]
        if isinstance(v, list):
            for uri in v:
                _validate_redirect_pattern(uri)
        return v

    @property
    def sqlite_path(self) -> Path:
        return self.data_dir / "app.sqlite"

    @property
    def kv_store_path(self) -> Path:
        return self.data_dir / "oauth_store"


# Scope constants — single source of truth. Referenced by `GOOGLE_OAUTH_SCOPES`
# below and by each tool's `invoke_tool(..., required_scope=...)` call in
# `src/tools/_common.py`. Adding a new scope? Put it here first.
OPENID_SCOPE = "openid"
EMAIL_SCOPE = "email"
PROFILE_SCOPE = "profile"
CHAT_MESSAGES_READONLY = "https://www.googleapis.com/auth/chat.messages.readonly"
# Split from the umbrella `chat.messages` (Google *restricted* tier, annual CASA).
# .create + .reactions are both *sensitive* tier (3-5 day self-service verification)
# and cover the v2 tool surface (send_message, add/remove/list_reactions).
CHAT_MESSAGES_CREATE = "https://www.googleapis.com/auth/chat.messages.create"
CHAT_MESSAGES_REACTIONS = "https://www.googleapis.com/auth/chat.messages.reactions"
CHAT_SPACES_READONLY = "https://www.googleapis.com/auth/chat.spaces.readonly"
# Retained for find_direct_message's create-on-miss path (spaces.setup).
CHAT_SPACES_CREATE = "https://www.googleapis.com/auth/chat.spaces.create"
CHAT_MEMBERSHIPS_READONLY = "https://www.googleapis.com/auth/chat.memberships.readonly"
# Write-side membership scope: add_member / remove_member (v0.3.1).
# Google sensitive tier; deployers re-consent on upgrade.
CHAT_MEMBERSHIPS = "https://www.googleapis.com/auth/chat.memberships"
# Message lifecycle (update + delete) — Google's RESTRICTED tier (v0.3.2).
# Public-published apps need annual CASA review; Internal Workspace apps
# skip verification entirely. The runbook covers the deployer trade-off.
CHAT_MESSAGES = "https://www.googleapis.com/auth/chat.messages"
DIRECTORY_READONLY = "https://www.googleapis.com/auth/directory.readonly"
# Consumer Gmail fallback for search_people (v0.3.1). Covers caller's own
# contacts + "other contacts" auto-populated from interactions. Sensitive tier.
CONTACTS_READONLY = "https://www.googleapis.com/auth/contacts.readonly"

GOOGLE_OAUTH_SCOPES: tuple[str, ...] = (
    OPENID_SCOPE,
    EMAIL_SCOPE,
    PROFILE_SCOPE,
    CHAT_MESSAGES_READONLY,
    CHAT_MESSAGES_CREATE,
    CHAT_MESSAGES_REACTIONS,
    CHAT_MESSAGES,
    CHAT_SPACES_READONLY,
    CHAT_SPACES_CREATE,
    CHAT_MEMBERSHIPS_READONLY,
    CHAT_MEMBERSHIPS,
    DIRECTORY_READONLY,
    CONTACTS_READONLY,
)
