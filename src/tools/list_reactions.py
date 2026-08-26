"""Tool: list_reactions — reactions on a message, paginated."""

from __future__ import annotations

from ..models import (
    ListReactionsInput,
    ListReactionsResult,
    ReactionEntry,
    _ChatReactionsListResponse,
)
from ._common import (
    CHAT_MESSAGES_REACTIONS,
    CHAT_MESSAGES_READONLY,
    ToolContext,
    invoke_tool,
    space_id_from_message_name,
)


async def list_reactions_handler(
    ctx: ToolContext, payload: ListReactionsInput
) -> ListReactionsResult:
    """List reactions on a message. Pagination via page_token / next_page_token."""
    space_id = space_id_from_message_name(payload.message_name)

    async def body(access_token: str, _user_sub: str) -> ListReactionsResult:
        raw = await ctx.client.list_reactions(
            access_token,
            message_name=payload.message_name,
            limit=payload.limit,
            page_token=payload.page_token,
        )
        parsed = _ChatReactionsListResponse(**raw)
        entries = [
            ReactionEntry(
                reaction_name=r.name,
                emoji=r.emoji.display,
                user_id=r.user.name,
            )
            for r in parsed.reactions
            if r.emoji.display is not None
        ]
        return ListReactionsResult(
            reactions=entries,
            next_page_token=parsed.next_page_token,
        )

    return await invoke_tool(
        "list_reactions",
        ctx,
        body,
        target_space_id=space_id,
        # `required_scope` stays on the sensitive-tier reactions scope: it is
        # the one named in the re-auth prompt, and v0.4.0 moved it here
        # deliberately so a deployer who declined the restricted umbrella is
        # not pushed back into that tier by the prompt.
        #
        # `chat.messages.readonly` is a genuine per-method exception. Google
        # accepts it for reactions.list even though it is not an umbrella of
        # the reactions scope, so `SCOPE_IMPLIES` cannot express it. The
        # umbrella `chat.messages` is *not* listed — SCOPE_IMPLIES already
        # covers it, and repeating it here would be a second place to update.
        required_scope=CHAT_MESSAGES_REACTIONS,
        also_accepts=(CHAT_MESSAGES_READONLY,),
    )
