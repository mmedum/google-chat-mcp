"""Tool: get_messages — read recent messages from a space.

Resolves sender ID -> email via People API (cached in SQLite, 24h TTL). Email
may be None for senders who are unresolvable (e.g. external users, deleted
accounts) — the caller still gets display_name and user_id.
"""

from __future__ import annotations

from datetime import UTC

from ..models import (
    ChatMessage,
    GetMessagesInput,
    _ChatMessageResponse,
)
from ._common import CHAT_MESSAGES_READONLY, ToolContext, invoke_tool
from ._messages import enrich_messages


async def get_messages_handler(ctx: ToolContext, payload: GetMessagesInput) -> list[ChatMessage]:
    """Read up to `payload.limit` messages from `space_id`, newest first."""

    async def body(access_token: str, _user_sub: str) -> list[ChatMessage]:
        since_iso = (
            payload.since.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
            if payload.since
            else None
        )
        raw_messages = await ctx.client.list_messages(
            access_token,
            space_id=payload.space_id,
            limit=payload.limit,
            since_iso=since_iso,
        )
        parsed = [_ChatMessageResponse(**r) for r in raw_messages]
        return await enrich_messages(parsed, ctx, access_token)

    return await invoke_tool(
        "get_messages",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_MESSAGES_READONLY,
    )
