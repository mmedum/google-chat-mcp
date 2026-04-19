"""Tool: get_message — fetch one message by resource name, with reactions inline."""

from __future__ import annotations

from datetime import UTC

from ..models import (
    MessageDetails,
    ReactionSummary,
    _ChatMessageResponse,
)
from ._common import CHAT_MESSAGES_READONLY, ToolContext, invoke_tool
from ._directory import fetch_person

# If a message has more distinct reaction emoji than this, omit the inline
# summary and set `reactions_paged=True` so the caller knows to use
# `list_reactions` for full detail. Google's emojiReactionSummaries typically
# stays small, so this is a forward-compat guard, not a common path.
_INLINE_REACTIONS_CAP = 25


async def get_message_handler(ctx: ToolContext, message_name: str) -> MessageDetails:
    """Fetch a single message. `message_name` is the full `spaces/{s}/messages/{m}`.

    `space_id` is derived from the message name for audit tagging; the Chat API
    already enforces that the message belongs to its parent space.
    """
    # spaces/{SPACE}/messages/{MSG} → "spaces/{SPACE}". Validated by the
    # MessageId Pydantic constraint at the tool input layer, so we can trust
    # the structure here.
    parts = message_name.split("/")
    space_id = "/".join(parts[:2]) if len(parts) >= 2 else message_name

    async def body(access_token: str, _user_sub: str) -> MessageDetails:
        raw = await ctx.client.get_message(access_token, message_name)
        msg = _ChatMessageResponse(**raw)
        email, display_name = await _resolve_sender(access_token, msg, ctx)
        create_time = msg.create_time
        ts = create_time.astimezone(UTC) if create_time.tzinfo else create_time.replace(tzinfo=UTC)
        reactions, paged = _summarize_reactions(msg.emoji_reaction_summaries)
        return MessageDetails(
            message_id=msg.name,
            space_id=space_id,
            thread_id=msg.thread.name,
            sender_user_id=msg.sender.name,
            sender_email=email,
            sender_display_name=display_name or msg.sender.display_name,
            text=msg.text,
            timestamp=ts,
            last_update_time=msg.last_update_time,
            reactions=reactions,
            reactions_paged=paged,
        )

    return await invoke_tool(
        "get_message",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES_READONLY,
    )


def _summarize_reactions(
    raw: list[dict[str, object]] | None,
) -> tuple[list[ReactionSummary], bool]:
    if not raw:
        return [], False
    summaries: list[ReactionSummary] = []
    for entry in raw:
        if not isinstance(entry, dict):
            continue
        raw_emoji = entry.get("emoji")
        count_obj = entry.get("reactionCount")
        if not isinstance(raw_emoji, dict):
            continue
        # ty: narrow-to-Never on untyped dict .get — the runtime shape is the
        # Chat API's emoji object, we keep the checks anyway.
        emoji_str: object = raw_emoji.get("unicode")  # ty: ignore[invalid-argument-type]
        if not isinstance(emoji_str, str):
            custom = raw_emoji.get("customEmoji")  # ty: ignore[invalid-argument-type]
            if isinstance(custom, dict):
                emoji_str = custom.get("uid") or custom.get("name")
        if not isinstance(emoji_str, str):
            continue
        count = 0
        if isinstance(count_obj, int):
            count = count_obj
        elif isinstance(count_obj, str):
            try:
                count = int(count_obj)
            except ValueError:
                count = 0
        summaries.append(ReactionSummary(emoji=emoji_str, count=count))
    if len(summaries) > _INLINE_REACTIONS_CAP:
        return [], True
    return summaries, False


async def _resolve_sender(
    access_token: str, msg: _ChatMessageResponse, ctx: ToolContext
) -> tuple[str | None, str | None]:
    user_id = msg.sender.name
    cached = await ctx.directory_cache.get(user_id)
    if cached is not None:
        return cached
    fetched = await fetch_person(ctx.client, access_token, user_id)
    if fetched is None:
        return None, msg.sender.display_name
    email, display_name = fetched
    if email:
        await ctx.directory_cache.put(user_id, email, display_name)
    return email, display_name or msg.sender.display_name
