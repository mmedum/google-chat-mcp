"""Resource: `gchat://spaces/{space_id}/messages/{message_id}` — one message with reactions."""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING

from fastmcp.resources import ResourceContent

from ..tools.get_message import get_message_handler

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
        full_space = space_id if space_id.startswith("spaces/") else f"spaces/{space_id}"
        message_name = (
            message_id
            if message_id.startswith(f"{full_space}/messages/")
            else f"{full_space}/messages/{message_id}"
        )
        details = await get_message_handler(ctx, message_name)
        body = details.model_dump_json()
        return [ResourceContent(body, mime_type="application/json")]
