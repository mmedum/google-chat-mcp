"""Tool: get_messages — read recent messages from a space.

Resolves sender ID -> email via People API (cached in SQLite, 24h TTL). Email
may be None for senders who are unresolvable (e.g. external users, deleted
accounts) — the caller still gets display_name and user_id.
"""

from __future__ import annotations

import asyncio
from datetime import UTC

from ..models import (
    ChatMessage,
    GetMessagesInput,
    _ChatMessageResponse,
)
from ..observability import logger
from ..storage import DirectoryCache
from ._common import ToolContext, invoke_tool
from ._directory import fetch_person


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
        # `return_exceptions=True`: one bad People-API lookup shouldn't blank the
        # whole batch. We log the offender and skip it; the user sees partial
        # results rather than a blanket error.
        results = await asyncio.gather(
            *[_enrich_sender(access_token, m, ctx) for m in parsed],
            return_exceptions=True,
        )
        enriched: list[ChatMessage] = []
        for msg, res in zip(parsed, results, strict=True):
            if isinstance(res, BaseException):
                logger.warning(
                    "enrich_sender_failed",
                    sender=msg.sender.name,
                    error=type(res).__name__,
                )
                continue
            enriched.append(res)
        return enriched

    return await invoke_tool(
        "get_messages",
        ctx,
        body,
        target_space_id=payload.space_id,
    )


async def _enrich_sender(
    access_token: str,
    msg: _ChatMessageResponse,
    ctx: ToolContext,
) -> ChatMessage:
    email, display_name = await _resolve_sender(access_token, msg, ctx.directory_cache, ctx)
    create_time = msg.create_time
    ts = create_time.astimezone(UTC) if create_time.tzinfo else create_time.replace(tzinfo=UTC)
    return ChatMessage(
        message_id=msg.name,
        sender_user_id=msg.sender.name,
        sender_email=email,
        sender_display_name=display_name or msg.sender.display_name,
        text=msg.text,
        timestamp=ts,
        thread_id=msg.thread.name,
    )


async def _resolve_sender(
    access_token: str,
    msg: _ChatMessageResponse,
    cache: DirectoryCache,
    ctx: ToolContext,
) -> tuple[str | None, str | None]:
    user_id = msg.sender.name
    cached = await cache.get(user_id)
    if cached is not None:
        return cached
    fetched = await fetch_person(ctx.client, access_token, user_id)
    if fetched is None:
        return None, msg.sender.display_name
    email, display_name = fetched
    if email:
        await cache.put(user_id, email, display_name)
    return email, display_name or msg.sender.display_name
