"""Tool: delete_section — remove a custom section from the caller's sidebar."""

from __future__ import annotations

from ..chat_client import ChatApiError
from ..models import DeleteSectionInput, DeleteSectionResult
from ._common import (
    CHAT_USERS_SECTIONS,
    ToolContext,
    invoke_tool,
    is_already_gone,
)


async def delete_section_handler(
    ctx: ToolContext, payload: DeleteSectionInput
) -> DeleteSectionResult:
    """Delete a custom section.

    Non-destructive to conversations: Google moves the section's spaces back
    to the default sections rather than deleting them.

    Idempotent on 404 — a repeat delete returns `deleted=False`. A plain 403
    is NOT swallowed, unlike `delete_message`: the one that shows up here is
    Google refusing to delete a *system* section, which the caller has to see.
    """

    async def body(access_token: str, _user_sub: str) -> DeleteSectionResult:
        if payload.dry_run:
            return DeleteSectionResult(
                section_name=payload.section_name,
                deleted=False,
                dry_run=True,
            )
        try:
            await ctx.client.delete_section(access_token, payload.section_name)
        except ChatApiError as exc:
            if is_already_gone(exc, forbidden_means_gone=False):
                return DeleteSectionResult(section_name=payload.section_name, deleted=False)
            raise
        return DeleteSectionResult(section_name=payload.section_name, deleted=True)

    return await invoke_tool("delete_section", ctx, body, required_scope=CHAT_USERS_SECTIONS)
