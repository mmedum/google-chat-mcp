"""Invariants shared by every tool handler — auth plumbing, audit-log hashing."""

from __future__ import annotations

import hashlib
import hmac
from pathlib import Path

import httpx
import pytest
import respx
from src.models import ListSpacesInput
from src.storage import lifespan_database
from src.tools import list_spaces_handler
from src.tools._common import AuthInfo, ToolContext, audit_user_sub


def test_audit_user_sub_hashes_with_pepper() -> None:
    pepper = b"deterministic-pepper"
    out = audit_user_sub("google-sub-12345", pepper=pepper, hash_enabled=True)
    expected = hmac.new(pepper, b"google-sub-12345", hashlib.sha256).hexdigest()
    assert out == expected
    assert len(out) == 64


def test_audit_user_sub_passthrough_when_hashing_off() -> None:
    out = audit_user_sub("google-sub-12345", pepper=None, hash_enabled=False)
    assert out == "google-sub-12345"


def test_audit_user_sub_raises_without_pepper_when_hash_enabled() -> None:
    with pytest.raises(ValueError, match="pepper required"):
        audit_user_sub("x", pepper=None, hash_enabled=True)


def test_tool_context_rejects_hash_enabled_without_pepper(
    tool_ctx: ToolContext,  # triggers conftest, DB is fine; we don't use ctx
) -> None:
    # Direct construction; the `tool_ctx` fixture is just a conftest trigger.
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter

    with pytest.raises(ValueError, match="audit_pepper required"):
        ToolContext(
            client=tool_ctx.client,
            db=tool_ctx.db,
            limiter=TokenBucketLimiter(capacity=60),
            active_users=ActiveUserTracker(),
            audit_pepper=None,
            audit_hash_user_sub=True,
        )


@pytest.mark.asyncio
async def test_resolver_override_path_preferred_over_fastmcp(
    chat_client,
    tmp_path: Path,
) -> None:
    """When ctx.resolver is set, invoke_tool uses it and never calls FastMCP's dep."""
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter

    async def fake_resolver() -> AuthInfo:
        return AuthInfo(access_token="from-resolver", user_sub="stdio-user-42")

    async with lifespan_database(tmp_path / "test.sqlite") as db:
        ctx = ToolContext(
            client=chat_client,
            db=db,
            limiter=TokenBucketLimiter(capacity=60),
            active_users=ActiveUserTracker(),
            audit_pepper=b"pepper",
            audit_hash_user_sub=True,
            resolver=fake_resolver,
        )
        with respx.mock(base_url="https://chat.test/v1") as mock:
            route = mock.get("/spaces").mock(
                return_value=httpx.Response(200, json={"spaces": []})
            )
            # No mock_access_token patch — if invoke_tool called FastMCP's get_access_token
            # here, the real impl would hit a FastMCP request-context guard and raise.
            await list_spaces_handler(ctx, ListSpacesInput())
        # Upstream request carried the resolver-provided token.
        auth_header = route.calls.last.request.headers.get("authorization")
        assert auth_header == "Bearer from-resolver"

        async with db.cursor() as conn:
            cur = await conn.execute("SELECT user_sub FROM audit_log ORDER BY id DESC LIMIT 1")
            row = await cur.fetchone()
    assert row is not None
    # The sub logged is the resolver's, hashed by the configured pepper.
    expected = hmac.new(b"pepper", b"stdio-user-42", hashlib.sha256).hexdigest()
    assert row["user_sub"] == expected


@pytest.mark.asyncio
async def test_audit_row_stores_hashed_sub(
    tool_ctx: ToolContext,
    mock_access_token,
    tmp_path: Path,
) -> None:
    # tool_ctx fixture: audit_hash_user_sub=True, pepper="test-audit-pepper-not-a-real-secret".
    raw_sub = "google-oauth-sub-99"
    expected_hash = hmac.new(
        b"test-audit-pepper-not-a-real-secret",
        raw_sub.encode(),
        hashlib.sha256,
    ).hexdigest()

    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(sub=raw_sub),
    ):
        mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": []}))
        await list_spaces_handler(tool_ctx, ListSpacesInput())

    # Inspect the audit_log directly.
    async with tool_ctx.db.cursor() as conn:
        cur = await conn.execute("SELECT user_sub FROM audit_log ORDER BY id DESC LIMIT 1")
        row = await cur.fetchone()
    assert row is not None
    assert row["user_sub"] == expected_hash
    assert row["user_sub"] != raw_sub


@pytest.mark.asyncio
async def test_audit_row_stores_raw_sub_when_hashing_disabled(
    chat_client,
    tmp_path: Path,
    mock_access_token,
) -> None:
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter

    # Build a context with hashing off.
    async with lifespan_database(tmp_path / "test.sqlite") as db:
        ctx = ToolContext(
            client=chat_client,
            db=db,
            limiter=TokenBucketLimiter(capacity=60),
            active_users=ActiveUserTracker(),
            audit_pepper=None,
            audit_hash_user_sub=False,
        )
        raw_sub = "google-oauth-sub-77"
        with (
            respx.mock(base_url="https://chat.test/v1") as mock,
            mock_access_token(sub=raw_sub),
        ):
            mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": []}))
            await list_spaces_handler(ctx, ListSpacesInput())

        async with db.cursor() as conn:
            cur = await conn.execute("SELECT user_sub FROM audit_log ORDER BY id DESC LIMIT 1")
            row = await cur.fetchone()
    assert row is not None
    assert row["user_sub"] == raw_sub
