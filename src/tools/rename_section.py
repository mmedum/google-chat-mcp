"""Tool: rename_section — change a custom section's display name."""

from __future__ import annotations

from ..chat_client import _build_update_section_body
from ..models import RenameSectionInput, RenameSectionResult
from ._common import (
    CHAT_USERS_SECTIONS,
    ToolContext,
    invoke_tool,
    write_result,
)


async def rename_section_handler(
    ctx: ToolContext, payload: RenameSectionInput
) -> RenameSectionResult:
    """Rename a custom section.

    Only `CUSTOM_SECTION` can be renamed; Google's rejection of a patch
    against a system section is left to speak for itself rather than being
    second-guessed here from a resource id.
    """

    async def body(access_token: str, _user_sub: str) -> RenameSectionResult:
        if payload.dry_run:
            return RenameSectionResult(
                section_name=payload.section_name,
                display_name=payload.display_name,
                dry_run=True,
                rendered_payload=_build_update_section_body(display_name=payload.display_name),
            )
        raw = await ctx.client.update_section(
            access_token, payload.section_name, display_name=payload.display_name
        )
        return write_result(
            lambda: RenameSectionResult(
                section_name=raw["name"],
                display_name=payload.display_name,
            ),
            action="rename_section",
        )

    return await invoke_tool("rename_section", ctx, body, required_scope=CHAT_USERS_SECTIONS)
