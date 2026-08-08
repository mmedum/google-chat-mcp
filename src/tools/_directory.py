"""Shared People API resolution for tool handlers.

`get_messages` (sender → email) and `list_members` (member → email) both
need to turn `users/{id}` into a primary email + display name. The
resolution logic lives here so neither handler reaches into the other's
private helpers.

Enrichment here is **best-effort by contract**: nothing in this module raises,
and every consumer field (`ChatMessage.sender_email`, `Member.email`) is
nullable. That is deliberate — see `resolve_people`.
"""

from __future__ import annotations

import asyncio
from collections.abc import Iterable
from typing import TYPE_CHECKING, Any

from ..chat_client import ChatClient
from ..observability import current_tool, logger, mcp_people_lookup_failures_total

if TYPE_CHECKING:
    from ._common import ToolContext


async def _fetch_person(
    client: ChatClient,
    access_token: str,
    user_id: str,
) -> tuple[str | None, str | None] | None:
    """Hit People API for `users/{id}`; return (email, display_name) or None on 404.

    Private: callers go through `resolve_people`, which adds the cache and the
    degrade. A raising lookup reachable from a handler is what caused the
    empty-result bug this module's contract now rules out.
    """
    data = await client.resolve_person(access_token, user_id)
    if data is None:
        return None
    return primary_email(data), primary_name(data)


async def resolve_people(
    ctx: ToolContext,
    access_token: str,
    user_ids: Iterable[str],
) -> dict[str, tuple[str | None, str | None]]:
    """Resolve many `users/{id}` → (email, display_name) at once. Never raises.

    Batch-shaped on purpose. Every caller resolves a page of senders, members
    or reactors concurrently, so a per-row cache read meant one `aiosqlite`
    connection — and one OS thread — per row, all of them missing before any
    could write back. One query in, one write out, and one People request per
    genuine miss.

    **Why failures degrade instead of propagating.** Email is optional
    enrichment: 404 (external or deleted user) already resolves to `None`, and
    the caller keeps `user_id` + `display_name` from the Chat payload
    regardless. Letting a 403 (`directory.readonly` never granted), 429, 5xx or
    a transport timeout escape meant the caller's
    `gather(return_exceptions=True)` dropped the whole row — so `get_messages`
    and `list_members` returned `[]`, which a calling model reads as *the space
    is empty* rather than *lookups are broken*. Same silent-emptiness shape as
    the `markupSyntax` outage.

    Every id asked for is present in the result, so callers can index without
    a fallback.
    """
    ids = list(dict.fromkeys(user_ids))
    if not ids:
        return {}
    resolved: dict[str, tuple[str | None, str | None]] = dict(await _cache_get_many(ctx, ids))
    misses = [user_id for user_id in ids if user_id not in resolved]
    if misses:
        looked_up = await asyncio.gather(
            *[_lookup_person(ctx, access_token, user_id) for user_id in misses]
        )
        resolved.update(zip(misses, looked_up, strict=True))
        # One write for the page, not one per resolved user: per-row writes
        # opened a connection each and then queued on SQLite's single write
        # lock, and any that timed out were swallowed — so those users were
        # never memoised and the next call repeated the whole fan-out.
        await _cache_put_many(
            ctx,
            [
                (user_id, email, display_name)
                for user_id, (email, display_name) in zip(misses, looked_up, strict=True)
                if email
            ],
        )
    return resolved


async def resolve_person_cached(
    ctx: ToolContext,
    access_token: str,
    user_id: str,
) -> tuple[str | None, str | None]:
    """Single-id form of `resolve_people`. Never raises."""
    return (await resolve_people(ctx, access_token, [user_id]))[user_id]


async def resolve_email_cached(
    ctx: ToolContext,
    access_token: str,
    user_id: str,
) -> str | None:
    """Resolve `users/{id}` → primary email only. Never raises."""
    email, _ = await resolve_person_cached(ctx, access_token, user_id)
    return email


async def _lookup_person(
    ctx: ToolContext,
    access_token: str,
    user_id: str,
) -> tuple[str | None, str | None]:
    """One People fetch, absorbing every failure. The caller does the caching.

    `except Exception` is deliberately broad, not a list of status codes:
    `ChatClient._request` does not wrap transport errors, so `httpx`
    `TimeoutException` / `ConnectError` arrive raw — exactly the flaky-upstream
    case this exists for. The invariant is "nothing here may cost a row", and
    enumerating failure types re-litigates it per exception.
    """
    try:
        fetched = await _fetch_person(ctx.client, access_token, user_id)
    except Exception as exc:
        mcp_people_lookup_failures_total.labels(current_tool.get()).inc()
        # Status as well as type: the runbook asks the operator to tell a 403
        # (scope never granted) from a 429 (quota) from a 5xx, and all three
        # arrive as `ChatApiError`. On stdio this line is the only signal that
        # exists — the metric has no scrape endpoint there. Never the payload,
        # which carries names and emails.
        logger.warning(
            "person_lookup_degraded",
            error=type(exc).__name__,
            status=getattr(exc, "status_code", None),
            google_status=getattr(exc, "google_status", None),
        )
        return None, None
    if fetched is None:
        return None, None
    return fetched


async def _cache_get_many(
    ctx: ToolContext, user_ids: list[str]
) -> dict[str, tuple[str, str | None]]:
    """Directory-cache read that degrades to a total miss. Never raises.

    The cache is SQLite, so it has its own failure modes — locked past
    `busy_timeout`, a full disk, an unparseable `fetched_at`. Those have nothing
    to do with the Chat API, and letting one escape would drop the row exactly
    like the People API failures this module exists to absorb, while also
    firing the schema-drift alert for a local storage fault.
    """
    try:
        return await ctx.directory_cache.get_many(user_ids)
    except Exception as exc:
        logger.warning("directory_cache_unavailable", op="get_many", error=type(exc).__name__)
        return {}


async def _cache_put_many(ctx: ToolContext, rows: list[tuple[str, str, str | None]]) -> None:
    """Directory-cache write that degrades to a no-op. Never raises.

    Failing to memoise costs a round-trip next time; failing the call costs the
    caller their data.
    """
    if not rows:
        return
    try:
        await ctx.directory_cache.put_many_users(rows)
    except Exception as exc:
        logger.warning("directory_cache_unavailable", op="put_many", error=type(exc).__name__)


def primary_email(data: dict[str, Any]) -> str | None:
    """Primary email from a People `Person` payload.

    Passed through as Google sent it. Tool results type these as `str`, not
    `EmailStr`, precisely so no address needs sanitising on the way out — see
    the note above `PersonHit` in `models.py`.
    """
    emails = data.get("emailAddresses")
    if not isinstance(emails, list):
        return None
    return _pick_field(emails, "value")


def primary_name(data: dict[str, Any]) -> str | None:
    names = data.get("names")
    if not isinstance(names, list):
        return None
    return _pick_field(names, "displayName")


def _pick_field(items: list[Any], field: str) -> str | None:
    """Return `field` from the first dict marked primary; else from the first dict."""
    for item in items:
        if not isinstance(item, dict):
            continue
        meta = item.get("metadata")
        if isinstance(meta, dict) and meta.get("primary"):
            value = item.get(field)
            if isinstance(value, str):
                return value
    for item in items:
        if isinstance(item, dict):
            value = item.get(field)
            if isinstance(value, str):
                return value
    return None
