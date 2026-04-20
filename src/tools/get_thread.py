"""Tool: get_thread — read all messages in a single thread, oldest-first."""

from __future__ import annotations

from ..models import ChatMessage, GetThreadInput, _ChatMessageResponse
from ._common import CHAT_MESSAGES_READONLY, ToolContext, invoke_tool
from ._messages import enrich_messages


async def get_thread_handler(ctx: ToolContext, payload: GetThreadInput) -> list[ChatMessage]:
    """List up to `payload.limit` messages in `payload.thread_name`, oldest-first.

    Uses the Chat API's `filter=thread.name=...` on spaces.messages.list. The
    `space_id` must match the thread's parent space; the API returns 400 otherwise.
    """

    async def body(access_token: str, _user_sub: str) -> list[ChatMessage]:
        raw = await ctx.client.list_messages_by_thread(
            access_token,
            space_id=payload.space_id,
            thread_name=payload.thread_name,
            limit=payload.limit,
        )
        parsed = [_ChatMessageResponse(**r) for r in raw]
        return await enrich_messages(parsed, ctx, access_token)

    return await invoke_tool(
        "get_thread",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_MESSAGES_READONLY,
    )
