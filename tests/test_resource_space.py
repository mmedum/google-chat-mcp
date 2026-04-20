"""Resource: `gchat://spaces/{space_id}` reads via the same chat_client path as get_space."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.models import SpaceDetails
from src.resources.space import register_space_resource
from src.tools._common import AuthInfo, ToolContext


@pytest.fixture
def space_mcp(chat_client, db, mocker=None):
    """Minimal FastMCP app with only the space resource + test-ctx resolver."""
    from fastmcp import FastMCP
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter

    async def fake_resolver() -> AuthInfo:
        return AuthInfo(access_token="fake-access-token", user_sub="test-user")

    ctx = ToolContext(
        client=chat_client,
        db=db,
        limiter=TokenBucketLimiter(capacity=60),
        active_users=ActiveUserTracker(),
        audit_pepper=b"pepper",
        audit_hash_user_sub=True,
        resolver=fake_resolver,
    )
    mcp = FastMCP(name="test-server")
    register_space_resource(mcp, resolve_ctx=lambda: ctx)
    return mcp


@pytest.mark.asyncio
async def test_space_resource_reads_via_chat_client(space_mcp) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces/AAA").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/AAA",
                    "type": "SPACE",
                    "displayName": "#engineering",
                },
            )
        )
        contents = await space_mcp.read_resource("gchat://spaces/AAA")

    # FastMCP wraps the handler string into ResourceContent objects inside
    # a ResourceResult (mimeType set via the @mcp.resource decorator).
    assert len(contents.contents) == 1
    body = contents.contents[0]
    assert body.mime_type == "application/json"
    import json

    data = json.loads(body.content)
    parsed = SpaceDetails.model_validate(data)
    assert parsed.space_id == "spaces/AAA"
    assert parsed.display_name == "#engineering"
