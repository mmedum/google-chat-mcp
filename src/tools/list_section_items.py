"""Tool: list_section_items — read which spaces sit in which section."""

from __future__ import annotations

from ..models import (
    ListSectionItemsInput,
    ListSectionItemsResult,
    SectionItemSummary,
    _ChatSectionItemResponse,
)
from ._common import (
    CHAT_USERS_SECTIONS_READONLY,
    ToolContext,
    invoke_tool,
    section_name_from_item_name,
)
from ._rows import _parse_rows


async def list_section_items_handler(
    ctx: ToolContext, payload: ListSectionItemsInput
) -> ListSectionItemsResult:
    """List the spaces filed under a section, or locate one space's section.

    Which parent the listing runs against, and why one of the two selectors is
    required, is documented on `ListSectionItemsInput`.
    """

    async def body(access_token: str, _user_sub: str) -> ListSectionItemsResult:
        raw, next_page_token = await ctx.client.list_section_items(
            access_token,
            limit=payload.limit,
            section_name=payload.section_name,
            space_id=payload.space_id,
            page_token=payload.page_token,
        )
        items, unparsed = _parse_rows(
            raw, lambda r: _item_summary(_ChatSectionItemResponse(**r)), kind="list_section_items"
        )
        return ListSectionItemsResult(
            items=items, next_page_token=next_page_token, unparsed=unparsed
        )

    return await invoke_tool(
        "list_section_items",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_USERS_SECTIONS_READONLY,
    )


def _item_summary(i: _ChatSectionItemResponse) -> SectionItemSummary:
    return SectionItemSummary(
        item_name=i.name,
        section_name=section_name_from_item_name(i.name),
        space_id=i.space,
    )
