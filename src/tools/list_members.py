"""Tool: list_members — list members of a Chat space.

Returns both human members (with People-API email resolution) and Google
Group members. Email resolution reuses the `ToolContext.directory_cache`
that `get_messages` also populates.
"""

from __future__ import annotations

from ..models import (
    ListMembersInput,
    Member,
    _ChatMembershipResponse,
    _ChatMembershipsListResponse,
)
from ..observability import logger, mcp_schema_drift_total
from ._common import (
    CHAT_MEMBERSHIPS_READONLY,
    ToolContext,
    invoke_tool,
    member_role_out,
    member_state_out,
)
from ._directory import resolve_people


async def list_members_handler(ctx: ToolContext, payload: ListMembersInput) -> list[Member]:
    """List up to `payload.limit` members of `payload.space_id`."""

    async def body(access_token: str, _user_sub: str) -> list[Member]:
        raw = await ctx.client.list_members(
            access_token, space_id=payload.space_id, limit=payload.limit
        )
        parsed = _ChatMembershipsListResponse(
            memberships=[_ChatMembershipResponse(**r) for r in raw]
        ).memberships
        # One batched cache read plus one People request per genuine miss, for
        # the whole page. A failed lookup surfaces as email=None for that
        # member, not a dropped row (see `resolve_people`), so the failure
        # branch below now means genuine drift only.
        by_member = await resolve_people(
            ctx, access_token, (m.member.name for m in parsed if m.member is not None)
        )
        out: list[Member] = []
        for m in parsed:
            try:
                out.append(_to_member(m, by_member))
            except (TypeError, ValueError) as exc:
                logger.warning(
                    "list_members_enrich_failed",
                    membership=m.name,
                    error=type(exc).__name__,
                )
                # A dropped row shortens the list with no signal to the caller,
                # who reads it as the complete membership. `_to_member` raises
                # only when neither `member` nor `groupMember` is set — i.e.
                # when Google adds a third kind of member — so this is a drift
                # symptom, never a failed People API lookup, and it has to
                # reach the metric operators alert on.
                mcp_schema_drift_total.labels("list_members.dropped_row").inc()
        return out

    return await invoke_tool(
        "list_members",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_MEMBERSHIPS_READONLY,
    )


def _to_member(
    m: _ChatMembershipResponse,
    by_member: dict[str, tuple[str | None, str | None]],
) -> Member:
    role = member_role_out(m.role)
    state = member_state_out(m.state)
    if m.member is not None:
        email, display_name = by_member.get(m.member.name, (None, None))
        return Member(
            kind="HUMAN",
            member_id=m.member.name,
            display_name=display_name or m.member.display_name,
            email=email,
            role=role,
            state=state,
        )
    if m.group_member is not None:
        return Member(
            kind="GROUP",
            member_id=m.group_member.name,
            display_name=m.group_member.display_name,
            email=None,
            role=role,
            state=state,
        )
    # Should not happen — Google always populates exactly one of the two.
    raise ValueError(f"Membership {m.name!r} has neither member nor groupMember")
