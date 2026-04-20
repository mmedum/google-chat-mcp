"""Transport-agnostic MCP server assembly.

Builds the FastMCP instance, registers tools and resources, wires the
ToolContext lifespan. Transport-specific composition (GoogleProvider for
HTTPS, loopback login for stdio) lives in `src/server.py` and `src/stdio.py`;
this module stays unaware of either.
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass
from importlib.metadata import PackageNotFoundError, version
from typing import TYPE_CHECKING

from fastmcp import FastMCP
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest
from pydantic import EmailStr
from starlette.requests import Request
from starlette.responses import JSONResponse, PlainTextResponse, Response

from .chat_client import ChatClient
from .config import Settings
from .models import (
    AddMemberInput,
    AddMemberResult,
    AddReactionInput,
    AddReactionResult,
    ChatMessage,
    CreateGroupChatInput,
    CreateGroupChatResult,
    CreateSpaceInput,
    CreateSpaceResult,
    DirectMessageResult,
    GetMessagesInput,
    GetThreadInput,
    ListMembersInput,
    ListReactionsInput,
    ListReactionsResult,
    ListSpacesInput,
    Member,
    MessageDetails,
    MessageId,
    RemoveMemberInput,
    RemoveMemberResult,
    RemoveReactionInput,
    RemoveReactionResult,
    SearchMessagesInput,
    SearchMessagesResult,
    SearchPeopleInput,
    SearchPeopleResult,
    SendMessageInput,
    SendMessageResult,
    SpaceDetails,
    SpaceSummary,
    SpaceType,
    WhoamiResult,
)
from .observability import (
    REGISTRY,
    logger,
    mcp_active_users,
)
from .rate_limit import ActiveUserTracker, TokenBucketLimiter
from .resources import (
    register_message_resource,
    register_space_resource,
    register_thread_resource,
)
from .storage import Database, lifespan_database, prune_audit_log
from .tools import (
    add_member_handler,
    add_reaction_handler,
    create_group_chat_handler,
    create_space_handler,
    find_direct_message_handler,
    get_message_handler,
    get_messages_handler,
    get_space_handler,
    get_thread_handler,
    list_members_handler,
    list_reactions_handler,
    list_spaces_handler,
    remove_member_handler,
    remove_reaction_handler,
    search_messages_handler,
    search_people_handler,
    send_message_handler,
    whoami_handler,
)
from .tools._common import AuthResolver, ToolContext

if TYPE_CHECKING:
    from fastmcp.server.auth.auth import AuthProvider


def _package_version() -> str:
    """Resolve the installed package version, falling back to a sentinel in unusual envs."""
    try:
        return version("google-chat-mcp")
    except PackageNotFoundError:
        return "0.0.0+unknown"


@dataclass(slots=True)
class _AppState:
    """Lifespan-scoped singletons, mutated once during startup."""

    ctx: ToolContext | None = None


def build_app(  # noqa: PLR0915 — composition root; each tool/resource adds statements. Splitting further fragments the wire-shape registration.
    settings: Settings,
    *,
    resolver: AuthResolver | None = None,
    auth: AuthProvider | None = None,
) -> FastMCP:
    """Construct the FastMCP app with tools, resources, and (HTTPS-only) custom routes.

    `resolver` is the per-invocation auth resolver (stdio supplies a local one;
    HTTPS leaves it None to fall back to FastMCP's request-context dependency).
    `auth` is the HTTPS `AuthProvider` (GoogleProvider); stdio leaves it None,
    which also suppresses the HTTP-only health / readiness / metrics routes.
    """
    state = _AppState()

    @asynccontextmanager
    async def lifespan(_: FastMCP) -> AsyncIterator[None]:
        async with lifespan_database(settings.sqlite_path) as db:
            client = ChatClient(
                timeout_seconds=settings.http_timeout_seconds,
                max_retries=settings.http_max_retries,
                base_chat=settings.chat_api_base,
                base_people=settings.people_api_base,
            )
            limiter = TokenBucketLimiter(capacity=settings.rate_limit_per_minute)
            active_users = ActiveUserTracker()
            state.ctx = ToolContext(
                client=client,
                db=db,
                limiter=limiter,
                active_users=active_users,
                audit_pepper=(
                    settings.audit_pepper.get_secret_value().encode("utf-8")
                    if settings.audit_pepper is not None
                    else None
                ),
                audit_hash_user_sub=settings.audit_hash_user_sub,
                directory_cache_ttl_seconds=settings.directory_cache_ttl_seconds,
                resolver=resolver,
            )
            # Prune once at startup — the periodic loop first fires 24h from now,
            # so without this an always-fresh container would never prune.
            removed = await prune_audit_log(db, settings.audit_retention_days)
            if removed:
                logger.info(
                    "audit_log_pruned_on_startup",
                    removed=removed,
                    retention_days=settings.audit_retention_days,
                )
            gauge_task = asyncio.create_task(_active_users_gauge_loop(active_users))
            prune_task = asyncio.create_task(_audit_prune_loop(db, settings.audit_retention_days))
            try:
                logger.info("startup_complete", base_url=settings.base_url)
                yield
            finally:
                gauge_task.cancel()
                prune_task.cancel()
                await client.close()
                logger.info("shutdown_complete")

    mcp: FastMCP = FastMCP(
        name="google-chat-mcp",
        version=_package_version(),
        auth=auth,
        lifespan=lifespan,
    )

    # ---- tools ----

    @mcp.tool(
        name="list_spaces",
        title="List Google Chat spaces",
        description=(
            "List Google Chat spaces (DMs, group chats, named spaces) the "
            "authenticated user belongs to. Defaults to 50 entries; pass "
            "`limit` (1-200) to widen and `space_type` "
            "('SPACE' | 'DIRECT_MESSAGE' | 'GROUP_CHAT') to narrow."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def list_spaces(
        space_type: SpaceType | None = None,
        limit: int = 50,
    ) -> list[SpaceSummary]:
        return await list_spaces_handler(
            _require_ctx(state),
            ListSpacesInput(space_type=space_type, limit=limit),
        )

    @mcp.tool(
        name="find_direct_message",
        title="Find or create a DM space",
        description=(
            "Find or create a direct-message space with a Google Workspace user. "
            "Returns the space ID for use with send_message."
        ),
        # Not read-only: creates a DM space on miss (via spaces.setup).
        annotations={
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": True,
            "openWorldHint": True,
        },
    )
    async def find_direct_message(user_email: EmailStr) -> DirectMessageResult:
        return await find_direct_message_handler(_require_ctx(state), user_email)

    @mcp.tool(
        name="create_group_chat",
        title="Create a group chat",
        description=(
            "Create an unnamed multi-person DM (`GROUP_CHAT`) with 2-20 members. "
            "`member_emails` excludes the caller — Google adds the authenticated "
            "user implicitly. Google rejects `displayName` on this space type; "
            "use `create_space` if you need a named room. Set `dry_run=true` to "
            "preview the request body without posting."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": False,
            "openWorldHint": True,
        },
    )
    async def create_group_chat(payload: CreateGroupChatInput) -> CreateGroupChatResult:
        return await create_group_chat_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="create_space",
        title="Create a named space",
        description=(
            "Create a named space (`SPACE`) with 1-20 initial members and a "
            "required `display_name`. `member_emails` excludes the caller — "
            "Google adds the authenticated user implicitly. Set `dry_run=true` "
            "to preview the request body without posting."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": False,
            "openWorldHint": True,
        },
    )
    async def create_space(payload: CreateSpaceInput) -> CreateSpaceResult:
        return await create_space_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="add_member",
        title="Add a member to a space",
        description=(
            "Invite a Google Workspace user into a space by email. Idempotent-"
            "adjacent: if the user is already a member, returns a `ToolError` "
            "naming them (not a silent success — the existing membership_name "
            "belongs to the original inviter). Set `dry_run=true` to preview "
            "the request body."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": False,
            "openWorldHint": True,
        },
    )
    async def add_member(payload: AddMemberInput) -> AddMemberResult:
        return await add_member_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="remove_member",
        title="Remove a member from a space",
        description=(
            "Remove a membership by its full resource name "
            "(`spaces/{space}/members/{member}`). Idempotent: double-delete "
            "returns `removed=false` rather than erroring. Fetch the "
            "membership_name via `list_members` first — there is no "
            "email-filter shape, since non-self People API resolution is "
            "unreliable and would silently miss the target."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": True,
            "idempotentHint": True,
            "openWorldHint": True,
        },
    )
    async def remove_member(payload: RemoveMemberInput) -> RemoveMemberResult:
        return await remove_member_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="search_people",
        title="Search people by name or email",
        description=(
            "Hybrid lookup across the caller's Workspace directory "
            "(`searchDirectoryPeople`) and personal contacts "
            "(`searchContacts`). Returns up to `limit` hits; each hit is "
            "tagged with the `source` that produced it. Workspace hits "
            "back-fill the directory cache so later `get_messages` / "
            "`list_members` resolve `sender_email` without another API call. "
            'Use this to turn `"jesper"` into an email before calling '
            "`create_group_chat`, `add_member`, or `send_message`."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def search_people(payload: SearchPeopleInput) -> SearchPeopleResult:
        return await search_people_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="send_message",
        title="Send a Google Chat message",
        description=(
            "Post a text message to a Chat space (DM or room). Optional thread_name "
            "replies to an existing thread."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": False,
            "openWorldHint": True,
        },
    )
    async def send_message(payload: SendMessageInput) -> SendMessageResult:
        return await send_message_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="get_messages",
        title="Read recent messages",
        description=(
            "Read recent messages from a space. Returns up to `limit` messages "
            "(default 20, max 100), newest first. Sender email resolved via People API."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def get_messages(payload: GetMessagesInput) -> list[ChatMessage]:
        return await get_messages_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="get_space",
        title="Get space details",
        description=(
            "Fetch details for a single Google Chat space by its ID. Use this "
            "to identify unnamed DMs/group chats or confirm space metadata."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def get_space(space_id: str) -> SpaceDetails:
        return await get_space_handler(_require_ctx(state), space_id)

    @mcp.tool(
        name="list_members",
        title="List space members",
        description=(
            "List members of a Google Chat space. Returns humans (with email "
            "resolved via People API) and Google Groups, each tagged by `kind`. "
            "Defaults to 50 entries; pass `limit` (1-200) to widen."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def list_members(space_id: str, limit: int = 50) -> list[Member]:
        return await list_members_handler(
            _require_ctx(state),
            ListMembersInput(space_id=space_id, limit=limit),
        )

    @mcp.tool(
        name="whoami",
        title="Authenticated user identity",
        description=(
            "Return the authenticated Google user's identity (sub, email, "
            "display name). Useful as a first-call smoke test and for "
            "self-identity queries. Reads from OIDC /userinfo."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def whoami() -> WhoamiResult:
        return await whoami_handler(_require_ctx(state))

    @mcp.tool(
        name="get_thread",
        title="Read a Chat thread",
        description=(
            "Read all messages in a single thread, oldest-first. Provide the "
            "parent `space_id` and the `thread_name` "
            "(`spaces/{space}/threads/{thread}`). Default limit 50 (max 100)."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def get_thread(payload: GetThreadInput) -> list[ChatMessage]:
        return await get_thread_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="get_message",
        title="Get one Chat message",
        description=(
            "Fetch a single message by its resource name "
            "(`spaces/{space}/messages/{message}`). Reaction summaries are "
            "hydrated inline; `reactions_paged: true` signals the caller "
            "should follow up with `list_reactions` for full detail."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def get_message(message_name: MessageId) -> MessageDetails:
        return await get_message_handler(_require_ctx(state), message_name)

    @mcp.tool(
        name="add_reaction",
        title="Add a reaction",
        description=(
            "Add a Unicode emoji reaction to a message. Idempotent — re-adding "
            "the same (emoji, user) combination is a no-op on the Chat API."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": True,
            "openWorldHint": True,
        },
    )
    async def add_reaction(payload: AddReactionInput) -> AddReactionResult:
        return await add_reaction_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="remove_reaction",
        title="Remove a reaction",
        description=(
            "Delete a reaction. Provide either `reaction_name` (direct delete "
            "by full resource name) OR the tuple "
            "(`message_name` + `emoji` + `user_email`) — the latter looks up "
            "the reaction server-side via filter. `removed: false` means "
            "the lookup matched zero reactions (already gone)."
        ),
        annotations={
            "readOnlyHint": False,
            "destructiveHint": True,
            "idempotentHint": True,
            "openWorldHint": True,
        },
    )
    async def remove_reaction(payload: RemoveReactionInput) -> RemoveReactionResult:
        return await remove_reaction_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="list_reactions",
        title="List reactions on a message",
        description=(
            "List reactions on a single message. Default limit 50 (max 200); "
            "paginate via `page_token` / `next_page_token`."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def list_reactions(payload: ListReactionsInput) -> ListReactionsResult:
        return await list_reactions_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="search_messages",
        title="Search messages in a space",
        description=(
            "Client-side search over a single space's message history. "
            "Always pass a target `space_id` and a `created_after` lower "
            "bound — an unbounded scan of a large space hits the page cap "
            "and returns a partial result. Provide exactly one of `query` "
            "(exact substring, case-insensitive) or `regex` (Python re.search). "
            "For org-wide history, direct the user to the Chat web UI."
        ),
        annotations={"readOnlyHint": True, "openWorldHint": True},
    )
    async def search_messages(payload: SearchMessagesInput) -> SearchMessagesResult:
        return await search_messages_handler(_require_ctx(state), payload)

    # ---- resources ----

    register_space_resource(mcp, resolve_ctx=lambda: _require_ctx(state))
    register_thread_resource(mcp, resolve_ctx=lambda: _require_ctx(state))
    register_message_resource(mcp, resolve_ctx=lambda: _require_ctx(state))

    # ---- HTTP-only custom routes ----
    # Registered only when an HTTPS auth provider is wired; stdio has no HTTP surface.

    if auth is not None:

        @mcp.custom_route("/healthz", methods=["GET"])
        async def healthz(_: Request) -> Response:
            return PlainTextResponse("ok")

        @mcp.custom_route("/readyz", methods=["GET"])
        async def readyz(_: Request) -> Response:
            ctx = state.ctx
            if ctx is None:
                return JSONResponse({"ready": False, "reason": "not started"}, status_code=503)
            try:
                async with ctx.db.cursor() as conn:
                    await conn.execute("SELECT 1")
            except (OSError, RuntimeError) as exc:
                return JSONResponse(
                    {"ready": False, "reason": f"db: {exc.__class__.__name__}"},
                    status_code=503,
                )
            # Probe the OAuth disk KV store: write/read/delete a canary. Catches
            # "volume unmounted" or "write-disabled" long before the first OAuth call
            # would crash with a confusing stack trace.
            probe = settings.kv_store_path / ".readyz_probe"
            try:
                settings.kv_store_path.mkdir(parents=True, exist_ok=True)
                probe.write_text("ok")
                probe.unlink()
            except OSError as exc:
                return JSONResponse(
                    {"ready": False, "reason": f"kv_store: {exc.__class__.__name__}"},
                    status_code=503,
                )
            return JSONResponse({"ready": True})

        @mcp.custom_route("/metrics", methods=["GET"])
        async def metrics(_: Request) -> Response:
            return Response(generate_latest(REGISTRY), media_type=CONTENT_TYPE_LATEST)

    return mcp


def _require_ctx(state: _AppState) -> ToolContext:
    """Tool and resource handlers must not fire before the lifespan has populated state."""
    if state.ctx is None:
        raise RuntimeError("ToolContext accessed before server startup completed.")
    return state.ctx


async def _active_users_gauge_loop(tracker: ActiveUserTracker) -> None:
    """Keep the active-users gauge honest even when no tool calls arrive."""
    while True:
        try:
            await asyncio.sleep(30)
            # Touch with a sentinel that immediately ages out, just to re-prune.
            # We don't record the sentinel itself in the count.
            count = await tracker.touch("__gauge_probe__")
            mcp_active_users.set(max(0, count - 1))
        except asyncio.CancelledError:
            break
        except Exception:
            logger.exception("active_users_gauge_error")


async def _audit_prune_loop(db: Database, retention_days: int) -> None:
    """Drop audit rows older than the retention window, daily."""
    while True:
        try:
            await asyncio.sleep(86_400)
            removed = await prune_audit_log(db, retention_days)
            if removed:
                logger.info("audit_log_pruned", removed=removed, retention_days=retention_days)
        except asyncio.CancelledError:
            break
        except Exception:
            logger.exception("audit_prune_error")
