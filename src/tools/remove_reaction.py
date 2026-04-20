"""Tool: remove_reaction — delete a reaction by name or by (emoji, user) filter."""

from __future__ import annotations

from ..models import RemoveReactionInput, RemoveReactionResult, _ChatReactionsListResponse
from ._common import (
    CHAT_MESSAGES_REACTIONS,
    ToolContext,
    invoke_tool,
    space_id_from_message_name,
)
from ._directory import fetch_person


async def remove_reaction_handler(
    ctx: ToolContext, payload: RemoveReactionInput
) -> RemoveReactionResult:
    """Delete a reaction.

    Two shapes (mutually exclusive, enforced by the input model's validator):
    - `reaction_name` — direct DELETE by full resource name.
    - `(message_name, emoji, user_email)` — server-side filter on emoji only,
      then walk each returned reaction, resolve `user.name` (`users/{sub}`)
      to its primary email via People API, and DELETE the first match. The
      Chat API's reactions.list filter requires a numeric `users/{sub}` on
      the `user.name` predicate and returns 500 on `users/{email}`; we can't
      do the match server-side without a sub, and `user_email` is the
      caller's natural handle. No-match returns removed=False with
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

        listed = await ctx.client.list_reactions(
            access_token,
            message_name=payload.message_name,
            limit=200,
            emoji_filter=payload.emoji,
        )
        parsed = _ChatReactionsListResponse(**listed)
        target_email = payload.user_email.lower()
        for reaction in parsed.reactions:
            resolved = await fetch_person(ctx.client, access_token, reaction.user.name)
            if resolved is None:
                continue
            email, _name = resolved
            if email is not None and email.lower() == target_email:
                await ctx.client.delete_reaction(access_token, reaction.name)
                return RemoveReactionResult(reaction_name=reaction.name, removed=True)
        return RemoveReactionResult(reaction_name=None, removed=False)

    return await invoke_tool(
        "remove_reaction",
        ctx,
        body,
        target_space_id=space_id or None,
        required_scope=CHAT_MESSAGES_REACTIONS,
    )
