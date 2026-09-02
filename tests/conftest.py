"""Shared test fixtures."""

from __future__ import annotations

import io
import logging
from collections.abc import AsyncIterator, Iterator
from contextlib import contextmanager
from pathlib import Path
from unittest.mock import patch

import httpx2
import pytest
import pytest_asyncio
import structlog
from cryptography.fernet import Fernet
from src.chat_client import ChatClient
from src.observability import configure_logging
from src.rate_limit import ActiveUserTracker, TokenBucketLimiter
from src.storage import Database, lifespan_database
from src.tools._common import ToolContext

from ._httpx2_mock import dispatch_transport, intercept_all_clients


@pytest.fixture(autouse=True)
def _no_real_network() -> Iterator[None]:
    """No test may reach the network, including via a client it did not build."""
    with intercept_all_clients():
        yield


@pytest.fixture(autouse=True)
def _env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Baseline env for Settings.from_env(): every required var satisfied.

    Also pins `GCM_CONFIG_DIR` into `tmp_path` for *every* test. `cmd_login`
    writes tokens.json through `_open_store()`, which resolves that variable
    at call time — so a test that exercises it without opting into isolation
    overwrites the developer's own `~/.config/google-chat-mcp/tokens.json`
    and destroys their refresh token. That happened. Opting in per-test was
    the design and it failed the first time someone added a login test
    without remembering, so the default is now safe and tests that care
    about config-dir behaviour override it themselves.
    """
    monkeypatch.setenv("GCM_CONFIG_DIR", str(tmp_path / "gcm-config"))
    monkeypatch.setenv("GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME", "1")
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
    # Transport-injected rather than globally patched: `mock_api()` sets the
    # active router, and this client's transport looks it up per request. A
    # request outside any `mock_api()` block raises instead of reaching the
    # network.
    client = ChatClient(
        base_chat="https://chat.test/v1",
        base_people="https://people.test/v1",
        client=httpx2.AsyncClient(transport=dispatch_transport()),
    )
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


def scope_403() -> httpx2.Response:
    """Google's insufficient-scope 403, in the exact shape `is_missing_scope_error` parses.

    Shared because it guards a security invariant: every idempotent-delete path
    must keep raising on this rather than reporting a quiet `deleted=False`.
    The envelope was hand-rebuilt across the suite with no copy authoritative;
    the identical ones now import this. Three files still build their own
    because their payloads genuinely differ: `test_tools.py` adds a `domain`
    key, `test_tools_common.py` varies the envelope to exercise the parser,
    and `test_degraded_enrichment.py` omits `@type` and reuses one response
    object across calls. New tests should import this one.
    """
    return httpx2.Response(
        403,
        json={
            "error": {
                "code": 403,
                "message": "Request had insufficient authentication scopes.",
                "status": "PERMISSION_DENIED",
                "details": [
                    {
                        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                        "reason": "ACCESS_TOKEN_SCOPE_INSUFFICIENT",
                    }
                ],
            }
        },
    )


def person_payload(email: str, display_name: str | None = None) -> dict[str, object]:
    """Build a People-API `people.get` response body with primary email + name.

    `emailAddresses` is always present; `names` only when `display_name` is
    supplied — lets tests simulate the real-world pattern where non-self
    Workspace users return emailAddresses=null (see project_people_api_resolution.md).
    """
    payload: dict[str, object] = {
        "emailAddresses": [{"metadata": {"primary": True}, "value": email}],
    }
    if display_name is not None:
        payload["names"] = [{"metadata": {"primary": True}, "displayName": display_name}]
    return payload


@pytest.fixture
def structlog_stream() -> Iterator[io.StringIO]:
    """Capture structlog output through the REAL processor chain.

    `structlog.testing.capture_logs` replaces the chain, so it cannot see
    redaction or rendering — this fixture is for asserting on what an operator
    would actually read.

    Teardown restores the *previous config object* rather than rebuilding one.
    That matters: `configure_logging` installs a fresh processor list, and
    `cache_logger_on_first_use=True` means any module-level logger bound
    earlier still points at the old list. `capture_logs` mutates the current
    list in place, so a rebuilt config silently stops intercepting those
    loggers and later tests see no output. Root handlers are likewise restored,
    not cleared — pytest keeps its `caplog` handlers there.
    """
    buf = io.StringIO()
    saved_handlers = list(logging.getLogger().handlers)
    saved_config = structlog.get_config()
    configure_logging("INFO", stream=buf)
    try:
        yield buf
    finally:
        structlog.configure(**saved_config)
        logging.getLogger().handlers[:] = saved_handlers


@pytest.fixture(autouse=True)
def _reset_drift_dedup() -> Iterator[None]:
    """Clear the process-wide `schema_drift` dedup between tests.

    `src.models._reported_drift` suppresses repeat reports of the same
    `Model.field`. Without a reset, whether a test sees its log line depends on
    whether an earlier test happened to use the same key — order-dependent, and
    the failure message points at the wrong test.
    """
    from src import models

    models._reported_drift.clear()
    yield
    models._reported_drift.clear()
