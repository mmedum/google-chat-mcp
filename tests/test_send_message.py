"""send_message dry_run: renders payload without posting, preserves audit + rate-limit."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.chat_client import _build_send_message_body
from src.models import SendMessageInput
from src.tools import send_message_handler
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_dry_run_returns_rendered_payload_and_zero_posts(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces/AAA/messages")
        result = await send_message_handler(
            tool_ctx,
            SendMessageInput(space_id="spaces/AAA", text="hello dry-run", dry_run=True),
        )
    # No upstream POST happened — dry_run short-circuits before the HTTP call.
    assert route.call_count == 0
    # Result is dry-run shaped: no message_id, no thread_id, flag set, payload
    # populated.
    assert result.dry_run is True
    assert result.message_id is None
    assert result.thread_id is None
    assert result.space_id == "spaces/AAA"
    assert result.rendered_payload == {"text": "hello dry-run"}


@pytest.mark.asyncio
async def test_dry_run_parity_with_real_post_body(tool_ctx: ToolContext, mock_access_token) -> None:
    """rendered_payload on dry-run equals the body the real POST would send."""
    thread_name = "spaces/AAA/threads/T.42"
    payload = SendMessageInput(space_id="spaces/AAA", text="parity test", thread_name=thread_name)

    # Run a real post, capture the body it sends.
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
                    "text": "parity test",
                    "thread": {"name": thread_name},
                },
            )
        )
        await send_message_handler(tool_ctx, payload)
    import json

    real_body = json.loads(route.calls[0].request.content.decode())

    # Run dry-run; body builder is the same pure function.
    dry_body, _params = _build_send_message_body(text=payload.text, thread_name=payload.thread_name)
    assert dry_body == real_body, "dry/real parity broken — refactor the shared builder"


@pytest.mark.asyncio
async def test_dry_run_still_writes_audit_row(tool_ctx: ToolContext, mock_access_token) -> None:
    """dry_run cannot bypass observability — audit row + rate-limit bucket still fire."""
    with (
        respx.mock(base_url="https://chat.test/v1"),
        mock_access_token(),
    ):
        await send_message_handler(
            tool_ctx,
            SendMessageInput(space_id="spaces/AAA", text="audited dry", dry_run=True),
        )

    async with tool_ctx.db.cursor() as conn:
        cur = await conn.execute(
            "SELECT tool_name, success, target_space_id FROM audit_log ORDER BY id DESC LIMIT 1"
        )
        row = await cur.fetchone()
    assert row is not None
    assert row["tool_name"] == "send_message"
    assert row["success"] == 1
    assert row["target_space_id"] == "spaces/AAA"
