"""Tool: update_message — edit the text of a message via spaces.messages.patch."""

from __future__ import annotations

from ..chat_client import _build_update_message_body
from ..models import UpdateMessageInput, UpdateMessageResult, _ChatMessageResponse
from ._common import CHAT_MESSAGES, ToolContext, invoke_tool, space_id_from_message_name


async def update_message_handler(
    ctx: ToolContext, payload: UpdateMessageInput
) -> UpdateMessageResult:
    """Replace the text of an existing message.

    `payload.dry_run=True` builds the patch body without PATCHing —
    same dry/real-parity contract as `send_message`. Rate-limit bucket
    + audit row still fire.

    Text-only: `updateMask=text` is set on the URL by the client, so
    cards / attachments / other fields are left untouched. Editing
    those requires app-auth (server-side bot identity) which this
    per-user-OAuth surface doesn't expose.
    """
    space_id = space_id_from_message_name(payload.message_name)

    async def body(access_token: str, _user_sub: str) -> UpdateMessageResult:
        if payload.dry_run:
            rendered = _build_update_message_body(text=payload.text)
            return UpdateMessageResult(
                message_name=payload.message_name,
                text=payload.text,
                dry_run=True,
                rendered_payload=rendered,
            )
        raw = await ctx.client.update_message(access_token, payload.message_name, payload.text)
        parsed = _ChatMessageResponse(**raw)
        return UpdateMessageResult(
            message_name=parsed.name,
            text=parsed.text,
        )

    return await invoke_tool(
        "update_message",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES,
    )
