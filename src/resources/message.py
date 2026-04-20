"""Resource: `gchat://spaces/{space_id}/messages/{message_id}` — one message with reactions."""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING

from fastmcp.resources import ResourceContent

from ..tools.get_message import get_message_handler
from ._common import ensure_child_name

if TYPE_CHECKING:
    from fastmcp import FastMCP

    from ..tools._common import ToolContext


def register_message_resource(mcp: FastMCP, resolve_ctx: Callable[[], ToolContext]) -> None:
    """Bind `gchat://spaces/{space_id}/messages/{message_id}` to the get_message handler.

    URI params are bare IDs (no `spaces/`/`messages/` prefixes); the Chat API
    expects the full resource names, built here.
    """

    @mcp.resource(
        "gchat://spaces/{space_id}/messages/{message_id}",
        name="message",
        title="Google Chat message",
        mime_type="application/json",
        description=(
            "A single Google Chat message by ID, with reaction summaries "
            "hydrated inline. Same content shape as the `get_message` tool. "
            "IDs are bare (no prefix)."
        ),
    )
    async def message_resource(space_id: str, message_id: str) -> list[ResourceContent]:
        ctx: ToolContext = resolve_ctx()
        details = await get_message_handler(
            ctx, ensure_child_name(space_id, message_id, "messages")
        )
        body = details.model_dump_json()
        return [ResourceContent(body, mime_type="application/json")]
