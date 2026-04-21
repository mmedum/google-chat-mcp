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
from ..observability import logger
from ..storage import workspace_user_id
from ._common import (
    CONTACTS_READONLY,
    DIRECTORY_READONLY,
    ToolContext,
    format_missing_scope_message,
    invoke_tool,
    is_missing_scope_error,
)
from ._directory import primary_email, primary_name


class _SourceFailure:
    """Sentinel: one upstream source errored. Carries the original exception so
    the handler can decide whether to raise (all sources gone) or degrade
    (still have hits from the other source).

    Using a class instead of re-raising inside `_safe_search` avoids
    `asyncio.gather` propagating one task's exception and throwing away
    the other task's success — the hybrid-lookup contract says one
    source's failure must not mask another's hits.
    """

    def __init__(self, exc: ChatApiError) -> None:
        self.exc = exc
        self.missing_scope = is_missing_scope_error(exc)


_SearchFn = Callable[[str, str, int], Awaitable[list[dict[str, Any]]]]
_SourceResult = list[dict[str, Any]] | _SourceFailure


async def search_people_handler(ctx: ToolContext, payload: SearchPeopleInput) -> SearchPeopleResult:
    """Fan out to the requested sources in parallel, merge by resourceName.

    Back-fills the DirectoryCache for hits whose `resourceName` matches the
    Workspace-profile shape (shared namespace with Chat's `users/{id}`) so
    later `get_messages`/`list_members` can resolve `sender_email` without
    another People API call. Contact-ID hits don't back-fill — different
    namespace, would poison the cache.

    Failure handling: a per-source ChatApiError (any status — missing-scope,
    admin-disabled directory sharing, 5xx) is treated as a source failure
    that drops out of the result; remaining sources still contribute hits.
    If EVERY requested source fails, the handler raises — a missing-scope-
    only failure set maps to the re-consent prompt (user-fixable); any
    mixed or non-scope failure set aggregates the upstream reasons so an
    operator can see what actually broke.
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
        failures: list[tuple[PeopleSearchSource, _SourceFailure]] = []
        hits_by_name: dict[str, PersonHit] = {}
        cache_writes: list[tuple[str, str, str | None]] = []

        for source, outcome in zip(sources_attempted, results, strict=True):
            if isinstance(outcome, _SourceFailure):
                failures.append((source, outcome))
                if not outcome.missing_scope:
                    # Non-scope errors still degrade (one source dying must
                    # not mask another source's hits), but operators need to
                    # see the upstream status in logs to diagnose — e.g.
                    # the Workspace-admin "directory sharing disabled" 403.
                    logger.warning(
                        "search_people_source_error",
                        source=source,
                        upstream_status=outcome.exc.status_code,
                        google_status=outcome.exc.google_status,
                        message=outcome.exc.message,
                    )
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

        if failures and not sources_succeeded:
            # Every requested source errored. Pick the message: all
            # missing-scope → the re-consent prompt (user-fixable);
            # otherwise aggregate the upstream messages so the admin can
            # see what actually broke.
            if all(f.missing_scope for _, f in failures):
                raise ToolError(format_missing_scope_message(_scope_for(failures[0][0])))
            reasons = "; ".join(f"{source}: {f.exc.message}" for source, f in failures)
            raise ToolError(f"search_people: all sources failed ({reasons})")

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
    """Call one upstream search; return a sentinel on any ChatApiError.

    Both scope-denials and other 403s (e.g. Workspace admin has disabled
    directory sharing) count as per-source failures — the caller layer
    decides whether to raise (all failed) or degrade (at least one
    succeeded).
    """
    try:
        return await fn(access_token, payload.query, payload.limit)
    except ChatApiError as exc:
        return _SourceFailure(exc)


def _scope_for(source: PeopleSearchSource) -> str:
    return DIRECTORY_READONLY if source == "DIRECTORY" else CONTACTS_READONLY
