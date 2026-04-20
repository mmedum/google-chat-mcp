"""Shared People API resolution for tool handlers.

`get_messages` (sender → email) and `list_members` (member → email) both
need to turn `users/{id}` into a primary email + display name. The
resolution logic lives here so neither handler reaches into the other's
private helpers.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from ..chat_client import ChatClient

if TYPE_CHECKING:
    from ._common import ToolContext


async def fetch_person(
    client: ChatClient,
    access_token: str,
    user_id: str,
) -> tuple[str | None, str | None] | None:
    """Hit People API for `users/{id}`; return (email, display_name) or None on 404."""
    data = await client.resolve_person(access_token, user_id)
    if data is None:
        return None
    return primary_email(data), primary_name(data)


async def resolve_email_cached(
    ctx: ToolContext,
    access_token: str,
    user_id: str,
) -> str | None:
    """Resolve `users/{id}` → primary email, using `ctx.directory_cache` to dedup
    People-API round-trips across callers in the same process.
    """
    cached = await ctx.directory_cache.get(user_id)
    if cached is not None:
        email, _ = cached
        return email
    fetched = await fetch_person(ctx.client, access_token, user_id)
    if fetched is None:
        return None
    email, display_name = fetched
    if email:
        await ctx.directory_cache.put(user_id, email, display_name)
    return email


def primary_email(data: dict[str, Any]) -> str | None:
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
