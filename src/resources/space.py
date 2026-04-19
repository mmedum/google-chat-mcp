"""Resource: `gchat://spaces/{space_id}` — single space metadata."""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING

from fastmcp.resources import ResourceContent

from ..tools import get_space_handler

if TYPE_CHECKING:
    from fastmcp import FastMCP

    from ..tools._common import ToolContext


def register_space_resource(mcp: FastMCP, resolve_ctx: Callable[[], ToolContext]) -> None:
    """Bind `gchat://spaces/{space_id}` to the shared get_space handler.

    `resolve_ctx` is a callable that returns the current `ToolContext`
    — injected by the composition root so this module doesn't know about
    server lifespan state.
    """

    @mcp.resource(
        "gchat://spaces/{space_id}",
        name="space",
        title="Google Chat space",
        mime_type="application/json",
        description=(
            "A single Google Chat space by ID. Returns the same content shape "
            "as the `get_space` tool. `space_id` is the bare ID (no `spaces/` prefix)."
        ),
    )
    async def space_resource(space_id: str) -> list[ResourceContent]:
        ctx: ToolContext = resolve_ctx()
        # URI param is the bare ID; the Chat API expects the `spaces/{id}` resource name.
        full_name = space_id if space_id.startswith("spaces/") else f"spaces/{space_id}"
        details = await get_space_handler(ctx, full_name)
        # Explicit ResourceContent so mime_type propagates to the wire envelope;
        # FastMCP's str auto-wrap defaults to text/plain regardless of decorator hint.
        return [ResourceContent(details.model_dump_json(), mime_type="application/json")]
