"""Settings loading: env + Docker secret fallback, redirect list parsing."""

from __future__ import annotations

import pytest
from pydantic import SecretStr
from src.config import GOOGLE_OAUTH_SCOPES, Settings


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
    s = Settings.from_mapping(
        {
            "base_url": "https://stdio.example.test",
            "google_client_id": "explicit-id",
            "google_client_secret": "explicit-secret",
            "fernet_key": "explicit-fernet",
            "jwt_signing_key": "explicit-jwt-key",
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
    b = Settings()  # type: ignore[call-arg]
    assert a.base_url == b.base_url
    assert a.google_client_id.get_secret_value() == b.google_client_id.get_secret_value()
