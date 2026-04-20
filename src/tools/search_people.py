"""Tool: search_people — hybrid lookup over Workspace directory + caller contacts."""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from typing import Any

from fastmcp.exceptions import ToolError

from ..chat_client import ChatApiError
from ..models import (
    PeopleSearchSource,
    PersonHit,
    SearchPeopleInput,
    SearchPeopleResult,
)
from ..storage import workspace_user_id
from ._common import (
    CONTACTS_READONLY,
    DIRECTORY_READONLY,
    ToolContext,
    _format_missing_scope_message,
    _is_missing_scope_error,
    invoke_tool,
)
from ._directory import primary_email, primary_name


class _MissingScope:
    """Sentinel: the upstream source returned a missing-scope 403.

    Using a class rather than re-raising inside `_safe_search` so
    `asyncio.gather` doesn't propagate ExceptionGroup on scope
    mismatches; each task resolves to either a list of persons or
    this sentinel.
    """


_SearchFn = Callable[[str, str, int], Awaitable[list[dict[str, Any]]]]
_SourceResult = list[dict[str, Any]] | _MissingScope


async def search_people_handler(ctx: ToolContext, payload: SearchPeopleInput) -> SearchPeopleResult:
    """Fan out to the requested sources in parallel, merge by resourceName.

    Back-fills the DirectoryCache for hits whose `resourceName` matches the
    Workspace-profile shape (shared namespace with Chat's `users/{id}`) so
    later `get_messages`/`list_members` can resolve `sender_email` without
    another People API call. Contact-ID hits don't back-fill — different
    namespace, would poison the cache.

    Failure handling: if one source returns missing-scope, drop it and
    continue. If BOTH sources requested fail with missing-scope, raise a
    ToolError pointing the caller at the scope(s) they're missing —
    distinguishing "no scope" from "genuine no matches" is the one case
    where an error beats an empty list.
    """

    async def body(access_token: str, _user_sub: str) -> SearchPeopleResult:
        sources_attempted: list[PeopleSearchSource] = list(payload.sources)
        fns: list[_SearchFn] = [
            ctx.client.search_directory_people if s == "DIRECTORY" else ctx.client.search_contacts
            for s in sources_attempted
        ]
        results: list[_SourceResult] = await asyncio.gather(
            *(_safe_search(fn, access_token, payload) for fn in fns)
        )

        sources_succeeded: list[PeopleSearchSource] = []
        scope_missing: list[PeopleSearchSource] = []
        hits_by_name: dict[str, PersonHit] = {}
        cache_writes: list[tuple[str, str, str | None]] = []

        for source, outcome in zip(sources_attempted, results, strict=True):
            if isinstance(outcome, _MissingScope):
                scope_missing.append(source)
                continue
            sources_succeeded.append(source)
            for person in outcome:
                resource_name = person.get("resourceName")
                if not isinstance(resource_name, str) or not resource_name:
                    continue
                if resource_name in hits_by_name:
                    # Dedupe: first-source-wins; DIRECTORY listed first by
                    # default input order, so Workspace hits take precedence
                    # over the same person's consumer contact entry.
                    continue
                email = primary_email(person)
                display_name = primary_name(person)
                user_id = workspace_user_id(resource_name)
                hits_by_name[resource_name] = PersonHit(
                    user_id=user_id,
                    email=email,
                    display_name=display_name,
                    source=source,
                )
                if email:
                    # The cache gate in storage.py filters by resource_name
                    # shape — contact IDs are dropped there. We pass
                    # everything and let the gate enforce the invariant.
                    cache_writes.append((resource_name, email, display_name))

        if scope_missing and not sources_succeeded:
            # Every requested source failed with missing-scope — surface it
            # so the caller can re-consent rather than silently returning
            # empty.
            raise ToolError(_format_missing_scope_message(_scope_for(scope_missing[0])))

        if cache_writes:
            await ctx.directory_cache.put_many(cache_writes)

        hits = list(hits_by_name.values())[: payload.limit]
        return SearchPeopleResult(
            people=hits,
            total_returned=len(hits),
            sources_attempted=sources_attempted,
            sources_succeeded=sources_succeeded,
        )

    return await invoke_tool("search_people", ctx, body)


async def _safe_search(
    fn: _SearchFn, access_token: str, payload: SearchPeopleInput
) -> _SourceResult:
    """Call one upstream search; translate missing-scope 403s into a sentinel."""
    try:
        return await fn(access_token, payload.query, payload.limit)
    except ChatApiError as exc:
        if _is_missing_scope_error(exc):
            return _MissingScope()
        raise


def _scope_for(source: PeopleSearchSource) -> str:
    return DIRECTORY_READONLY if source == "DIRECTORY" else CONTACTS_READONLY
