"""Configuration loaded from env + Docker secrets.

Secrets (Fernet key, Google client credentials, JWT signing key) are read from
/run/secrets/* in the container, or from `GCM_*` env vars in local dev, by
pydantic-settings. The ``secrets_dir`` is silently skipped when it doesn't
exist, so env vars take over in development.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Annotated, Any

from pydantic import Field, SecretStr, field_validator, model_validator
from pydantic_settings import BaseSettings, NoDecode, SettingsConfigDict


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

    # Upstream base URLs — default to Google but overridable so integration
    # tests can point ChatClient at a local mock server. Not load-bearing in
    # production; don't set these outside tests.
    chat_api_base: str = Field(default="https://chat.googleapis.com/v1")
    people_api_base: str = Field(default="https://people.googleapis.com/v1")

    # Secrets: required. Pydantic raises ValidationError if absent from both
    # /run/secrets and env. The field names must match the Docker secret filenames
    # (google_client_id, google_client_secret, fernet_key, jwt_signing_key, audit_pepper).
    google_client_id: SecretStr = Field(..., min_length=1)
    google_client_secret: SecretStr = Field(..., min_length=1)
    fernet_key: SecretStr = Field(..., min_length=1)
    jwt_signing_key: SecretStr = Field(..., min_length=1)
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

    @field_validator("allowed_client_redirects", mode="before")
    @classmethod
    def _split_csv(cls, v: object) -> object:
        if isinstance(v, str):
            v = [s.strip() for s in v.split(",") if s.strip()]
        if isinstance(v, list):
            for uri in v:
                if not isinstance(uri, str) or not uri.startswith("https://"):
                    raise ValueError(
                        f"allowed_client_redirects entries must be absolute https:// URLs; "
                        f"got {uri!r}"
                    )
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
