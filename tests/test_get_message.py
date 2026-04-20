"""get_message tool + message resource — single message with inline reactions."""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from fastmcp import FastMCP
from src.resources.message import register_message_resource
from src.tools import get_message_handler
from src.tools._common import ToolContext


def _upstream_message(
    *, emoji_reaction_summaries: list[dict[str, object]] | None = None
) -> dict[str, object]:
    msg: dict[str, object] = {
        "name": "spaces/AAA/messages/M.1",
        "sender": {"name": "users/111", "displayName": "Alice"},
        "createTime": "2026-04-19T10:00:00Z",
        "text": "hi there",
        "thread": {"name": "spaces/AAA/threads/T.1"},
    }
    if emoji_reaction_summaries is not None:
        msg["emojiReactionSummaries"] = emoji_reaction_summaries
    return msg


@pytest.fixture
def _mock_people(mock_access_token):
    """Return a respx mock that covers the message endpoint and People API."""
    return mock_access_token


@pytest.mark.asyncio
async def test_get_message_inlines_reaction_summaries(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1").mock(
            return_value=httpx.Response(
                200,
                json=_upstream_message(
                    emoji_reaction_summaries=[
                        {"emoji": {"unicode": "🙂"}, "reactionCount": 3},
                        {"emoji": {"unicode": "🎉"}, "reactionCount": 1},
                    ]
                ),
            )
        )
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice"}],
                },
            )
        )
        out = await get_message_handler(tool_ctx, "spaces/AAA/messages/M.1")
    assert out.message_id == "spaces/AAA/messages/M.1"
    assert out.space_id == "spaces/AAA"
    assert out.thread_id == "spaces/AAA/threads/T.1"
    assert out.reactions_paged is False
    assert [(r.emoji, r.count) for r in out.reactions] == [("🙂", 3), ("🎉", 1)]


@pytest.mark.asyncio
async def test_get_message_flags_paged_when_many_reactions(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """More than the inline cap → reactions cleared + reactions_paged=True."""
    many_emoji: list[dict[str, object]] = [
        {"emoji": {"unicode": chr(0x1F600 + i)}, "reactionCount": 1} for i in range(30)
    ]
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1").mock(
            return_value=httpx.Response(
                200, json=_upstream_message(emoji_reaction_summaries=many_emoji)
            )
        )
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice"}],
                },
            )
        )
        out = await get_message_handler(tool_ctx, "spaces/AAA/messages/M.1")
    assert out.reactions == []
    assert out.reactions_paged is True


@pytest.mark.asyncio
async def test_get_message_tolerates_missing_reactions(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1").mock(
            return_value=httpx.Response(200, json=_upstream_message())
        )
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice"}],
                },
            )
        )
        out = await get_message_handler(tool_ctx, "spaces/AAA/messages/M.1")
    assert out.reactions == []
    assert out.reactions_paged is False


@pytest.mark.asyncio
async def test_message_resource_reads_via_tool_path(chat_client, db) -> None:
    """gchat://spaces/{AAA}/messages/{M.1} returns the tool-equivalent body."""
    from src.rate_limit import ActiveUserTracker, TokenBucketLimiter
    from src.tools._common import AuthInfo

    async def fake_resolver() -> AuthInfo:
        return AuthInfo(access_token="fake", user_sub="test")

    ctx = ToolContext(
        client=chat_client,
        db=db,
        limiter=TokenBucketLimiter(capacity=60),
        active_users=ActiveUserTracker(),
        audit_pepper=b"p",
        audit_hash_user_sub=True,
        resolver=fake_resolver,
    )
    mcp = FastMCP(name="test")
    register_message_resource(mcp, resolve_ctx=lambda: ctx)

    with respx.mock(assert_all_called=False) as mock:
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1").mock(
            return_value=httpx.Response(200, json=_upstream_message())
        )
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice"}],
                },
            )
        )
        contents = await mcp.read_resource("gchat://spaces/AAA/messages/M.1")

    assert len(contents.contents) == 1
    body = contents.contents[0]
    assert body.mime_type == "application/json"
    data = json.loads(body.content)
    assert data["message_id"] == "spaces/AAA/messages/M.1"
    assert data["space_id"] == "spaces/AAA"
    assert data["thread_id"] == "spaces/AAA/threads/T.1"
