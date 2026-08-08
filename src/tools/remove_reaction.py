"""Tool: remove_reaction — delete a reaction by name or by (emoji, user) filter."""

from __future__ import annotations

import asyncio

from fastmcp.exceptions import ToolError

from ..models import RemoveReactionInput, RemoveReactionResult, _ChatReactionsListResponse
from ._common import (
    CHAT_MESSAGES_REACTIONS,
    ToolContext,
    invoke_tool,
    space_id_from_message_name,
)
from ._directory import resolve_email_cached


async def remove_reaction_handler(
    ctx: ToolContext, payload: RemoveReactionInput
) -> RemoveReactionResult:
    """Delete a reaction.

    Two shapes (mutually exclusive, enforced by the input model's validator):
    - `reaction_name` — direct DELETE by full resource name.
    - `(message_name, emoji, user_email)` — server-side filter on emoji, then
      resolve each returned reactor's email via People API (concurrent,
      cache-deduped) and DELETE the first email match. Chat API's reactions.list
      filter requires a numeric `users/{sub}` on the `user.name` predicate and
      500s on `users/{email}`, so the match has to happen client-side. No-match
      returns `removed=False` with `reaction_name=None` so the caller can
      distinguish "already gone" — but only when every reactor's email actually
      resolved. If any did not, the tool raises instead: an unresolved reactor
      means a match cannot be ruled out, and reporting "already gone" there
      would be a confident wrong answer.
    """
    space_id = space_id_from_message_name(payload.message_name or payload.reaction_name or "")

    async def body(access_token: str, _user_sub: str) -> RemoveReactionResult:
        if payload.reaction_name is not None:
            await ctx.client.delete_reaction(access_token, payload.reaction_name)
            return RemoveReactionResult(reaction_name=payload.reaction_name, removed=True)

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
        if not parsed.reactions:
            return RemoveReactionResult(reaction_name=None, removed=False)

        # No `return_exceptions`: `resolve_email_cached` degrades rather than
        # raising, so the old exception branch was dead — and worse than dead,
        # since it would silently swallow anything raising added here later.
        emails = await asyncio.gather(
            *(resolve_email_cached(ctx, access_token, r.user.name) for r in parsed.reactions),
        )
        target_email = payload.user_email.lower()
        for reaction, email in zip(parsed.reactions, emails, strict=True):
            if email is None:
                continue
            if email.lower() == target_email:
                await ctx.client.delete_reaction(access_token, reaction.name)
                return RemoveReactionResult(reaction_name=reaction.name, removed=True)
        if any(email is None for email in emails):
            # `removed=False` is documented as "already gone", and a caller —
            # usually a model — stops there. It must not also mean "we could
            # not tell": some reactor's address did not resolve, so the target
            # may well still be reacting. Say so instead of reporting an
            # absence we never established.
            raise ToolError(
                f"Could not verify whether {payload.user_email} reacted with "
                f"{payload.emoji}: at least one reactor's email did not resolve, so a "
                "match cannot be ruled out. Nothing was deleted. Fetch the reaction "
                "via `list_reactions` and delete it by `reaction_name` instead."
            )
        return RemoveReactionResult(reaction_name=None, removed=False)

    return await invoke_tool(
        "remove_reaction",
        ctx,
        body,
        target_space_id=space_id or None,
        required_scope=CHAT_MESSAGES_REACTIONS,
    )
