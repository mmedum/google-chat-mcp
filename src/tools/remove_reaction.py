"""Tool: remove_reaction — delete a reaction by name or by (emoji, user) filter."""

from __future__ import annotations

from ..models import RemoveReactionInput, RemoveReactionResult
from ._common import CHAT_MESSAGES_REACTIONS, ToolContext, invoke_tool
from ._directory import fetch_person


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
    space_id = _space_from_message_name(payload.message_name or payload.reaction_name or "")

    async def body(access_token: str, _user_sub: str) -> RemoveReactionResult:
        if payload.reaction_name is not None:
            await ctx.client.delete_reaction(access_token, payload.reaction_name)
            return RemoveReactionResult(reaction_name=payload.reaction_name, removed=True)

        # Lookup-by-filter path. Input validator guarantees these are all set.
        assert payload.message_name is not None
        assert payload.emoji is not None
        assert payload.user_email is not None

        # Resolve the email to a Google user ID via the People API (cached in
        # user_directory). The reactions list filter expects `user.name = "users/{id}"`.
        user_id = await _resolve_email_to_user_id(ctx, access_token, payload.user_email)
        if user_id is None:
            return RemoveReactionResult(reaction_name=None, removed=False)

        listed = await ctx.client.list_reactions(
            access_token,
            message_name=payload.message_name,
            limit=1,
            emoji_filter=payload.emoji,
            user_filter=user_id,
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


def _space_from_message_name(name: str) -> str:
    parts = name.split("/")
    return "/".join(parts[:2]) if len(parts) >= 2 else ""


async def _resolve_email_to_user_id(ctx: ToolContext, access_token: str, email: str) -> str | None:
    """Look up `users/{id}` from an email via People API (search, not cache)."""
    # The directory_cache is keyed by user_id, not email — we need the reverse
    # lookup. Google's People API `people:searchDirectoryPeople` does it, but
    # we keep the chat_client surface minimal; hit People API with a query.
    result = await fetch_person(ctx.client, access_token, f"users/{email}")
    # fetch_person resolves `users/{email}` shaped IDs in some Workspace configs.
    # If that fails, the reaction lookup returns no match — acceptable for v2.
    if result is None:
        return None
    # fetch_person returns (email, display_name) — no user_id. The Chat API's
    # reaction filter accepts `user.name = "users/{email}"` too in some paths;
    # if that stops working, widen fetch_person to return user_id.
    return f"users/{email}"
