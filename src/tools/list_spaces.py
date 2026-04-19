"""Tool: list_spaces — list Chat spaces the authenticated user belongs to."""

from __future__ import annotations

from ..models import ListSpacesInput, SpaceSummary, _ChatSpaceResponse, _ChatSpacesListResponse
from ._common import ToolContext, invoke_tool


async def list_spaces_handler(ctx: ToolContext, payload: ListSpacesInput) -> list[SpaceSummary]:
    """List Chat spaces (DMs, group chats, named spaces) the user is in."""

    async def body(access_token: str, _user_sub: str) -> list[SpaceSummary]:
        raw = await ctx.client.list_spaces(
            access_token,
            limit=payload.limit,
            space_type=payload.space_type,
        )
        spaces = _ChatSpacesListResponse(spaces=[_ChatSpaceResponse(**r) for r in raw]).spaces
        return [
            SpaceSummary(
                space_id=s.name,
                type=s.type_,
                display_name=s.display_name or _fallback_name(s),
            )
            for s in spaces
        ]

    return await invoke_tool("list_spaces", ctx, body)


def _fallback_name(s: _ChatSpaceResponse) -> str:
    if s.type_ == "DIRECT_MESSAGE":
        return "(direct message)"
    if s.type_ == "GROUP_CHAT":
        return "(group chat)"
    return "(unnamed space)"
