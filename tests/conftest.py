"""Shared test fixtures."""

from __future__ import annotations

from collections.abc import AsyncIterator, Iterator
from contextlib import contextmanager
from pathlib import Path
from unittest.mock import patch

import pytest
import pytest_asyncio
from cryptography.fernet import Fernet
from src.chat_client import ChatClient
from src.rate_limit import ActiveUserTracker, TokenBucketLimiter
from src.storage import Database, lifespan_database
from src.tools._common import ToolContext


@pytest.fixture(autouse=True)
def _env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Baseline env for Settings.from_env(): every required var satisfied."""
    monkeypatch.setenv("GCM_BASE_URL", "https://mcp.example.test")
    monkeypatch.setenv("GCM_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("GCM_GOOGLE_CLIENT_ID", "test-client-id")
    monkeypatch.setenv("GCM_GOOGLE_CLIENT_SECRET", "test-client-secret")
    monkeypatch.setenv("GCM_FERNET_KEY", Fernet.generate_key().decode())
    monkeypatch.setenv("GCM_JWT_SIGNING_KEY", "test-jwt-signing-key-at-least-32-bytes-long")
    monkeypatch.setenv("GCM_AUDIT_PEPPER", "test-audit-pepper-not-a-real-secret")


@pytest_asyncio.fixture
async def db(tmp_path: Path) -> AsyncIterator[Database]:
    async with lifespan_database(tmp_path / "test.sqlite") as d:
        yield d


@pytest_asyncio.fixture
async def chat_client() -> AsyncIterator[ChatClient]:
    client = ChatClient(base_chat="https://chat.test/v1", base_people="https://people.test/v1")
    try:
        yield client
    finally:
        await client.close()


@pytest_asyncio.fixture
async def tool_ctx(db: Database, chat_client: ChatClient) -> AsyncIterator[ToolContext]:
    yield ToolContext(
        client=chat_client,
        db=db,
        limiter=TokenBucketLimiter(capacity=60),
        active_users=ActiveUserTracker(),
        audit_pepper=b"test-audit-pepper-not-a-real-secret",
        audit_hash_user_sub=True,
    )


class _FakeToken:
    def __init__(self, token: str, sub: str) -> None:
        self.token = token
        self.claims = {"sub": sub}


@contextmanager
def _patch_access_token(
    sub: str = "test-user-sub", upstream: str = "upstream-access-token"
) -> Iterator[None]:
    with patch(
        "src.tools._common.get_access_token",
        return_value=_FakeToken(token=upstream, sub=sub),
    ):
        yield


@pytest.fixture
def mock_access_token():
    """Yield a context-manager that patches fastmcp's get_access_token."""
    return _patch_access_token
