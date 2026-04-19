"""Shared sender-resolution helper for message-returning tools (get_messages, get_thread)."""

from __future__ import annotations

import asyncio
from datetime import UTC

from ..models import ChatMessage, _ChatMessageResponse
from ..observability import logger
from ..storage import DirectoryCache
from ._common import ToolContext
from ._directory import fetch_person


async def enrich_messages(
    parsed: list[_ChatMessageResponse],
    ctx: ToolContext,
    access_token: str,
) -> list[ChatMessage]:
    """Resolve senders to email + display name in parallel; drop rows that fail lookup.

    `return_exceptions=True`: one bad People-API lookup mustn't blank the whole
    batch. Failures are logged and skipped — the caller sees a partial result.
    """
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
