"""get_thread tool + gchat://spaces/{space_id}/threads/{thread_id} resource."""

from __future__ import annotations

import json
from urllib.parse import parse_qs, urlparse

import httpx
import pytest
import respx
from fastmcp import FastMCP
from src.models import GetThreadInput
from src.resources.thread import register_thread_resource
from src.tools import get_thread_handler
from src.tools._common import ToolContext


def _upstream_thread_page(thread_name: str) -> dict[str, object]:
    return {
        "messages": [
            {
                "name": f"{thread_name.replace('/threads/', '/messages/')}.1",
                "sender": {"name": "users/111", "displayName": "Alice"},
                "createTime": "2026-04-19T10:00:00Z",
                "text": "first",
                "thread": {"name": thread_name},
            },
            {
                "name": f"{thread_name.replace('/threads/', '/messages/')}.2",
                "sender": {"name": "users/222", "displayName": "Bob"},
                "createTime": "2026-04-19T10:01:00Z",
                "text": "second",
                "thread": {"name": thread_name},
            },
        ]
    }


@pytest.mark.asyncio
async def test_get_thread_returns_ordered_messages(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    thread_name = "spaces/AAA/threads/T.42"
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_upstream_thread_page(thread_name)),
        )
        # People-API resolve: return the same shape for both senders so enrichment
        # doesn't drop them. Use a regex so both /people/111 and /people/222 hit.
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [{"value": "u@example.com", "metadata": {"primary": True}}],
                    "names": [{"displayName": "U"}],
                },
            )
        )
        out = await get_thread_handler(
            tool_ctx, GetThreadInput(space_id="spaces/AAA", thread_name=thread_name)
        )
    # Filter + orderBy are on the request URL.
    qs = parse_qs(urlparse(str(route.calls[0].request.url)).query)
    assert qs["filter"] == [f'thread.name = "{thread_name}"']
    assert qs["orderBy"] == ["createTime asc"]
    # Thread messages come back in order, with sender enrichment attempted.
    texts = [m.text for m in out]
    assert texts == ["first", "second"]
    assert all(m.thread_id == thread_name for m in out)


@pytest.mark.asyncio
async def test_thread_resource_uri_parsing_and_read(chat_client, db, mock_access_token) -> None:
    """gchat://spaces/{AAA}/threads/{T.42} reads via the same path as get_thread tool."""
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter
    from src.tools._common import AuthInfo

    async def fake_resolver() -> AuthInfo:
        return AuthInfo(access_token="fake-token", user_sub="test-user")

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
    register_thread_resource(mcp, resolve_ctx=lambda: ctx)

    thread_name = "spaces/AAA/threads/T.42"
    with respx.mock(assert_all_called=False) as mock:
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_upstream_thread_page(thread_name)),
        )
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [{"value": "u@example.com", "metadata": {"primary": True}}],
                    "names": [{"displayName": "U"}],
                },
            )
        )
        contents = await mcp.read_resource("gchat://spaces/AAA/threads/T.42")

    assert len(contents.contents) == 1
    body = contents.contents[0]
    assert body.mime_type == "application/json"
    data = json.loads(body.content)
    assert len(data) == 2
    assert data[0]["text"] == "first"
    assert data[1]["text"] == "second"
