"""Shared sender-resolution + timestamp-coerce for message-returning tools."""

from __future__ import annotations

import asyncio
from datetime import UTC, datetime

from ..models import ChatMessage, _ChatMessageResponse
from ..observability import logger
from ._common import ToolContext
from ._directory import fetch_person


def ensure_utc(ts: datetime) -> datetime:
    """Return `ts` in UTC, treating naive timestamps as already-UTC."""
    return ts.astimezone(UTC) if ts.tzinfo else ts.replace(tzinfo=UTC)


async def resolve_sender(
    ctx: ToolContext,
    access_token: str,
    msg: _ChatMessageResponse,
) -> tuple[str | None, str | None]:
    """Resolve `msg.sender.name` to `(email, display_name)` via the directory cache."""
    user_id = msg.sender.name
    cache = ctx.directory_cache
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


async def enrich_messages(
    parsed: list[_ChatMessageResponse],
    ctx: ToolContext,
    access_token: str,
) -> list[ChatMessage]:
    """Resolve senders in parallel; drop rows whose People-API lookup raises.

    `return_exceptions=True`: one bad lookup mustn't blank the whole batch.
    Failures are logged and skipped — the caller sees a partial result.
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
    email, display_name = await resolve_sender(ctx, access_token, msg)
    return ChatMessage(
        message_id=msg.name,
        sender_user_id=msg.sender.name,
        sender_email=email,
        sender_display_name=display_name or msg.sender.display_name,
        text=msg.text,
        timestamp=ensure_utc(msg.create_time),
        thread_id=msg.thread.name,
    )
