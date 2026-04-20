"""Shared helpers for tool handlers.

Every handler goes through `invoke_tool` which:
- enforces the per-user rate limit,
- pulls the upstream Google access token from FastMCP's auth context,
- times the call,
- records metrics and audit log,
- translates upstream errors into clean MCP errors.
"""

from __future__ import annotations

import hashlib
import hmac
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Literal

from fastmcp.exceptions import ToolError
from fastmcp.server.dependencies import get_access_token

from ..chat_client import ChatApiError, ChatClient
from ..config import (
    CHAT_MEMBERSHIPS,
    CHAT_MEMBERSHIPS_READONLY,
    CHAT_MESSAGES_CREATE,
    CHAT_MESSAGES_REACTIONS,
    CHAT_MESSAGES_READONLY,
    CHAT_SPACES_CREATE,
    CHAT_SPACES_READONLY,
    CONTACTS_READONLY,
    DIRECTORY_READONLY,
    OPENID_SCOPE,
)
from ..models import _ChatSpaceResponse
from ..observability import (
    logger,
    mcp_rate_limit_hits_total,
    mcp_tool_calls_total,
    mcp_tool_latency_seconds,
)
from ..rate_limit import ActiveUserTracker, TokenBucketLimiter
from ..storage import Database, DirectoryCache, write_audit_row


@dataclass(slots=True, frozen=True)
class AuthInfo:
    """Resolved auth for a single tool call: upstream Google token + user sub."""

    access_token: str
    user_sub: str


AuthResolver = Callable[[], Awaitable[AuthInfo]]

ToolName = Literal[
    "list_spaces",
    "find_direct_message",
    "send_message",
    "get_messages",
    "get_space",
    "list_members",
    "whoami",
    "get_thread",
    "get_message",
    "add_reaction",
    "remove_reaction",
    "list_reactions",
    "search_messages",
    "create_group_chat",
    "create_space",
    "add_member",
    "remove_member",
    "search_people",
]

# Scope constants re-exported from `src/config.py` so tool handlers + tests
# can import from one module. Explicit `__all__` prevents ruff from treating
# the transitive re-exports as unused.
__all__ = [
    "CHAT_MEMBERSHIPS",
    "CHAT_MEMBERSHIPS_READONLY",
    "CHAT_MESSAGES_CREATE",
    "CHAT_MESSAGES_REACTIONS",
    "CHAT_MESSAGES_READONLY",
    "CHAT_SPACES_CREATE",
    "CHAT_SPACES_READONLY",
    "CONTACTS_READONLY",
    "DIRECTORY_READONLY",
    "OPENID_SCOPE",
    "AuthInfo",
    "AuthResolver",
    "ToolContext",
    "ToolName",
    "audit_user_sub",
    "invoke_tool",
    "space_display_name",
    "space_id_from_message_name",
]


class ToolContext:
    """Process-wide singletons injected into tool handlers at server startup."""

    __slots__ = (
        "active_users",
        "audit_hash_user_sub",
        "audit_pepper",
        "client",
        "db",
        "directory_cache",
        "limiter",
        "resolver",
    )

    def __init__(
        self,
        client: ChatClient,
        db: Database,
        limiter: TokenBucketLimiter,
        active_users: ActiveUserTracker,
        audit_pepper: bytes | None = None,
        audit_hash_user_sub: bool = True,
        directory_cache_ttl_seconds: int = 86_400,
        resolver: AuthResolver | None = None,
    ) -> None:
        if audit_hash_user_sub and audit_pepper is None:
            raise ValueError("audit_pepper required when audit_hash_user_sub is True")
        self.client = client
        self.db = db
        self.directory_cache = DirectoryCache(db, ttl_seconds=directory_cache_ttl_seconds)
        self.limiter = limiter
        self.active_users = active_users
        self.audit_pepper = audit_pepper
        self.audit_hash_user_sub = audit_hash_user_sub
        self.resolver = resolver


def space_id_from_message_name(message_name: str) -> str:
    """Extract `spaces/{S}` from a `spaces/{S}/messages/{M}` resource name.

    Trusts that the caller already validated the shape via `MessageId` — the
    tool input layer rejects malformed values upstream.
    """
    return message_name.rsplit("/messages/", 1)[0]


