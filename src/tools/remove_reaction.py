"""Tool: remove_reaction — delete a reaction by name or by (emoji, user) filter."""

from __future__ import annotations

from ..models import RemoveReactionInput, RemoveReactionResult
from ._common import (
    CHAT_MESSAGES_REACTIONS,
    ToolContext,
    invoke_tool,
    space_id_from_message_name,
)


async def remove_reaction_handler(
    ctx: ToolContext, payload: RemoveReactionInput
) -> RemoveReactionResult:
    """Delete a reaction.

    Two shapes (mutually exclusive, enforced by the input model's validator):
    - `reaction_name` — direct DELETE by full resource name.
    - `(message_name, emoji, user_email)` — server-side filtered list
      (`emoji.unicode = "..." AND user.name = "users/..."`) then DELETE the
      single returned resource. No-match returns removed=False with
      reaction_name=None so the caller can distinguish "already gone".
    """
    space_id = space_id_from_message_name(payload.message_name or payload.reaction_name or "")

    async def body(access_token: str, _user_sub: str) -> RemoveReactionResult:
        if payload.reaction_name is not None:
            await ctx.client.delete_reaction(access_token, payload.reaction_name)
            return RemoveReactionResult(reaction_name=payload.reaction_name, removed=True)

        # Lookup-by-filter path. Input validator guarantees these are all set.
        assert payload.message_name is not None
        assert payload.emoji is not None
        assert payload.user_email is not None

        # Chat API's reactions.list filter accepts `user.name = "users/{email}"`
        # directly — no People-API round-trip needed to resolve the email first.
        listed = await ctx.client.list_reactions(
            access_token,
            message_name=payload.message_name,
            limit=1,
            emoji_filter=payload.emoji,
            user_filter=f"users/{payload.user_email}",
        )
        reactions = listed.get("reactions", [])
        if not isinstance(reactions, list) or not reactions:
            return RemoveReactionResult(reaction_name=None, removed=False)
        first = reactions[0]
        if not isinstance(first, dict) or not isinstance(first.get("name"), str):
            return RemoveReactionResult(reaction_name=None, removed=False)
        reaction_name = str(first["name"])
        await ctx.client.delete_reaction(access_token, reaction_name)
        return RemoveReactionResult(reaction_name=reaction_name, removed=True)

    return await invoke_tool(
        "remove_reaction",
        ctx,
        body,
        target_space_id=space_id or None,
        required_scope=CHAT_MESSAGES_REACTIONS,
    )
