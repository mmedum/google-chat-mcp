"""Tool: search_messages — paginated list + client-side exact/regex filter."""

from __future__ import annotations

import os
import re
from datetime import UTC

from ..models import (
    SearchMatch,
    SearchMessagesInput,
    SearchMessagesResult,
    _ChatMessageResponse,
)
from ._common import CHAT_MESSAGES_READONLY, ToolContext, invoke_tool
from ._messages import ensure_utc

_DEFAULT_MAX_PAGES = 10
_SNIPPET_CONTEXT_CHARS = 80


async def search_messages_handler(
    ctx: ToolContext, payload: SearchMessagesInput
) -> SearchMessagesResult:
    """Scan a space's messages client-side for `query` (exact) or `regex`.

    Always space-scoped — cross-space search is deliberately unsupported; the
    model should direct the user to the Chat web UI for org-wide history.
    """
    max_pages = payload.max_pages or _operator_max_pages()

    # Compile regex up-front so an invalid pattern fails early (before hitting
    # Google). The input validator enforces exactly-one of query/regex.
    regex = re.compile(payload.regex) if payload.regex is not None else None
    query_lower = payload.query.lower() if payload.query is not None else None
    created_after_iso = _format_created_after(payload)

    async def body(access_token: str, _user_sub: str) -> SearchMessagesResult:
        matches: list[SearchMatch] = []
        scanned = 0
        page_token: str | None = None
        for _ in range(max_pages):
            raw_page, next_token = await ctx.client.list_messages_page(
                access_token,
                space_id=payload.space_id,
                page_size=100,
                page_token=page_token,
                created_after_iso=created_after_iso,
            )
            for raw in raw_page:
                scanned += 1
                # Pydantic's ValidationError is a ValueError; TypeError is the only
                # other realistic failure (non-mapping raw). Catch both via the
                # common base — written as Exception narrowing because ruff-format
                # in the pinned 0.15.x rewrites tuple except clauses.
                try:
                    msg = _ChatMessageResponse(**raw)
                except Exception as exc:
                    if not isinstance(exc, TypeError | ValueError):
                        raise
                    continue
                snippet_start = _match_index(msg.text, query_lower=query_lower, regex=regex)
                if snippet_start is None:
                    continue
                matches.append(
                    SearchMatch(
                        message_id=msg.name,
                        thread_id=msg.thread.name,
                        sender_user_id=msg.sender.name,
                        text=msg.text,
                        timestamp=ensure_utc(msg.create_time),
                        snippet=_extract_snippet(msg.text, snippet_start),
                    )
                )
                if len(matches) >= payload.limit:
                    return SearchMessagesResult(matches=matches, scanned=scanned, cap_reached=False)
            if not next_token:
                return SearchMessagesResult(matches=matches, scanned=scanned, cap_reached=False)
            page_token = next_token
        # Fell out of the loop with a next_token still available: cap reached.
        return SearchMessagesResult(matches=matches, scanned=scanned, cap_reached=True)

    return await invoke_tool(
        "search_messages",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_MESSAGES_READONLY,
    )


def _operator_max_pages() -> int:
    raw = os.environ.get("GCM_SEARCH_MAX_PAGES")
    if not raw:
        return _DEFAULT_MAX_PAGES
    try:
        value = int(raw)
    except ValueError:
        return _DEFAULT_MAX_PAGES
    return max(1, min(value, 50))


def _format_created_after(payload: SearchMessagesInput) -> str | None:
    if payload.created_after is None:
        return None
    dt = (
        payload.created_after.astimezone(UTC)
        if payload.created_after.tzinfo
        else payload.created_after.replace(tzinfo=UTC)
    )
    return dt.strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _match_index(
    text: str, *, query_lower: str | None, regex: re.Pattern[str] | None
) -> int | None:
    if query_lower is not None:
        idx = text.lower().find(query_lower)
        return idx if idx >= 0 else None
    if regex is not None:
        m = regex.search(text)
        return m.start() if m is not None else None
    return None


def _extract_snippet(text: str, match_start: int) -> str:
    start = max(0, match_start - _SNIPPET_CONTEXT_CHARS)
    end = min(len(text), match_start + _SNIPPET_CONTEXT_CHARS)
    snippet = text[start:end]
    if start > 0:
        snippet = f"…{snippet}"
    if end < len(text):
        snippet = f"{snippet}…"
    return snippet
