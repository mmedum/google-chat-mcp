"""Settings loading: env + Docker secret fallback, redirect list parsing."""

from __future__ import annotations

import pytest
from src.config import GOOGLE_OAUTH_SCOPES, Settings


def test_default_redirects_is_empty() -> None:
    # The server is intentionally client-agnostic: no hardcoded callbacks.
    # Operators must set GCM_ALLOWED_CLIENT_REDIRECTS for their MCP client.
    s = Settings()  # type: ignore[call-arg]
    assert s.allowed_client_redirects == []


def test_redirect_list_parses_csv(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(
        "GCM_ALLOWED_CLIENT_REDIRECTS",
        "https://first.example.com/cb,https://second.example.com/cb,https://staging.test/cb",
    )
    s = Settings()  # type: ignore[call-arg]
    assert len(s.allowed_client_redirects) == 3
    assert s.allowed_client_redirects[-1] == "https://staging.test/cb"


def test_missing_secret_raises(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("GCM_FERNET_KEY", raising=False)
    # `_env_file=None` bypasses the repo-root .env a developer may keep for
    # local `uv run`; the test asserts missing-secret behavior independent of
    # that dev-only convenience. pydantic-settings accepts it dynamically.
    with pytest.raises(ValueError, match="fernet_key"):
        Settings(_env_file=None)  # ty: ignore[unknown-argument]


def test_oauth_scopes_include_required_set() -> None:
    required = {
        "https://www.googleapis.com/auth/chat.messages",
        "https://www.googleapis.com/auth/chat.messages.readonly",
        "https://www.googleapis.com/auth/chat.spaces.readonly",
        "https://www.googleapis.com/auth/chat.spaces.create",
        "https://www.googleapis.com/auth/chat.memberships.readonly",
        "https://www.googleapis.com/auth/directory.readonly",
    }
    assert required.issubset(set(GOOGLE_OAUTH_SCOPES))
