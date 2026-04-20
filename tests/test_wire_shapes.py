"""MCP wire-shape regression tests.

Guards against FastMCP / MCP-SDK upgrades silently changing the over-the-wire
envelope. These tests inspect the result objects as the MCP client sees them,
not just the handler's Python return value.

In scope:
- Tools with typed outputs emit both `content` (TextContent) and
  `structured_content` (dict) — MCP spec 2025-06-18 says typed outputs
  MUST emit structuredContent AND SHOULD emit a JSON-mirror TextContent
  for backcompat.
- Missing-scope failures surface as ToolError with the scope URL in the
  message — the current wire shape; structuredContent on error is a
  follow-up once FastMCP supports it.
- Resources return `mimeType: "application/json"` on ResourceContent
  for dual-exposed get_* endpoints.
"""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from fastmcp import Client, FastMCP
from src.app import build_app
from src.config import Settings
from src.resources.space import register_space_resource
from src.tools._common import AuthInfo, ToolContext


@pytest.fixture
def mcp() -> FastMCP:
    return build_app(Settings.from_env())


# ---------- tool wire shapes ----------


@pytest.mark.asyncio
async def test_whoami_emits_both_content_and_structured_content(
    mcp: FastMCP, mock_access_token
) -> None:
    """Per MCP spec: typed tool outputs emit structuredContent AND a JSON-text mirror.

    Uses the in-process Client so the lifespan runs (ToolContext populated);
    call_tool returns the wire-level CallToolResult as the MCP client sees it.
    """
    with (
        respx.mock(assert_all_called=False) as route_mock,
        mock_access_token(),
    ):
        route_mock.get(url__regex=r"https://openidconnect\.googleapis\.com/v1/userinfo").mock(
            return_value=httpx.Response(
                200,
                json={
                    "sub": "109876543210",
                    "email": "alice@example.com",
                    "name": "Alice",
                },
            )
        )
        async with Client(mcp) as client:
            result = await client.call_tool("whoami", {})

    # Native structuredContent envelope carries the full shape.
    assert result.structured_content is not None, "structuredContent must be present"
    assert result.structured_content["user_sub"] == "109876543210"
    assert result.structured_content["email"] == "alice@example.com"

    # And a TextContent mirror of the JSON is present for backcompat clients.
    assert result.content, "content[] must be present"
    first = result.content[0]
    assert getattr(first, "type", None) == "text"
    mirror = json.loads(first.text)
    assert mirror["user_sub"] == "109876543210"


@pytest.mark.asyncio
async def test_missing_scope_surfaces_as_tool_error_result_with_scope_url(
    mcp: FastMCP, mock_access_token
) -> None:
    """Forcing Google's insufficient-scope 403 → isError:true CallToolResult with scope in text."""
    with (
        respx.mock(assert_all_called=False) as route_mock,
        mock_access_token(),
    ):
        route_mock.get("https://chat.googleapis.com/v1/spaces").mock(
            return_value=httpx.Response(
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
        )
        async with Client(mcp) as client:
            result = await client.call_tool("list_spaces", {}, raise_on_error=False)

    # MCP clients see isError: true + a TextContent block naming the scope.
    # structuredContent for error results is a follow-up once FastMCP supports
    # it; today the scope URL in the text block is the only machine-readable
    # handle for clients that want to drive re-auth prompts.
    assert result.is_error is True
    assert result.content
    text = result.content[0].text
    assert "Missing required OAuth scope:" in text
    assert "chat.spaces.readonly" in text
    assert "login" in text


@pytest.mark.asyncio
async def test_find_direct_message_rejects_invalid_email_at_tool_boundary(
    mcp: FastMCP, mock_access_token
) -> None:
    """EmailStr on the find_direct_message tool wrapper blocks malformed inputs
    at the MCP boundary, before the handler runs — so invalid emails never hit
    Google's API as a 400 and the caller gets a Pydantic-shaped error instead.
    """
    with (
        respx.mock(assert_all_called=False) as _route_mock,
        mock_access_token(),
    ):
        async with Client(mcp) as client:
            result = await client.call_tool(
                "find_direct_message",
                {"user_email": "not-an-email"},
                raise_on_error=False,
            )
    assert result.is_error is True
    assert result.content
    text = result.content[0].text
    assert "email" in text.lower() or "@" in text


# ---------- resource wire shapes ----------


@pytest.mark.asyncio
async def test_resource_read_emits_application_json(chat_client, db) -> None:
    """gchat://spaces/{id} → contents[].mimeType == application/json."""
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter

    async def fake_resolver() -> AuthInfo:
        return AuthInfo(access_token="fake", user_sub="u")

    ctx = ToolContext(
        client=chat_client,
        db=db,
        limiter=TokenBucketLimiter(capacity=60),
        active_users=ActiveUserTracker(),
        audit_pepper=b"p",
        audit_hash_user_sub=True,
        resolver=fake_resolver,
    )
    mini = FastMCP(name="test")
    register_space_resource(mini, resolve_ctx=lambda: ctx)
    with respx.mock(base_url="https://chat.test/v1") as m:
        m.get("/spaces/AAA").mock(
            return_value=httpx.Response(
                200, json={"name": "spaces/AAA", "type": "SPACE", "displayName": "#eng"}
            )
        )
        result = await mini.read_resource("gchat://spaces/AAA")
    assert len(result.contents) == 1
    body = result.contents[0]
    assert body.mime_type == "application/json"
    parsed = json.loads(body.content)
    assert parsed["space_id"] == "spaces/AAA"
    assert parsed["type"] == "SPACE"


# ---------- serverInfo + capabilities ----------


@pytest.mark.asyncio
async def test_server_identity_and_capabilities(mcp: FastMCP) -> None:
    """MCP spec: serverInfo {name, version} + {tools, resources} capabilities only."""
    # Pulled from the FastMCP instance directly — these feed the initialize
    # handshake that any MCP client sees first.
    assert mcp.name == "google-chat-mcp"
    assert isinstance(mcp.version, str)
    assert mcp.version

    tools = await mcp.list_tools()
    tool_names = {t.name for t in tools}
    # All v2 tools registered.
    assert tool_names == {
        "list_spaces",
        "find_direct_message",
        "send_message",
        "get_messages",
        "get_space",
        "list_members",
        "whoami",
        "get_thread",
        "get_message",
        "add_reaction",
        "remove_reaction",
        "list_reactions",
        "search_messages",
        "create_group_chat",
        "create_space",
        "add_member",
        "remove_member",
        "search_people",
    }

    templates = await mcp.list_resource_templates()
    uris = {str(t.uri_template) for t in templates}
    assert uris == {
        "gchat://spaces/{space_id}",
        "gchat://spaces/{space_id}/messages/{message_id}",
        "gchat://spaces/{space_id}/threads/{thread_id}",
    }