def audit_user_sub(user_sub: str, *, pepper: bytes | None, hash_enabled: bool) -> str:
    """Return the sub as stored in audit_log — HMAC-SHA256 hex when hashing is on."""
    if not hash_enabled:
        return user_sub
    if pepper is None:
        raise ValueError("pepper required when hashing is enabled")
    return hmac.new(pepper, user_sub.encode("utf-8"), hashlib.sha256).hexdigest()


def _is_missing_scope_error(exc: ChatApiError) -> bool:
    """Detect Google's "insufficient scope" 403 from an AIP-193 error envelope.

    Dual condition: prefer the typed reason code (error.details[].reason); fall
    back to the textual status + message substring for endpoints whose error
    envelope doesn't populate the reason.
    """
    if exc.status_code != 403:
        return False
    if exc.google_reason == "ACCESS_TOKEN_SCOPE_INSUFFICIENT":
        return True
    return (
        exc.google_status == "PERMISSION_DENIED"
        and "insufficient authentication scopes" in exc.message.lower()
    )


def _format_missing_scope_message(scope: str) -> str:
    """Human-readable text for a missing-scope ToolError.

    Format is stable: the scope URL appears between "scope: " and ". Re-run".
    Clients that want machine-readable re-auth info can parse it out. FastMCP
    3.2's ToolError doesn't carry structuredContent on isError results; when
    upstream support arrives, this moves to a proper structured envelope.
    """
    return (
        f"Missing required OAuth scope: {scope}. "
        "Re-run `google-chat-mcp login` (stdio) or re-consent in your MCP "
        "client (HTTPS) to grant this scope."
    )


async def _resolve_auth_via_fastmcp() -> AuthInfo:
    """HTTPS-transport resolver: pull sub + upstream token from the FastMCP request context."""
    token = get_access_token()
    if token is None or not token.claims:
        raise ToolError("Not authenticated.")
    sub = token.claims.get("sub")
    if not sub:
        raise ToolError("Token is missing the 'sub' claim.")
    upstream_access_token = token.token
    if not upstream_access_token:
        raise ToolError("No upstream access token available for this session.")
    return AuthInfo(access_token=upstream_access_token, user_sub=str(sub))


async def invoke_tool[T](
    tool_name: ToolName,
    ctx: ToolContext,
    body: Callable[[str, str], Awaitable[T]],
    *,
    target_space_id: str | None = None,
    required_scope: str | None = None,
) -> T:
    """Run a tool handler with audit, metrics, rate-limit, and auth context.

    `required_scope`, when provided, drives the missing-scope error wrapping:
    on an upstream 403 matching Google's insufficient-scope shape, the user-
    facing ToolError names the exact scope so the MCP client can prompt for
    re-auth.
    """
    auth = await (ctx.resolver() if ctx.resolver is not None else _resolve_auth_via_fastmcp())
    user_sub = auth.user_sub
    upstream_access_token = auth.access_token

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
        logger.error(
            "tool_upstream_error",
            tool=tool_name,
            status=exc.status_code,
            google_status=exc.google_status,
            google_reason=exc.google_reason,
        )
        if required_scope is not None and _is_missing_scope_error(exc):
            error_code = "missing_scope"
            raise ToolError(_format_missing_scope_message(required_scope)) from exc
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
        audit_sub = audit_user_sub(
            user_sub or "unknown",
            pepper=ctx.audit_pepper,
            hash_enabled=ctx.audit_hash_user_sub,
        )
        await write_audit_row(
            ctx.db,
            user_sub=audit_sub,
            tool_name=tool_name,
            target_space_id=target_space_id,
            success=success,
            latency_ms=latency_ms,
            error_code=error_code,
        )


def space_display_name(s: _ChatSpaceResponse) -> str:
    """Human-friendly label for a space: `displayName` if set, else a synthetic tag."""
    if s.display_name:
        return s.display_name
    if s.type_ == "DIRECT_MESSAGE":
        return "(direct message)"
    if s.type_ == "GROUP_CHAT":
        return "(group chat)"
    return "(unnamed space)"
