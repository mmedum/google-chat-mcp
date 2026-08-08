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
from contextvars import Token
from dataclasses import dataclass
from typing import Literal, get_args

from fastmcp.exceptions import ToolError
from fastmcp.server.dependencies import get_access_token
from pydantic import ValidationError

from ..chat_client import ChatApiError, ChatClient
from ..config import (
    CHAT_MEMBERSHIPS,
    CHAT_MEMBERSHIPS_READONLY,
    CHAT_MESSAGES,
    CHAT_MESSAGES_CREATE,
    CHAT_MESSAGES_REACTIONS,
    CHAT_MESSAGES_READONLY,
    CHAT_SPACES,
    CHAT_SPACES_CREATE,
    CHAT_SPACES_READONLY,
    CONTACTS_READONLY,
    DIRECTORY_READONLY,
    OPENID_SCOPE,
)
from ..models import MemberRole, MemberState, SpaceTypeOut, _ChatSpaceResponse
from ..observability import (
    current_tool,
    logger,
    mcp_audit_write_failures_total,
    mcp_rate_limit_hits_total,
    mcp_schema_drift_total,
    mcp_tool_calls_total,
    mcp_tool_latency_seconds,
)
from ..rate_limit import ActiveUserTracker, TokenBucketLimiter
from ..storage import Database, DirectoryCache, write_audit_row


@dataclass(slots=True, frozen=True)
class AuthInfo:
    """Resolved auth for a single tool call: upstream Google token + user sub.

    `granted_scopes` is set by the stdio resolver (read from the local
    tokens.json) so `invoke_tool` can fail-fast with a re-consent prompt
    when the caller asks for a tool whose `required_scope` was never
    granted. HTTPS leaves it None — FastMCP's GoogleProvider already
    validates scopes against the MCP-layer JWT before the request reaches
    a tool handler.
    """

    access_token: str
    user_sub: str
    granted_scopes: tuple[str, ...] | None = None


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
    "update_message",
    "delete_message",
    "update_space",
]

