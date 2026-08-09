"""Settings loading: env + Docker secret fallback, redirect list parsing."""

from __future__ import annotations

import warnings
from pathlib import Path

import pytest
from pydantic import SecretStr
from pydantic_settings import SettingsError
from src import config as config_mod
from src.config import (
    EMAIL_SCOPE,
    GOOGLE_OAUTH_SCOPES,
    OPENID_SCOPE,
    PROFILE_SCOPE,
    Settings,
    canonical_scopes,
)


def test_default_redirects_is_empty() -> None:
    # The server is intentionally client-agnostic: no hardcoded callbacks.
    # Operators must set GCM_ALLOWED_CLIENT_REDIRECTS for their MCP client.
    s = Settings.from_env()
    assert s.allowed_client_redirects == []


def test_redirect_list_parses_csv(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(
        "GCM_ALLOWED_CLIENT_REDIRECTS",
        "https://first.example.com/cb,https://second.example.com/cb,https://staging.test/cb",
    )
    s = Settings.from_env()
    assert len(s.allowed_client_redirects) == 3
    assert s.allowed_client_redirects[-1] == "https://staging.test/cb"


def test_missing_secret_raises(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("GCM_FERNET_KEY", raising=False)
    # from_mapping bypasses env + the repo-root .env a developer may keep for
    # local `uv run`; the test asserts missing-secret behavior independent of
    # that dev-only convenience.
    with pytest.raises(ValueError, match="fernet_key"):
        Settings.from_mapping({})


def test_oauth_scopes_include_required_set() -> None:
    required = {
        "https://www.googleapis.com/auth/chat.messages.readonly",
        "https://www.googleapis.com/auth/chat.messages.create",
        "https://www.googleapis.com/auth/chat.messages.reactions",
        "https://www.googleapis.com/auth/chat.spaces.readonly",
        "https://www.googleapis.com/auth/chat.spaces.create",
        "https://www.googleapis.com/auth/chat.memberships.readonly",
        "https://www.googleapis.com/auth/directory.readonly",
    }
    assert required.issubset(set(GOOGLE_OAUTH_SCOPES))


def test_chat_messages_umbrella_present_for_message_lifecycle() -> None:
    # v0.2.0 dropped the restricted-tier umbrella `chat.messages` in favor of
    # narrower sensitive-tier scopes (.create / .reactions / .readonly).
    # v0.3.2 brings it back because update_message + delete_message hit
    # `spaces.messages.patch` / `.delete`, which only the umbrella scope
    # authorizes (the .create / .readonly scopes don't cover edit + delete).
    # Pinning here so the deployer-visible scope set is intentional, not drift.
    assert "https://www.googleapis.com/auth/chat.messages" in GOOGLE_OAUTH_SCOPES
    # The narrower scopes still ride along — different endpoints use each.
    assert "https://www.googleapis.com/auth/chat.messages.create" in GOOGLE_OAUTH_SCOPES
    assert "https://www.googleapis.com/auth/chat.messages.readonly" in GOOGLE_OAUTH_SCOPES


@pytest.mark.parametrize(
    "uri",
    [
        "https://*",
        "https://example.*",
        "https://*/cb",
        "https://com/cb",
        "http://example.com/cb",
        "ftp://example.com/cb",
    ],
)
def test_allowed_redirects_rejects_unsafe_patterns(
    monkeypatch: pytest.MonkeyPatch, uri: str
) -> None:
    """Regression for the redirect-allowlist hardening: bare-TLD patterns,
    multi-`*` wildcards, and non-https schemes are common operator typos
    that turn the allowlist into an open-redirect surface."""
    monkeypatch.setenv("GCM_ALLOWED_CLIENT_REDIRECTS", uri)
    with pytest.raises(ValueError, match="redirect"):
        Settings.from_env()


def test_allowed_redirects_accepts_documented_subdomain_wildcard(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Single leading `*.` bound to a ≥2-label suffix is the FastMCP-
    documented pattern; keep it working."""
    monkeypatch.setenv("GCM_ALLOWED_CLIENT_REDIRECTS", "https://*.client.example.com/cb")
    s = Settings.from_env()
    assert s.allowed_client_redirects == ["https://*.client.example.com/cb"]


def test_chat_api_base_rejects_non_google_url_in_production(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Closes the token-exfil vector: GCM_CHAT_API_BASE must point at Google
    unless the test-only GCM_DEV_MODE=1 gate is set."""
    monkeypatch.delenv("GCM_DEV_MODE", raising=False)
    monkeypatch.setenv("GCM_CHAT_API_BASE", "https://attacker.example.com/v1")
    with pytest.raises(ValueError, match=r"googleapis\.com"):
        Settings.from_env()


def test_chat_api_base_rejects_http_scheme_in_production(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("GCM_DEV_MODE", raising=False)
    monkeypatch.setenv("GCM_CHAT_API_BASE", "http://chat.googleapis.com/v1")
    with pytest.raises(ValueError, match="https://"):
        Settings.from_env()


def test_chat_api_base_accepts_arbitrary_url_in_dev_mode(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Integration tests need to point at a local mock — explicitly opt in."""
    monkeypatch.setenv("GCM_DEV_MODE", "1")
    monkeypatch.setenv("GCM_CHAT_API_BASE", "http://127.0.0.1:54321/v1")
    s = Settings.from_env()
    assert s.chat_api_base == "http://127.0.0.1:54321/v1"


def test_chat_api_base_default_passes_validator(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("GCM_DEV_MODE", raising=False)
    monkeypatch.delenv("GCM_CHAT_API_BASE", raising=False)
    monkeypatch.delenv("GCM_PEOPLE_API_BASE", raising=False)
    s = Settings.from_env()
    assert s.chat_api_base.startswith("https://chat.googleapis.com/")
    assert s.people_api_base.startswith("https://people.googleapis.com/")


def test_jwt_signing_key_min_length_enforced(monkeypatch: pytest.MonkeyPatch) -> None:
    """Trivially short keys (1 char) would let an attacker recover the
    signing key from any emitted MCP-layer JWT and forge bearer tokens."""
    monkeypatch.setenv("GCM_JWT_SIGNING_KEY", "x")
    with pytest.raises(ValueError, match="jwt_signing_key"):
        Settings.from_env()


def test_fernet_key_length_enforced(monkeypatch: pytest.MonkeyPatch) -> None:
    """Fernet keys are URL-safe base64 of 32 bytes — exactly 44 chars.
    Anything else isn't a valid Fernet key and would crash at first use;
    the validator surfaces the mistake at config-parse."""
    monkeypatch.setenv("GCM_FERNET_KEY", "too-short")
    with pytest.raises(ValueError, match="fernet_key"):
        Settings.from_env()


def test_secret_fields_are_secretstr() -> None:
    s = Settings.from_env()
    assert isinstance(s.google_client_id, SecretStr)
    assert isinstance(s.google_client_secret, SecretStr)
    assert isinstance(s.fernet_key, SecretStr)
    assert isinstance(s.jwt_signing_key, SecretStr)
    assert isinstance(s.audit_pepper, SecretStr)


def test_secret_fields_mask_in_model_dump() -> None:
    s = Settings.from_env()
    dumped = s.model_dump()
    # SecretStr masks to `**********` in model_dump (not the raw value).
    for key in ("google_client_id", "google_client_secret", "fernet_key", "jwt_signing_key"):
        assert "test-" not in str(dumped[key]), (
            f"{key} leaked raw secret into model_dump: {dumped[key]!r}"
        )


def test_audit_pepper_required_when_hashing_enabled(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("GCM_AUDIT_PEPPER", raising=False)
    with pytest.raises(ValueError, match="audit_pepper is required"):
        Settings.from_mapping({})


def test_audit_pepper_optional_when_hashing_disabled(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("GCM_AUDIT_PEPPER", raising=False)
    monkeypatch.setenv("GCM_AUDIT_HASH_USER_SUB", "false")
    s = Settings.from_mapping({})
    assert s.audit_hash_user_sub is False
    assert s.audit_pepper is None


def test_from_mapping_bypasses_env(monkeypatch: pytest.MonkeyPatch) -> None:
    # Clear every GCM_* env var; from_mapping must succeed on kwargs alone.
    for var in [
        "GCM_BASE_URL",
        "GCM_GOOGLE_CLIENT_ID",
        "GCM_GOOGLE_CLIENT_SECRET",
        "GCM_FERNET_KEY",
        "GCM_JWT_SIGNING_KEY",
        "GCM_AUDIT_PEPPER",
        "GCM_DATA_DIR",
    ]:
        monkeypatch.delenv(var, raising=False)
    import secrets

    from cryptography.fernet import Fernet

    s = Settings.from_mapping(
        {
            "base_url": "https://stdio.example.test",
            "google_client_id": "explicit-id",
            "google_client_secret": "explicit-secret",
            "fernet_key": Fernet.generate_key().decode(),
            # Generated at runtime so the secret-shaped value never sits in
            # source — appeases gitleaks's generic-api-key heuristic.
            "jwt_signing_key": secrets.token_urlsafe(32),
            "audit_pepper": "explicit-pepper",
        }
    )
    assert s.base_url == "https://stdio.example.test"
    assert s.google_client_id.get_secret_value() == "explicit-id"
    assert s.audit_pepper is not None
    assert s.audit_pepper.get_secret_value() == "explicit-pepper"


def test_from_env_matches_bare_construction() -> None:
    # Classmethod is a thin alias; behavior parity with cls() must hold.
    a = Settings.from_env()
    b = Settings()
    assert a.base_url == b.base_url
    assert a.google_client_id.get_secret_value() == b.google_client_id.get_secret_value()


def test_construction_is_silent_when_secrets_dir_is_absent(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """No `directory "..." does not exist` warning on the stdio path.

    /run/secrets exists only in the Docker deployment, so passing it
    unconditionally put a UserWarning in every stdio user's client log on every
    invocation. The runbook points operators at stderr as the one signal for
    degraded People lookups; routine noise there teaches them to ignore it.

    Points at a path under tmp_path rather than trusting the real /run/secrets
    to be absent — otherwise this passes vacuously inside a container.
    """
    monkeypatch.setattr(config_mod, "_SECRETS_DIR", tmp_path / "nope")
    with warnings.catch_warnings():
        warnings.simplefilter("error", UserWarning)
        Settings.from_env()


def test_secrets_dir_is_read_when_present(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """The Docker branch: secrets load from files when the env var is unset.

    This is the only path that loads secrets in production, and gating it on
    `Path.exists()` at import time left it unreachable from a test. Env still
    outranks the file (pydantic-settings' default order, preserved here), so
    this drops the env var to see the file actually being read.
    """
    secrets = tmp_path / "run-secrets"
    secrets.mkdir()
    # env_prefix applies to secrets_dir lookups too, hence the GCM_ prefix.
    (secrets / "GCM_google_client_id").write_text("id-from-secret-file")

    monkeypatch.setattr(config_mod, "_SECRETS_DIR", secrets)
    monkeypatch.delenv("GCM_GOOGLE_CLIENT_ID", raising=False)

    # from_mapping({}) drops the repo-root .env a developer may keep for local
    # `uv run`, which would otherwise supply the field ahead of the file.
    loaded = Settings.from_mapping({})
    assert loaded.google_client_id.get_secret_value() == "id-from-secret-file"


def test_secrets_dir_that_is_not_a_directory_still_raises(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """A mounted file where a directory belongs must stay loud.

    pydantic-settings only warns for a *missing* path but raises for one that
    exists and is not a directory. Gating on `is_dir()` would silently discard
    every secret and resurface as an unrelated missing-field error.
    """
    not_a_dir = tmp_path / "run-secrets"
    not_a_dir.write_text("oops")

    monkeypatch.setattr(config_mod, "_SECRETS_DIR", not_a_dir)

    with pytest.raises(SettingsError, match="must reference a directory"):
        Settings.from_env()


def test_canonical_scopes_rewrites_only_the_oidc_aliases() -> None:
    # Google accepts `email`/`profile` on the way out and reports the userinfo.*
    # URLs on the way back; google-auth compares the two verbatim on refresh.
    canonical = canonical_scopes([OPENID_SCOPE, EMAIL_SCOPE, PROFILE_SCOPE])
    assert canonical == [
        "openid",
        "https://www.googleapis.com/auth/userinfo.email",
        "https://www.googleapis.com/auth/userinfo.profile",
    ]
    # Everything else is already in the form Google echoes back.
    chat_scopes = [s for s in GOOGLE_OAUTH_SCOPES if s.startswith("https://")]
    assert canonical_scopes(chat_scopes) == chat_scopes


def test_alias_table_matches_fastmcp() -> None:
    """Pin our table to fastmcp's rather than trusting a comment to keep it so.

    The HTTPS transport normalizes through `GOOGLE_SCOPE_ALIASES` and stdio
    through ours. If the two drift, one transport starts accepting a scope name
    the other rejects — and no test would otherwise notice.
    """
    from fastmcp.server.auth.providers.google import GOOGLE_SCOPE_ALIASES

    assert config_mod._OIDC_ALIAS_CANONICAL == GOOGLE_SCOPE_ALIASES
