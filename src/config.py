"""Configuration loaded from env + Docker secrets.

Secrets (Fernet key, Google client credentials, JWT signing key) are read from
/run/secrets/* in the container, or from `GCM_*` env vars in local dev, by
pydantic-settings. The ``secrets_dir`` is silently skipped when it doesn't
exist, so env vars take over in development.
"""

from __future__ import annotations

from pathlib import Path
from typing import Annotated

from pydantic import Field, field_validator
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
    http_timeout_seconds: float = Field(default=10.0, gt=0)
    http_max_retries: int = Field(default=3, ge=0, le=10)

    # Secrets: required. Pydantic raises ValidationError if absent from both
    # /run/secrets and env. The field names must match the Docker secret filenames
    # (google_client_id, google_client_secret, fernet_key, jwt_signing_key).
    google_client_id: str = Field(..., min_length=1)
    google_client_secret: str = Field(..., min_length=1)
    fernet_key: str = Field(..., min_length=1)
    jwt_signing_key: str = Field(..., min_length=1)

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


GOOGLE_OAUTH_SCOPES: tuple[str, ...] = (
    "openid",
    "email",
    "profile",
    "https://www.googleapis.com/auth/chat.messages",
    "https://www.googleapis.com/auth/chat.messages.readonly",
    "https://www.googleapis.com/auth/chat.spaces.readonly",
    "https://www.googleapis.com/auth/chat.spaces.create",
    "https://www.googleapis.com/auth/chat.memberships.readonly",
    "https://www.googleapis.com/auth/directory.readonly",
)
