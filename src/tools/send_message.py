"""Tool: send_message — post a text message to a space.

The server appends `— Claude` to every outbound message body so humans on the
other end can tell the message was posted by Claude on behalf of the user.
"""

from __future__ import annotations

from ..models import SendMessageInput, SendMessageResult, _ChatMessageResponse
from ._common import ToolContext, invoke_tool

_CLAUDE_SUFFIX = "\n\n— Claude"


async def send_message_handler(ctx: ToolContext, payload: SendMessageInput) -> SendMessageResult:
    """Post a message. `thread_name` is an optional reply target."""

    async def body(access_token: str, _user_sub: str) -> SendMessageResult:
        text_with_suffix = payload.text + _CLAUDE_SUFFIX
        raw = await ctx.client.send_message(
            access_token,
            space_id=payload.space_id,
            text=text_with_suffix,
            thread_name=payload.thread_name,
        )
        msg = _ChatMessageResponse(**raw)
        return SendMessageResult(
            message_id=msg.name,
            space_id=payload.space_id,
            thread_id=msg.thread.name,
        )

    return await invoke_tool(
        "send_message",
        ctx,
        body,
        target_space_id=payload.space_id,
    )
