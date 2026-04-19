"""Tool: get_space — fetch a single Chat space by ID."""

from __future__ import annotations

from ..models import SpaceDetails, _ChatSpaceResponse
from ._common import ToolContext, invoke_tool


async def get_space_handler(ctx: ToolContext, space_id: str) -> SpaceDetails:
    """Fetch a single space. Useful for resolving unknown space IDs."""

    async def body(access_token: str, _user_sub: str) -> SpaceDetails:
        raw = await ctx.client.get_space(access_token, space_id)
        s = _ChatSpaceResponse(**raw)
        return SpaceDetails(
            space_id=s.name,
            type=s.type_,
            display_name=s.display_name or _fallback_name(s),
            single_user_bot_dm=s.single_user_bot_dm,
            external_user_allowed=s.external_user_allowed,
            create_time=s.create_time,
        )

    return await invoke_tool("get_space", ctx, body, target_space_id=space_id)


def _fallback_name(s: _ChatSpaceResponse) -> str:
    if s.type_ == "DIRECT_MESSAGE":
        return "(direct message)"
    if s.type_ == "GROUP_CHAT":
        return "(group chat)"
    return "(unnamed space)"
