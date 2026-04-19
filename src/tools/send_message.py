"""Tool: send_message — post a text message to a space."""

from __future__ import annotations

from ..chat_client import _build_send_message_body
from ..models import SendMessageInput, SendMessageResult, _ChatMessageResponse
from ._common import CHAT_MESSAGES_CREATE, ToolContext, invoke_tool


async def send_message_handler(ctx: ToolContext, payload: SendMessageInput) -> SendMessageResult:
    """Post a message. `thread_name` is an optional reply target.

    `payload.dry_run=True` short-circuits the POST: we build the exact request
    body the real path would send, return it in `rendered_payload`, and leave
    `message_id`/`thread_id` unset. Rate-limit bucket + audit row still fire,
    so dry_run can't be used to bypass either.
    """

    async def body(access_token: str, _user_sub: str) -> SendMessageResult:
        if payload.dry_run:
            rendered, _params = _build_send_message_body(
                text=payload.text, thread_name=payload.thread_name
            )
            return SendMessageResult(
                space_id=payload.space_id,
                dry_run=True,
                rendered_payload=rendered,
            )
        raw = await ctx.client.send_message(
            access_token,
            space_id=payload.space_id,
            text=payload.text,
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
        required_scope=CHAT_MESSAGES_CREATE,
    )
