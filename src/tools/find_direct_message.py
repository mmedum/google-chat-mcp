"""Tool: find_direct_message — resolve a user email to a DM space ID.

Creates the DM on the fly if none exists (requires `chat.spaces.create`).
"""

from __future__ import annotations

from fastmcp.exceptions import ToolError

from ..models import DirectMessageResult, _ChatSpaceResponse
from ._common import ToolContext, invoke_tool


async def find_direct_message_handler(ctx: ToolContext, user_email: str) -> DirectMessageResult:
    """Find or create a DM space with `user_email`. Returns the space ID."""

    async def body(access_token: str, _user_sub: str) -> DirectMessageResult:
        found = await ctx.client.find_direct_message(access_token, user_email)
        if found is not None:
            space = _ChatSpaceResponse(**found)
            return DirectMessageResult(space_id=space.name)
        try:
            created = await ctx.client.create_dm(access_token, user_email)
        except Exception as exc:
            raise ToolError(
                f"Could not find or create DM with {user_email}. "
                f"Is the user in your Workspace directory?"
            ) from exc
        space = _ChatSpaceResponse(**created)
        return DirectMessageResult(space_id=space.name)

    return await invoke_tool("find_direct_message", ctx, body)
