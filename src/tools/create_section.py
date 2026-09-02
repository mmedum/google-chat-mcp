"""Tool: create_section — add a custom section to the caller's sidebar."""

from __future__ import annotations

from ..chat_client import _build_create_section_body
from ..models import CreateSectionInput, CreateSectionResult
from ._common import (
    CHAT_USERS_SECTIONS,
    ToolContext,
    invoke_tool,
    write_result,
)


async def create_section_handler(
    ctx: ToolContext, payload: CreateSectionInput
) -> CreateSectionResult:
    """Create a `CUSTOM_SECTION` named `display_name`.

    Google does not deduplicate on name — creating "Clients" twice leaves two
    sections called "Clients". `list_sections` first if you mean to reuse one.
    """

    async def body(access_token: str, _user_sub: str) -> CreateSectionResult:
        if payload.dry_run:
            return CreateSectionResult(
                display_name=payload.display_name,
                dry_run=True,
                rendered_payload=_build_create_section_body(display_name=payload.display_name),
            )
        raw = await ctx.client.create_section(access_token, display_name=payload.display_name)
        return write_result(
            lambda: CreateSectionResult(
                section_name=raw["name"],
                display_name=payload.display_name,
            ),
            action="create_section",
        )

    return await invoke_tool("create_section", ctx, body, required_scope=CHAT_USERS_SECTIONS)
