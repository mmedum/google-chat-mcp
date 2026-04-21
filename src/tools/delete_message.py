"""Tool: delete_message — delete a message via spaces.messages.delete."""

from __future__ import annotations

from ..chat_client import ChatApiError
from ..models import DeleteMessageInput, DeleteMessageResult
from ._common import (
    CHAT_MESSAGES,
    ToolContext,
    invoke_tool,
    is_missing_scope_error,
    space_id_from_message_name,
)


async def delete_message_handler(
    ctx: ToolContext, payload: DeleteMessageInput
) -> DeleteMessageResult:
    """Delete a message by full resource name.

    Idempotent — same shape as `remove_member`: double-delete returns
    `deleted=False` on 404 NOT_FOUND or non-scope 403 PERMISSION_DENIED.
    Missing-scope 403s are NOT swallowed; they still raise so callers
    see the re-auth prompt.
    """
    space_id = space_id_from_message_name(payload.message_name)

    if payload.dry_run:

        async def dry_body(_access_token: str, _user_sub: str) -> DeleteMessageResult:
            return DeleteMessageResult(
                message_name=payload.message_name,
                deleted=False,
                dry_run=True,
            )

        return await invoke_tool(
            "delete_message",
            ctx,
            dry_body,
            target_space_id=space_id,
            required_scope=CHAT_MESSAGES,
        )

    async def body(access_token: str, _user_sub: str) -> DeleteMessageResult:
        try:
            await ctx.client.delete_message(access_token, payload.message_name)
        except ChatApiError as exc:
            if _is_gone_or_forbidden(exc):
                return DeleteMessageResult(
                    message_name=payload.message_name,
                    deleted=False,
                )
            raise
        return DeleteMessageResult(
            message_name=payload.message_name,
            deleted=True,
        )

    return await invoke_tool(
        "delete_message",
        ctx,
        body,
        target_space_id=space_id,
        required_scope=CHAT_MESSAGES,
    )


def _is_gone_or_forbidden(exc: ChatApiError) -> bool:
    """True when the message is already gone. Excludes missing-scope 403s."""
    if is_missing_scope_error(exc):
        return False
    if exc.status_code == 404 or exc.google_status == "NOT_FOUND":
        return True
    return exc.status_code == 403 and exc.google_status == "PERMISSION_DENIED"