# Scope constants re-exported from `src/config.py` so tool handlers + tests
# can import from one module. Explicit `__all__` prevents ruff from treating
# the transitive re-exports as unused.
__all__ = [
    "CHAT_MEMBERSHIPS",
    "CHAT_MEMBERSHIPS_READONLY",
    "CHAT_MESSAGES",
    "CHAT_MESSAGES_CREATE",
    "CHAT_MESSAGES_REACTIONS",
    "CHAT_MESSAGES_READONLY",
    "CHAT_SPACES",
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
    "drift_fields",
    "format_missing_scope_message",
    "invoke_tool",
    "is_missing_scope_error",
    "member_role_out",
    "member_state_out",
    "narrow_enum",
    "space_display_name",
    "space_id_from_message_name",
    "space_type_out",
    "write_result",
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


def is_missing_scope_error(exc: ChatApiError) -> bool:
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


def format_missing_scope_message(scope: str) -> str:
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


def drift_fields(exc: Exception) -> list[str]:
    """Field paths that failed validation, without their values.

    Response models are `extra="allow"`, so this fires for a field we *read*
    changing shape or disappearing — the field path is the whole diagnosis.
    (An added field no longer raises; that path reports via `schema_drift`.)
    `str(exc)` would carry it, but it renders `input_value=...` inline, and a
    drifted field holding message text or an email would then reach the logs
    as a preformatted string that `_redact_sensitive` cannot see into.

    **The safety here is that only `loc` is read.** The `include_*=False` kwargs
    are belt-and-braces, not the protection — `msg` and `ctx` still contain the
    value even with them set (a wrapped `ValueError` renders `got {v!r}`). If
    you ever extend this to return `err["msg"]` to make drift "more
    diagnosable", you reintroduce exactly the leak this function exists to
    prevent.

    Field paths are safe to log only while no model declares a
    `dict[str, <validated type>]`: pydantic puts *dict keys* into `loc`, so
    such a field would put attacker- or user-controlled keys in this list.
    Today only `membership_count: dict[str, int]` qualifies and its keys are
    Google constants. Re-check this if that changes.
    """
    if not isinstance(exc, ValidationError):
        return []
    return [
        ".".join(str(part) for part in err["loc"])
        for err in exc.errors(include_input=False, include_url=False, include_context=False)
    ]


def write_result[T](build: Callable[[], T], *, action: str) -> T:
    """Build a write tool's result, making a post-write failure non-retryable.

    By the time we read the response the write has already landed. A bare
    exception here becomes `Internal error.`, and a caller that reasonably
    retries posts the message twice — that is how the `markupSyntax` drift
    turned into duplicates rather than a clean failure.

    Write handlers therefore read only the identifiers they return, straight
    from the raw JSON, instead of validating a full response model to reach two
    fields. The result model still validates what it is handed, so this fires
    only if Google stops returning those identifiers at all.
    """
    try:
        return build()
    except Exception as exc:
        logger.warning("write_response_unusable", action=action, fields=drift_fields(exc))
        mcp_schema_drift_total.labels(action).inc()
        raise ToolError(
            f"The {action} call SUCCEEDED, but its response could not be read, so "
            "no result is available. Do NOT retry — retrying would duplicate it. "
            "The server's response models are stale against the Chat API."
        ) from exc


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

    # Pre-flight scope check (stdio path). HTTPS leaves auth.granted_scopes
    # as None and relies on GoogleProvider's MCP-layer JWT validation —
    # the upstream 403 fallback in the except branch handles its case.
    if (
        required_scope is not None
        and auth.granted_scopes is not None
        and required_scope not in auth.granted_scopes
    ):
        raise ToolError(format_missing_scope_message(required_scope))

    if not await ctx.limiter.allow(user_sub):
        mcp_rate_limit_hits_total.inc()
        raise ToolError("Rate limit exceeded. Try again in a moment.")
    await ctx.active_users.touch(user_sub)

    started = time.perf_counter()
    success = False
    error_code: str | None = None
    # Publish the tool name for code below the handler (People API enrichment
    # labels its counter from this) so the name is written once, here, rather
    # than re-passed by each handler and free to drift.
    tool_token = current_tool.set(tool_name)
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
        if required_scope is not None and is_missing_scope_error(exc):
            error_code = "missing_scope"
            raise ToolError(format_missing_scope_message(required_scope)) from exc
        raise ToolError(f"Google Chat API error: {exc}") from exc
    except ToolError:
        error_code = "tool_error"
        raise
    except Exception as exc:
        error_code = exc.__class__.__name__
        # The caller only ever sees "Internal error.", and the processor chain
        # has no format_exc_info, so `exc_info` renders as a bare `true`.
        # Without these two keys nothing about the failure reaches anyone:
        # `error_type` for ordinary bugs, `fields` for schema drift. Neither
        # carries a value.
        if isinstance(exc, ValidationError):
            mcp_schema_drift_total.labels(tool_name).inc()
        logger.exception(
            "tool_unhandled",
            tool=tool_name,
            error_type=exc.__class__.__name__,
            fields=drift_fields(exc),
        )
        raise ToolError("Internal error.") from exc
    finally:
        # Everything in this block is bookkeeping, and an exception escaping a
        # `finally` REPLACES whatever the tool was raising or returning — a
        # locked or full database would turn a clean `ToolError` into an
        # unrelated `OperationalError`, hiding the actual outcome from the
        # caller. Guarding the whole teardown rather than just the audit write
        # means anything added here later inherits the guarantee instead of
        # having to remember it.
        await _record_call_outcome(
            ctx,
            tool_token=tool_token,
            tool_name=tool_name,
            target_space_id=target_space_id,
            user_sub=user_sub,
            success=success,
            error_code=error_code,
            started=started,
        )


async def _record_call_outcome(
    ctx: ToolContext,
    *,
    tool_token: Token[str],
    tool_name: ToolName,
    target_space_id: str | None,
    user_sub: str,
    success: bool,
    error_code: str | None,
    started: float,
) -> None:
    """Record metrics + the audit row for one call. Contract: never raises.

    The two halves are guarded separately on purpose: sharing one `try` meant a
    metrics failure would skip the audit row as a side effect, losing the
    security-relevant record because of the cosmetic one.
    """
    latency_ms = int((time.perf_counter() - started) * 1000)
    try:
        current_tool.reset(tool_token)
    except Exception:
        logger.exception("tool_context_reset_failed", tool=tool_name)
    try:
        status_label = "ok" if success else "error"
        mcp_tool_calls_total.labels(tool_name, status_label).inc()
        mcp_tool_latency_seconds.labels(tool_name).observe(latency_ms / 1000.0)
    except Exception:
        logger.exception("tool_metrics_record_failed", tool=tool_name)
    try:
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
    except Exception as exc:
        # Fail-open, but never silently: the call completed and the caller gets
        # its result, while `audit_log` has a hole no other signal would show.
        # `error_type` explicitly, for the reason the handler above documents:
        # the processor chain has no `format_exc_info`, so `exc_info` renders
        # as a bare `true` and "check disk space" would be unactionable.
        mcp_audit_write_failures_total.inc()
        logger.exception("audit_write_failed", tool=tool_name, error_type=type(exc).__name__)


def narrow_enum[T: str](value: object, allowed: tuple[T, ...], fallback: T, *, location: str) -> T:
    """Map a wire enum string onto our closed set, bucketing anything new.

    Wire models type enum-shaped fields as `str`, because these fields sit on
    every row of their resource — a closed `Literal` there fails the whole
    resource the first time Google extends the enum, which is the same total
    outage an unknown *field* causes. Callers still get a closed set: anything
    unrecognised lands in `fallback` and is logged and counted instead.

    Pass `allowed` as `get_args(<the Literal>)`, never a hand-written tuple: a
    copy silently rots the moment someone extends the Literal, and the symptom
    is a value we *do* support being bucketed away and reported as drift.

    The value is safe to log: these are Google's enum names, not user content.
    """
    if isinstance(value, str) and value in allowed:
        return value  # ty: ignore[invalid-return-type]
    if value is not None:
        logger.warning("enum_value_unrecognised", location=location, value=value)
        mcp_schema_drift_total.labels(location).inc()
    return fallback


def space_type_out(s: _ChatSpaceResponse) -> SpaceTypeOut:
    """Narrow a wire space type to the tool-facing enum."""
    return narrow_enum(
        s.type_,
        get_args(SpaceTypeOut),
        "SPACE_TYPE_UNSPECIFIED",
        location="_ChatSpaceResponse.type",
    )


def member_role_out(value: object) -> MemberRole:
    """Narrow a wire membership role to the tool-facing enum."""
    return narrow_enum(
        value, get_args(MemberRole), "ROLE_UNSPECIFIED", location="_ChatMembershipResponse.role"
    )


def member_state_out(value: object) -> MemberState:
    """Narrow a wire membership state to the tool-facing enum."""
    return narrow_enum(
        value,
        get_args(MemberState),
        "MEMBERSHIP_STATE_UNSPECIFIED",
        location="_ChatMembershipResponse.state",
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
