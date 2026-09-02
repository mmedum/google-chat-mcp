"""Tool: list_sections — list the caller's sidebar sections."""

from __future__ import annotations

from ..models import (
    ListSectionsInput,
    ListSectionsResult,
    SectionSummary,
    _ChatSectionResponse,
)
from ._common import (
    CHAT_USERS_SECTIONS_READONLY,
    ToolContext,
    invoke_tool,
    section_display_name,
    section_type_out,
)
from ._rows import _parse_rows


async def list_sections_handler(ctx: ToolContext, payload: ListSectionsInput) -> ListSectionsResult:
    """List sidebar sections — the three system ones plus any custom ones."""

    async def body(access_token: str, _user_sub: str) -> ListSectionsResult:
        raw, next_page_token = await ctx.client.list_sections(
            access_token, limit=payload.limit, page_token=payload.page_token
        )
        sections, unparsed = _parse_rows(
            raw, lambda r: _section_summary(_ChatSectionResponse(**r)), kind="list_sections"
        )
        return ListSectionsResult(
            sections=sections, next_page_token=next_page_token, unparsed=unparsed
        )

    return await invoke_tool(
        "list_sections", ctx, body, required_scope=CHAT_USERS_SECTIONS_READONLY
    )


def _section_summary(s: _ChatSectionResponse) -> SectionSummary:
    return SectionSummary(
        section_name=s.name,
        display_name=section_display_name(s),
        type=section_type_out(s),
        sort_order=s.sort_order,
    )
