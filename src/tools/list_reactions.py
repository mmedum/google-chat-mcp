"""Tool: list_reactions — reactions on a message, paginated."""

from __future__ import annotations

from typing import Any

from ..models import ListReactionsInput, ListReactionsResult, ReactionEntry
from ._common import CHAT_MESSAGES_READONLY, ToolContext, invoke_tool, space_id_from_message_name


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
        entries = _parse_reactions(raw.get("reactions", []))
        next_token = raw.get("nextPageToken")
        return ListReactionsResult(
            reactions=entries,
            next_page_token=str(next_token) if isinstance(next_token, str) and next_token else None,
        )

    return await invoke_tool(
        "list_reactions",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES_READONLY,
    )


def _parse_reactions(raw: Any) -> list[ReactionEntry]:
    out: list[ReactionEntry] = []
    if not isinstance(raw, list):
        return out
    for r in raw:
        if not isinstance(r, dict):
            continue
        name = r.get("name")
        emoji_obj = r.get("emoji")
        user_obj = r.get("user")
        if not isinstance(name, str) or not isinstance(emoji_obj, dict):
            continue
        emoji = emoji_obj.get("unicode")
        if not isinstance(emoji, str):
            continue
        user_name = user_obj.get("name") if isinstance(user_obj, dict) else None
        if not isinstance(user_name, str):
            continue
        out.append(ReactionEntry(reaction_name=name, emoji=emoji, user_id=user_name))
    return out
