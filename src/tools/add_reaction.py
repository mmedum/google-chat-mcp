"""Tool: add_reaction — add a unicode-emoji reaction to a message."""

from __future__ import annotations

from ..models import AddReactionInput, AddReactionResult
from ._common import CHAT_MESSAGES_REACTIONS, ToolContext, invoke_tool


async def add_reaction_handler(ctx: ToolContext, payload: AddReactionInput) -> AddReactionResult:
    """Add a reaction. Idempotent — re-adding the same (emoji, user) is a no-op on the Chat API."""
    # spaces/{SPACE}/messages/{MSG} → "spaces/{SPACE}". Audit tagging only.
    parts = payload.message_name.split("/")
    space_id = "/".join(parts[:2]) if len(parts) >= 2 else payload.message_name

    async def body(access_token: str, _user_sub: str) -> AddReactionResult:
        raw = await ctx.client.add_reaction(access_token, payload.message_name, payload.emoji)
        # The upstream payload: {name, user: {name}, emoji: {unicode}}.
        emoji_obj = raw.get("emoji") or {}
        user_obj = raw.get("user") or {}
        emoji_unicode = emoji_obj.get("unicode") if isinstance(emoji_obj, dict) else None
        user_name = user_obj.get("name") if isinstance(user_obj, dict) else None
        return AddReactionResult(
            reaction_name=str(raw["name"]),
            emoji=str(emoji_unicode) if emoji_unicode else payload.emoji,
            user_id=str(user_name) if user_name else "users/me",
        )

    return await invoke_tool(
        "add_reaction",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES_REACTIONS,
    )
