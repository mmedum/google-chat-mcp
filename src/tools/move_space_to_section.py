"""Tool: move_space_to_section — file a space under a sidebar section."""

from __future__ import annotations

from typing import Any

from fastmcp.exceptions import ToolError

from ..chat_client import _build_move_section_item_body
from ..models import (
    MoveSpaceToSectionInput,
    MoveSpaceToSectionResult,
    _ChatSectionItemResponse,
)
from ._common import (
    CHAT_USERS_SECTIONS,
    ToolContext,
    invoke_tool,
    same_section,
    section_name_from_item_name,
)


async def move_space_to_section_handler(
    ctx: ToolContext, payload: MoveSpaceToSectionInput
) -> MoveSpaceToSectionResult:
    """Move a space into `section_name`.

    Google's move endpoint addresses a *section item*, not a space, so the
    item has to be identified first. `payload.item_name` supplies it directly;
    without it the handler lists items filtered to this space across all
    sections (the `-` wildcard parent) and uses the one that comes back.

    A dry run performs the lookup where one is needed — it is a read — so the
    preview can report which section the space would leave. Nothing is
    written.

    `item_name` is taken on trust when it names the target section: there is
    nothing left to ask Google, so the result says `moved=False` without any
    request. A hint that is stale *in that direction* — the space has since
    moved elsewhere — is therefore reported as already-filed rather than
    corrected. Pass `space_id` alone if you need that verified.
    """

    async def body(access_token: str, _user_sub: str) -> MoveSpaceToSectionResult:
        item_name = payload.item_name or await _locate_item(ctx, access_token, payload)
        from_section = section_name_from_item_name(item_name)

        needs_move = not same_section(from_section, payload.section_name)
        should_move = not payload.dry_run and needs_move
        if should_move:
            raw = await ctx.client.move_section_item(access_token, item_name, payload.section_name)
            # The item's resource name embeds its section, so the move renames
            # it and the name we arrived with now 404s. Returning the stale one
            # was actively harmful: the tool description tells callers to feed
            # `item_name` back in for a bulk sort. Prefer what Google returned;
            # fall back to the derived name, since the id survives the move.
            item_name = _moved_item_name(raw) or (
                f"{payload.section_name}/items/{item_name.rsplit('/items/', 1)[1]}"
            )
        return MoveSpaceToSectionResult(
            space_id=payload.space_id,
            section_name=payload.section_name,
            item_name=item_name,
            from_section=from_section,
            moved=should_move,
            dry_run=payload.dry_run,
            # Only on a dry run that would actually write. Rendering it for
            # an already-filed space made the preview unreadable: `moved=False`
            # means both "skipped" and "dry run", so the payload is the only
            # way to tell them apart — and it was populated either way.
            rendered_payload=(
                _build_move_section_item_body(target_section=payload.section_name)
                if payload.dry_run and needs_move
                else None
            ),
        )

    return await invoke_tool(
        "move_space_to_section",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_USERS_SECTIONS,
    )


def _moved_item_name(raw: dict[str, Any]) -> str | None:
    """The item's new resource name from a move response, if it is readable.

    Read defensively rather than through `write_result`: the move has already
    landed, so a response we cannot parse must not become an error the caller
    might retry. The derived fallback covers it.
    """
    item = raw.get("sectionItem")
    if not isinstance(item, dict):
        return None
    name = item.get("name")
    return name if isinstance(name, str) else None


async def _locate_item(
    ctx: ToolContext, access_token: str, payload: MoveSpaceToSectionInput
) -> str:
    """Find the space's current section item across every section."""
    raw_items, _ = await ctx.client.list_section_items(
        access_token, limit=1, space_id=payload.space_id
    )
    if not raw_items:
        raise ToolError(
            f"No section item found for {payload.space_id}. The space may not "
            "exist, or you may not be a member of it."
        )
    item = _ChatSectionItemResponse(**raw_items[0])
    # This path trusts a server-side `filter`, and what follows is a write: if
    # Google ever stops honouring that filter the first row back would be an
    # arbitrary space, and we would move it. Confirm the match rather than
    # assume it. The `item_name` path needs no equivalent — the caller named
    # the item, so there is no filter to be silently ignored.
    if item.space != payload.space_id:
        raise ToolError(
            f"Lookup for {payload.space_id} returned a section item for "
            f"{item.space or 'an unknown item'}. Refusing to move it."
        )
    return item.name
