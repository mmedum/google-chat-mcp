"""Resource: `gchat://spaces/{space_id}/threads/{thread_id}` — ordered thread messages."""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING

from fastmcp.resources import ResourceContent
from pydantic import TypeAdapter

from ..models import ChatMessage, GetThreadInput
from ..tools import get_thread_handler

if TYPE_CHECKING:
    from fastmcp import FastMCP

    from ..tools._common import ToolContext


_MESSAGES_ADAPTER = TypeAdapter(list[ChatMessage])


def register_thread_resource(mcp: FastMCP, resolve_ctx: Callable[[], ToolContext]) -> None:
    """Bind `gchat://spaces/{space_id}/threads/{thread_id}` to the get_thread handler.

    URI params are the bare IDs (no `spaces/` or `threads/` prefixes); the Chat
    API expects the full resource names, built here.
    """

    @mcp.resource(
        "gchat://spaces/{space_id}/threads/{thread_id}",
        name="thread",
        title="Google Chat thread",
        mime_type="application/json",
        description=(
            "All messages in a single thread, oldest-first. Same content shape "
            "as the `get_thread` tool. IDs are bare (no `spaces/`/`threads/` prefix)."
        ),
    )
    async def thread_resource(space_id: str, thread_id: str) -> list[ResourceContent]:
        ctx: ToolContext = resolve_ctx()
        full_space = space_id if space_id.startswith("spaces/") else f"spaces/{space_id}"
        thread_name = (
            thread_id
            if thread_id.startswith(f"{full_space}/threads/")
            else f"{full_space}/threads/{thread_id}"
        )
        messages = await get_thread_handler(
            ctx, GetThreadInput(space_id=full_space, thread_name=thread_name)
        )
        body = _MESSAGES_ADAPTER.dump_json(messages).decode("utf-8")
        return [ResourceContent(body, mime_type="application/json")]
