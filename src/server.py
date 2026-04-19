"""FastMCP entrypoint.

Composition root: config, Google OAuth provider, SQLite, shared httpx client,
the tool handlers, and custom routes (health, readiness, metrics).
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass

from cryptography.fernet import Fernet
from fastmcp import FastMCP
from fastmcp.server.auth.providers.google import GoogleProvider
from key_value.aio.stores.disk import DiskStore
from key_value.aio.wrappers.encryption import FernetEncryptionWrapper
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest
from starlette.requests import Request
from starlette.responses import JSONResponse, PlainTextResponse, Response

from .chat_client import ChatClient
from .config import GOOGLE_OAUTH_SCOPES, Settings
from .models import (
    ChatMessage,
    DirectMessageResult,
    GetMessagesInput,
    ListMembersInput,
    ListSpacesInput,
    Member,
    SendMessageInput,
    SendMessageResult,
    SpaceDetails,
    SpaceSummary,
    SpaceType,
)
from .observability import (
    REGISTRY,
    configure_logging,
    logger,
    mcp_active_users,
)
from .rate_limit import ActiveUserTracker, TokenBucketLimiter
from .storage import Database, lifespan_database, prune_audit_log
from .tools import (
    find_direct_message_handler,
    get_messages_handler,
    get_space_handler,
    list_members_handler,
    list_spaces_handler,
    send_message_handler,
)
from .tools._common import ToolContext


def build_auth(settings: Settings) -> GoogleProvider:
    """Wire FastMCP's GoogleProvider (OAuthProxy subclass) with encrypted disk storage."""
    kv_store = DiskStore(directory=str(settings.kv_store_path))
    encrypted_store = FernetEncryptionWrapper(
        key_value=kv_store,
        fernet=Fernet(settings.fernet_key.get_secret_value().encode()),
    )
    return GoogleProvider(
        client_id=settings.google_client_id.get_secret_value(),
        client_secret=settings.google_client_secret.get_secret_value(),
        base_url=settings.base_url,
        redirect_path="/oauth/callback",
        required_scopes=list(GOOGLE_OAUTH_SCOPES),
        valid_scopes=list(GOOGLE_OAUTH_SCOPES),
        allowed_client_redirect_uris=list(settings.allowed_client_redirects),
        client_storage=encrypted_store,
        jwt_signing_key=settings.jwt_signing_key.get_secret_value(),
        require_authorization_consent=True,
    )


@dataclass(slots=True)
class _AppState:
    """Lifespan-scoped singletons, mutated once during startup."""

    ctx: ToolContext | None = None


def build_app(settings: Settings) -> FastMCP:
    """Construct the FastMCP app with tools, auth, and custom routes bound."""
    auth = build_auth(settings)
    state = _AppState()

    @asynccontextmanager
    async def lifespan(_: FastMCP) -> AsyncIterator[None]:
        async with lifespan_database(settings.sqlite_path) as db:
            client = ChatClient(
                timeout_seconds=settings.http_timeout_seconds,
                max_retries=settings.http_max_retries,
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

    mcp: FastMCP = FastMCP(name="google-chat-mcp", auth=auth, lifespan=lifespan)

    # ---- tools ----

    @mcp.tool(
        name="list_spaces",
        description=(
            "List Google Chat spaces (DMs, group chats, named spaces) the "
            "authenticated user belongs to. Defaults to 50 entries; pass "
            "`limit` (1-200) to widen and `space_type` "
            "('SPACE' | 'DIRECT_MESSAGE' | 'GROUP_CHAT') to narrow."
        ),
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
        description=(
            "Find or create a direct-message space with a Google Workspace user. "
            "Returns the space ID for use with send_message."
        ),
    )
    async def find_direct_message(user_email: str) -> DirectMessageResult:
        return await find_direct_message_handler(_require_ctx(state), user_email)

    @mcp.tool(
        name="send_message",
        description=(
            "Post a text message to a Chat space (DM or room). Optional thread_name "
            "replies to an existing thread."
        ),
    )
    async def send_message(payload: SendMessageInput) -> SendMessageResult:
        return await send_message_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="get_messages",
        description=(
            "Read recent messages from a space. Returns up to `limit` messages "
            "(default 20, max 100), newest first. Sender email resolved via People API."
        ),
    )
    async def get_messages(payload: GetMessagesInput) -> list[ChatMessage]:
        return await get_messages_handler(_require_ctx(state), payload)

    @mcp.tool(
        name="get_space",
        description=(
            "Fetch details for a single Google Chat space by its ID. Use this "
            "to identify unnamed DMs/group chats or confirm space metadata."
        ),
    )
    async def get_space(space_id: str) -> SpaceDetails:
        return await get_space_handler(_require_ctx(state), space_id)

    @mcp.tool(
        name="list_members",
        description=(
            "List members of a Google Chat space. Returns humans (with email "
            "resolved via People API) and Google Groups, each tagged by `kind`. "
            "Defaults to 50 entries; pass `limit` (1-200) to widen."
        ),
    )
    async def list_members(space_id: str, limit: int = 50) -> list[Member]:
        return await list_members_handler(
            _require_ctx(state),
            ListMembersInput(space_id=space_id, limit=limit),
        )

    # ---- custom routes ----

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
    """Tool handlers must not fire before the lifespan has populated state."""
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


def main() -> None:
    settings = Settings.from_env()
    configure_logging(settings.log_level)
    app = build_app(settings)
    app.run(transport="http", host="0.0.0.0", port=8000)  # noqa: S104


if __name__ == "__main__":
    main()
