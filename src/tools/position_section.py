"""Tool: position_section — reorder a section in the caller's sidebar."""

from __future__ import annotations

from typing import Any

from ..chat_client import _build_position_section_body
from ..models import PositionSectionInput, PositionSectionResult
from ._common import (
    CHAT_USERS_SECTIONS,
    ToolContext,
    invoke_tool,
    write_result,
)


async def position_section_handler(
    ctx: ToolContext, payload: PositionSectionInput
) -> PositionSectionResult:
    """Move a section to an absolute rank or to the start / end of the list.

    The union rule on the two positioning fields is enforced by
    `PositionSectionInput`; see its docstring.
    """

    async def body(access_token: str, _user_sub: str) -> PositionSectionResult:
        if payload.dry_run:
            return PositionSectionResult(
                section_name=payload.section_name,
                # Deliberately not echoing `payload.sort_order`: the field
                # means "the rank Google reports". Nothing was reordered, so
                # the requested rank belongs in `rendered_payload` only.
                sort_order=None,
                dry_run=True,
                rendered_payload=_build_position_section_body(
                    sort_order=payload.sort_order,
                    relative_position=payload.relative_position,
                ),
            )
        raw = await ctx.client.position_section(
            access_token,
            payload.section_name,
            sort_order=payload.sort_order,
            relative_position=payload.relative_position,
        )
        return write_result(
            lambda: PositionSectionResult(
                section_name=payload.section_name,
                sort_order=_sort_order_of(raw),
            ),
            action="position_section",
        )

    return await invoke_tool("position_section", ctx, body, required_scope=CHAT_USERS_SECTIONS)


def _sort_order_of(raw: dict[str, Any]) -> int | None:
    """Read `section.sortOrder` out of the response, tolerating its absence.

    The rank is informational — the move already landed by the time we read
    it — so a response that omits it must not turn a successful reorder into
    an error the caller might retry.
    """
    section = raw.get("section")
    if not isinstance(section, dict):
        return None
    value = section.get("sortOrder")
    # `isinstance(True, int)` is True, and the strict result model then
    # rejects the bool — turning a reorder that already landed into the
    # non-retryable write_result error. Coerce a numeric string too, so
    # this agrees with `_ChatSectionResponse`, which pydantic coerces.
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, str):
        try:
            return int(value)
        except ValueError:
            return None
    return None
