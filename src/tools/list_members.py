"""Tool: list_members — list members of a Chat space.

Returns both human members (with People-API email resolution) and Google
Group members. Email resolution reuses the `ToolContext.directory_cache`
that `get_messages` also populates.
"""

from __future__ import annotations

import asyncio

from ..models import (
    ListMembersInput,
    Member,
    _ChatMembershipResponse,
    _ChatMembershipsListResponse,
)
from ..observability import logger
from ..storage import DirectoryCache
from ._common import CHAT_MEMBERSHIPS_READONLY, ToolContext, invoke_tool
from ._directory import fetch_person


async def list_members_handler(ctx: ToolContext, payload: ListMembersInput) -> list[Member]:
    """List up to `payload.limit` members of `payload.space_id`."""

    async def body(access_token: str, _user_sub: str) -> list[Member]:
        raw = await ctx.client.list_members(
            access_token, space_id=payload.space_id, limit=payload.limit
        )
        parsed = _ChatMembershipsListResponse(
            memberships=[_ChatMembershipResponse(**r) for r in raw]
        ).memberships
        # One People lookup per unique human member ID — gathered to parallelise
        # the cold-cache path. Exceptions don't blank the batch: a single failed
        # lookup surfaces as email=None for that member, not a dropped row.
        results = await asyncio.gather(
            *[_to_member(access_token, m, ctx) for m in parsed],
            return_exceptions=True,
        )
        out: list[Member] = []
        for m, res in zip(parsed, results, strict=True):
            if isinstance(res, BaseException):
                logger.warning(
                    "list_members_enrich_failed",
                    membership=m.name,
                    error=type(res).__name__,
                )
                continue
            out.append(res)
        return out

    return await invoke_tool(
        "list_members",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_MEMBERSHIPS_READONLY,
    )


async def _to_member(
    access_token: str,
    m: _ChatMembershipResponse,
    ctx: ToolContext,
) -> Member:
    role = m.role or "ROLE_UNSPECIFIED"
    if m.member is not None:
        email, display_name = await _resolve_human(
            access_token, m.member.name, ctx.directory_cache, ctx
        )
        return Member(
            kind="HUMAN",
            member_id=m.member.name,
            display_name=display_name or m.member.display_name,
            email=email,
            role=role,
            state=m.state,
        )
    if m.group_member is not None:
        return Member(
            kind="GROUP",
            member_id=m.group_member.name,
            display_name=m.group_member.display_name,
            email=None,
            role=role,
            state=m.state,
        )
    # Should not happen — Google always populates exactly one of the two.
    raise ValueError(f"Membership {m.name!r} has neither member nor groupMember")


async def _resolve_human(
    access_token: str,
    user_id: str,
    cache: DirectoryCache,
    ctx: ToolContext,
) -> tuple[str | None, str | None]:
    cached = await cache.get(user_id)
    if cached is not None:
        return cached
    fetched = await fetch_person(ctx.client, access_token, user_id)
    if fetched is None:
        return None, None
    email, display_name = fetched
    if email:
        await cache.put(user_id, email, display_name)
    return email, display_name
