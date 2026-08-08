"""Tool: add_reaction — add a unicode-emoji reaction to a message."""

from __future__ import annotations

from ..chat_client import ChatApiError
from ..models import (
    AddReactionInput,
    AddReactionResult,
    _ChatEmoji,
    _ChatReactionsListResponse,
)
from ._common import (
    CHAT_MESSAGES_REACTIONS,
    ToolContext,
    invoke_tool,
    space_id_from_message_name,
    write_result,
)


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
        return write_result(
            lambda: AddReactionResult(
                reaction_name=raw["name"],
                emoji=_emoji_display(raw.get("emoji"), fallback=payload.emoji),
                user_id=raw["user"]["name"],
            ),
            action="add_reaction",
        )

    return await invoke_tool(
        "add_reaction",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES_REACTIONS,
    )


def _emoji_display(raw_emoji: object, *, fallback: str) -> str:
    """`_ChatEmoji.display` semantics, degrading to the requested glyph on drift.

    The reaction already exists by the time this runs, so an unfamiliar field
    on the emoji object must not fail the call. The glyph the caller asked for
    is a good enough answer when Google's canonical form is unreadable.
    """
    if not isinstance(raw_emoji, dict):
        return fallback
    try:
        return _ChatEmoji.model_validate(raw_emoji).display or fallback
    except (TypeError, ValueError):
        return fallback
