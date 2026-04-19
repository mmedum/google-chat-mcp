"""Shared helpers for tool handlers.

Every handler goes through `invoke_tool` which:
- enforces the per-user rate limit,
- pulls the upstream Google access token from FastMCP's auth context,
- times the call,
- records metrics and audit log,
- translates upstream errors into clean MCP errors.
"""

from __future__ import annotations

import time
from collections.abc import Awaitable, Callable
from typing import Literal

from fastmcp.exceptions import ToolError
from fastmcp.server.dependencies import get_access_token

from ..chat_client import ChatApiError, ChatClient
from ..observability import (
    logger,
    mcp_rate_limit_hits_total,
    mcp_tool_calls_total,
    mcp_tool_latency_seconds,
)
from ..rate_limit import ActiveUserTracker, TokenBucketLimiter
from ..storage import Database, write_audit_row

ToolName = Literal["list_spaces", "find_direct_message", "send_message", "get_messages"]


class ToolContext:
    """Process-wide singletons injected into tool handlers at server startup."""

    __slots__ = ("active_users", "client", "db", "directory_cache_ttl_seconds", "limiter")

    def __init__(
        self,
        client: ChatClient,
        db: Database,
        limiter: TokenBucketLimiter,
        active_users: ActiveUserTracker,
        directory_cache_ttl_seconds: int = 86_400,
    ) -> None:
        self.client = client
        self.db = db
        self.limiter = limiter
        self.active_users = active_users
        self.directory_cache_ttl_seconds = directory_cache_ttl_seconds


async def invoke_tool[T](
    tool_name: ToolName,
    ctx: ToolContext,
    body: Callable[[str, str], Awaitable[T]],
    *,
    target_space_id: str | None = None,
) -> T:
    """Run a tool handler with audit, metrics, rate-limit, and auth context."""
    token = get_access_token()
    if token is None or not token.claims:
        raise ToolError("Not authenticated.")
    sub = token.claims.get("sub")
    if not sub:
        raise ToolError("Token is missing the 'sub' claim.")
    user_sub = str(sub)
    upstream_access_token = token.token
    if not upstream_access_token:
        raise ToolError("No upstream access token available for this session.")

    if not await ctx.limiter.allow(user_sub):
        mcp_rate_limit_hits_total.inc()
        raise ToolError("Rate limit exceeded. Try again in a moment.")
    await ctx.active_users.touch(user_sub)

    started = time.perf_counter()
    success = False
    error_code: str | None = None
    try:
        result = await body(upstream_access_token, user_sub)
        success = True
        return result
    except ChatApiError as exc:
        error_code = f"google_{exc.status_code}"
        logger.error("tool_upstream_error", tool=tool_name, status=exc.status_code)
        raise ToolError(f"Google Chat API error: {exc}") from exc
    except ToolError:
        error_code = "tool_error"
        raise
    except Exception as exc:
        error_code = exc.__class__.__name__
        logger.exception("tool_unhandled", tool=tool_name)
        raise ToolError("Internal error.") from exc
    finally:
        latency_ms = int((time.perf_counter() - started) * 1000)
        status_label = "ok" if success else "error"
        mcp_tool_calls_total.labels(tool_name, status_label).inc()
        mcp_tool_latency_seconds.labels(tool_name).observe(latency_ms / 1000.0)
        await write_audit_row(
            ctx.db,
            user_sub=user_sub or "unknown",
            tool_name=tool_name,
            target_space_id=target_space_id,
            success=success,
            latency_ms=latency_ms,
            error_code=error_code,
        )
