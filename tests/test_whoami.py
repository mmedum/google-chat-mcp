"""whoami tool — OIDC /userinfo wrap."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.tools import whoami_handler
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_whoami_returns_identity_from_userinfo(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://openidconnect.test/v1") as mock,
        mock_access_token(),
    ):
        # conftest's chat_client is built with defaults — override the OIDC base
        # by patching _base_oidc directly for this test.
        tool_ctx.client._base_oidc = "https://openidconnect.test/v1"
        mock.get("/userinfo").mock(
            return_value=httpx.Response(
                200,
                json={
                    "sub": "109876543210",
                    "email": "alice@example.com",
                    "email_verified": True,
                    "name": "Alice Example",
                    "picture": "https://example.com/alice.jpg",
                },
            )
        )
        out = await whoami_handler(tool_ctx)
    assert out.user_sub == "109876543210"
    assert out.email == "alice@example.com"
    assert out.display_name == "Alice Example"
    assert out.picture_url == "https://example.com/alice.jpg"


@pytest.mark.asyncio
async def test_whoami_tolerates_missing_optional_fields(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Google may omit email / name for some identities (e.g. service accounts)."""
    with (
        respx.mock(base_url="https://openidconnect.test/v1") as mock,
        mock_access_token(),
    ):
        tool_ctx.client._base_oidc = "https://openidconnect.test/v1"
        mock.get("/userinfo").mock(
            return_value=httpx.Response(200, json={"sub": "42"}),
        )
        out = await whoami_handler(tool_ctx)
    assert out.user_sub == "42"
    assert out.email is None
    assert out.display_name is None
    assert out.picture_url is None
