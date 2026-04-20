"""Tool: add_reaction — add a unicode-emoji reaction to a message."""

from __future__ import annotations

from ..models import AddReactionInput, AddReactionResult, _ChatReactionResponse
from ._common import CHAT_MESSAGES_REACTIONS, ToolContext, invoke_tool, space_id_from_message_name


async def add_reaction_handler(ctx: ToolContext, payload: AddReactionInput) -> AddReactionResult:
    """Add a reaction. Idempotent — re-adding the same (emoji, user) is a no-op on the Chat API."""
    space_id = space_id_from_message_name(payload.message_name)

    async def body(access_token: str, _user_sub: str) -> AddReactionResult:
        raw = await ctx.client.add_reaction(access_token, payload.message_name, payload.emoji)
        parsed = _ChatReactionResponse(**raw)
        return AddReactionResult(
            reaction_name=parsed.name,
            emoji=parsed.emoji.display or payload.emoji,
            user_id=parsed.user.name,
        )

    return await invoke_tool(
        "add_reaction",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES_REACTIONS,
    )
