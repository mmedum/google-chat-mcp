"""Tool: add_reaction — add a unicode-emoji reaction to a message."""

from __future__ import annotations

from ..chat_client import ChatApiError
from ..models import (
    AddReactionInput,
    AddReactionResult,
    _ChatReactionResponse,
    _ChatReactionsListResponse,
)
from ._common import CHAT_MESSAGES_REACTIONS, ToolContext, invoke_tool, space_id_from_message_name


async def add_reaction_handler(ctx: ToolContext, payload: AddReactionInput) -> AddReactionResult:
    """Add a reaction — presented as idempotent.

    Chat API returns 409 on a duplicate (emoji, user, message) rather than
    no-op'ing; on 409 we recover by resolving the existing reaction via a
    server-side-filtered `reactions.list` on (emoji.unicode, user.name).
    """
    space_id = space_id_from_message_name(payload.message_name)

    async def body(access_token: str, user_sub: str) -> AddReactionResult:
        try:
            raw = await ctx.client.add_reaction(access_token, payload.message_name, payload.emoji)
        except ChatApiError as exc:
            if exc.status_code != 409:
                raise
            listed = await ctx.client.list_reactions(
                access_token,
                message_name=payload.message_name,
                limit=1,
                emoji_filter=payload.emoji,
                user_filter=f"users/{user_sub}",
            )
            existing = _ChatReactionsListResponse(**listed).reactions
            if not existing:
                raise
            reaction = existing[0]
            return AddReactionResult(
                reaction_name=reaction.name,
                emoji=reaction.emoji.display or payload.emoji,
                user_id=reaction.user.name,
            )
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
