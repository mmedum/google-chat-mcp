"""Tool handler integration tests — mock upstream, assert the real handler wiring."""

from __future__ import annotations

import httpx
import pytest
import respx
from fastmcp.exceptions import ToolError
from src.models import GetMessagesInput, SendMessageInput
from src.tools import (
    find_direct_message_handler,
    get_messages_handler,
    list_spaces_handler,
    send_message_handler,
)
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_list_spaces_handler_happy_path(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces").mock(
            return_value=httpx.Response(
                200,
                json={
                    "spaces": [
                        {"name": "spaces/AAA", "type": "SPACE", "displayName": "#eng"},
                        {"name": "spaces/BBB", "type": "DIRECT_MESSAGE"},
                    ]
                },
            )
        )
        out = await list_spaces_handler(tool_ctx)
    ids = [s.space_id for s in out]
    assert ids == ["spaces/AAA", "spaces/BBB"]
    # DM without displayName falls back to synthetic label, not empty string.
    dm = next(s for s in out if s.type == "DIRECT_MESSAGE")
    assert dm.display_name == "(direct message)"


@pytest.mark.asyncio
async def test_find_direct_message_creates_on_404(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces:findDirectMessage").mock(return_value=httpx.Response(404))
        mock.post("/spaces:setup").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/NEWDM",
                    "type": "DIRECT_MESSAGE",
                },
            )
        )
        result = await find_direct_message_handler(tool_ctx, "alice@example.com")
    assert result.space_id == "spaces/NEWDM"


@pytest.mark.asyncio
async def test_send_message_appends_claude_suffix(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/AAA/messages/M.1",
                    "sender": {"name": "users/111"},
                    "createTime": "2026-04-19T10:00:00Z",
                    "text": "hi\n\n— Claude",
                    "thread": {"name": "spaces/AAA/threads/T.1"},
                },
            )
        )
        out = await send_message_handler(
            tool_ctx, SendMessageInput(space_id="spaces/AAA", text="hi")
        )
    assert out.message_id == "spaces/AAA/messages/M.1"
    # Body sent upstream ends with the suffix.
    sent_body = route.calls[0].request.content.decode()
    assert "— Claude" in sent_body


@pytest.mark.asyncio
async def test_get_messages_resolves_sender_via_people_api(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [
                        {
                            "name": "spaces/AAA/messages/M.1",
                            "sender": {"name": "users/111", "displayName": "Alice"},
                            "createTime": "2026-04-19T10:00:00Z",
                            "text": "hello",
                            "thread": {"name": "spaces/AAA/threads/T.1"},
                        }
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice Smith", "metadata": {"primary": True}}],
                },
            )
        )
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA", limit=5))
    assert len(out) == 1
    msg = out[0]
    assert msg.sender_email == "alice@example.com"
    assert msg.sender_display_name == "Alice Smith"


@pytest.mark.asyncio
async def test_get_messages_missing_people_entry_returns_display_only(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [
                        {
                            "name": "spaces/AAA/messages/M.1",
                            "sender": {"name": "users/999", "displayName": "External"},
                            "createTime": "2026-04-19T10:00:00Z",
                            "text": "x",
                            "thread": {"name": "spaces/AAA/threads/T.1"},
                        }
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/999").mock(return_value=httpx.Response(404))
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))
    assert out[0].sender_email is None
    assert out[0].sender_display_name == "External"


@pytest.mark.asyncio
async def test_rate_limit_blocks_after_capacity(tool_ctx: ToolContext, mock_access_token) -> None:
    # Replace limiter with a tight one.
    from src.rate_limit import TokenBucketLimiter

    tool_ctx.limiter = TokenBucketLimiter(capacity=1)
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": []}))
        await list_spaces_handler(tool_ctx)
        with pytest.raises(ToolError, match="Rate limit"):
            await list_spaces_handler(tool_ctx)
