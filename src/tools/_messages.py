"""Shared sender-resolution + timestamp-coerce for message-returning tools."""

from __future__ import annotations

from datetime import UTC, datetime

from ..models import ChatMessage, _ChatMessageResponse
from ..observability import logger, mcp_schema_drift_total
from ._common import ToolContext, drift_fields
from ._directory import resolve_people, resolve_person_cached


def ensure_utc(ts: datetime) -> datetime:
    """Return `ts` in UTC, treating naive timestamps as already-UTC."""
    return ts.astimezone(UTC) if ts.tzinfo else ts.replace(tzinfo=UTC)


async def resolve_sender(
    ctx: ToolContext,
    access_token: str,
    msg: _ChatMessageResponse,
) -> tuple[str | None, str | None]:
    """Resolve `msg.sender.name` to `(email, display_name)`. Never raises.

    Falls back to the display name Chat already gave us on the message, so a
    failed lookup costs the email and nothing else.
    """
    email, display_name = await resolve_person_cached(ctx, access_token, msg.sender.name)
    return email, display_name or msg.sender.display_name


async def enrich_messages(
    parsed: list[_ChatMessageResponse],
    ctx: ToolContext,
    access_token: str,
) -> list[ChatMessage]:
    """Resolve senders, keeping every message.

    Lookups are per *unique sender*, not per message, and the cache is read
    for the whole page in one query. Resolving per message meant a 50-message
    thread between three people issued 50 concurrent People calls, each with
    its own SQLite connection, all missing before the first `cache.put` landed.

    A People API failure no longer costs a row — `resolve_person_cached`
    degrades to a null email instead, and never raises. The only way to lose a
    message here is `ChatMessage` failing to validate, i.e. a field we *read*
    drifted. That is real drift, so it is counted rather than just logged: a
    dropped row shortens the list with no other signal, and the caller reads
    the short list as the whole conversation.
    """
    by_sender = await resolve_people(ctx, access_token, (m.sender.name for m in parsed))

    enriched: list[ChatMessage] = []
    for msg in parsed:
        email, display_name = by_sender.get(msg.sender.name, (None, None))
        try:
            enriched.append(
                ChatMessage(
                    message_id=msg.name,
                    sender_user_id=msg.sender.name,
                    sender_email=email,
                    sender_display_name=display_name or msg.sender.display_name,
                    text=msg.text,
                    timestamp=ensure_utc(msg.create_time),
                    thread_id=msg.thread.name,
                )
            )
        except (TypeError, ValueError) as exc:
            logger.warning(
                "enrich_sender_failed",
                sender=msg.sender.name,
                error=type(exc).__name__,
                fields=drift_fields(exc),
            )
            # `location` labels a code site, not a caller — every other use of
            # this metric is a literal. Interpolating the tool would split one
            # drifted `ChatMessage` field across three series and fragment the
            # alert the runbook tells operators to set.
            mcp_schema_drift_total.labels("enrich_messages.dropped_row").inc()
    return enriched
