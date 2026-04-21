"""Google Chat / People API HTTP client.

Shared `httpx.AsyncClient` with connection pooling, 10s timeout, exponential
backoff on 5xx and 429. Expects a per-request access token (the caller resolves
it via FastMCP's `get_access_token()` dependency).
"""

from __future__ import annotations

import asyncio
import random
from collections.abc import AsyncIterator, Mapping, Sequence
from contextlib import asynccontextmanager
from typing import Any, Literal, Self
from urllib.parse import urlencode

import httpx

from .models import SpaceType
from .observability import (
    logger,
    mcp_google_api_calls_total,
    mcp_google_api_latency_seconds,
)

# Query-param shapes httpx accepts. A mapping covers the common case; a
# sequence of tuples is needed when a single key repeats (Google's
# searchDirectoryPeople uses `sources` twice for profile + domain-contact).
_QueryParams = Mapping[str, str] | Sequence[tuple[str, str]]


class ChatApiError(RuntimeError):
    """Raised for non-recoverable errors from Google APIs."""

    def __init__(
        self,
        status_code: int,
        message: str,
        endpoint: str,
        *,
        google_status: str | None = None,
        google_reason: str | None = None,
    ) -> None:
        super().__init__(f"{endpoint} returned {status_code}: {message}")
        self.status_code = status_code
        self.endpoint = endpoint
        self.message = message
        # AIP-193 error envelope fields. Preserved so callers (invoke_tool) can
        # distinguish insufficient-scope from other 403s without string-matching.
        self.google_status = google_status
        self.google_reason = google_reason


class ChatClient:
    """Thin async wrapper around Google Chat + People APIs.

    One instance per server process. Created at startup, closed at shutdown.
    Per-request access tokens are passed into each call rather than stored.
    """

    def __init__(
        self,
        timeout_seconds: float = 10.0,
        max_retries: int = 3,
        base_chat: str = "https://chat.googleapis.com/v1",
        base_people: str = "https://people.googleapis.com/v1",
        base_oidc: str = "https://openidconnect.googleapis.com/v1",
        client: httpx.AsyncClient | None = None,
    ) -> None:
        self._base_chat = base_chat
        self._base_people = base_people
        self._base_oidc = base_oidc
        self._max_retries = max_retries
        self._client = client or httpx.AsyncClient(
            timeout=httpx.Timeout(timeout_seconds),
            limits=httpx.Limits(
                max_connections=50,
                max_keepalive_connections=20,
                keepalive_expiry=30.0,
            ),
            follow_redirects=False,
            http2=False,
        )

    async def __aenter__(self) -> Self:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.close()

    async def close(self) -> None:
        await self._client.aclose()

    # ---------- Chat ----------

    async def list_spaces(
        self,
        access_token: str,
        limit: int,
        space_type: str | None = None,
    ) -> list[dict[str, Any]]:
        """List spaces the user is a member of.

        Stops paginating once ``limit`` entries are collected. ``space_type``
        is passed through to Google's native ``filter`` param so the upstream
        already narrows before any page lands.
        """
        extra = {"filter": f'spaceType = "{space_type}"'} if space_type else None
        return await self._paginate(
            url=f"{self._base_chat}/spaces",
            access_token=access_token,
            items_key="spaces",
            limit=limit,
            endpoint_label="spaces.list",
            extra_params=extra,
        )

    async def get_space(self, access_token: str, space_id: str) -> dict[str, Any]:
        """Fetch a single Space resource."""
        return await self._get(
            f"{self._base_chat}/{space_id}",
            access_token=access_token,
            endpoint_label="spaces.get",
        )

    async def list_members(
        self,
        access_token: str,
        space_id: str,
        limit: int,
    ) -> list[dict[str, Any]]:
        """List memberships of a space, stopping once ``limit`` collected."""
        return await self._paginate(
            url=f"{self._base_chat}/{space_id}/members",
            access_token=access_token,
            items_key="memberships",
            limit=limit,
            endpoint_label="spaces.members.list",
        )

    async def find_direct_message(
        self, access_token: str, user_email: str
    ) -> dict[str, Any] | None:
        """Resolve the DM space with the given user. Returns None on 404."""
        return await self._get_optional(
            f"{self._base_chat}/spaces:findDirectMessage",
            access_token=access_token,
            params={"name": f"users/{user_email}"},
            endpoint_label="spaces.findDirectMessage",
        )

    async def create_dm(self, access_token: str, user_email: str) -> dict[str, Any]:
        """Create a DM space with the given user via `spaces.setup`."""
        return await self._setup_space(
            access_token,
            space_type="DIRECT_MESSAGE",
            display_name=None,
            member_emails=[user_email],
        )

    async def setup_space(
        self,
        access_token: str,
        *,
        space_type: Literal["SPACE", "GROUP_CHAT"],
        display_name: str | None,
        member_emails: list[str],
    ) -> dict[str, Any]:
        """Create a SPACE or GROUP_CHAT via `spaces.setup`.

        Wraps the internal builder so callers can't accidentally POST a
        `DIRECT_MESSAGE` from a tool handler — the DM path has its own
        `create_dm` method and a different Pydantic input model.
        """
        return await self._setup_space(
            access_token,
            space_type=space_type,
            display_name=display_name,
            member_emails=member_emails,
        )

    async def _setup_space(
        self,
        access_token: str,
        *,
        space_type: SpaceType,
        display_name: str | None,
        member_emails: list[str],
    ) -> dict[str, Any]:
        body = _build_setup_space_body(
            space_type=space_type,
            display_name=display_name,
            member_emails=member_emails,
        )
        return await self._post(
            f"{self._base_chat}/spaces:setup",
            access_token=access_token,
            json=body,
            endpoint_label="spaces.setup",
        )

    async def send_message(
        self,
        access_token: str,
        space_id: str,
        text: str,
        thread_name: str | None = None,
    ) -> dict[str, Any]:
        """Post a message. `thread_name` is a reply target (`spaces/X/threads/Y`)."""
        body, params = _build_send_message_body(text=text, thread_name=thread_name)
        return await self._post(
            f"{self._base_chat}/{space_id}/messages",
            access_token=access_token,
            json=body,
            params=params or None,
            endpoint_label="spaces.messages.create",
        )

    async def list_messages(
        self,
        access_token: str,
        space_id: str,
        limit: int,
        since_iso: str | None = None,
    ) -> list[dict[str, Any]]:
        """List up to `limit` messages, newest first. Google returns oldest-first,
        so we page in reverse order (orderBy=createTime desc)."""
        params: dict[str, str] = {
            "pageSize": str(min(limit, 100)),
            "orderBy": "createTime desc",
        }
        if since_iso:
            params["filter"] = f'createTime > "{since_iso}"'
        data = await self._get(
            f"{self._base_chat}/{space_id}/messages",
            access_token=access_token,
            params=params,
            endpoint_label="spaces.messages.list",
        )
        return data.get("messages", [])[:limit]

    async def list_messages_page(
        self,
        access_token: str,
        space_id: str,
        *,
        page_size: int = 100,
        page_token: str | None = None,
        created_after_iso: str | None = None,
    ) -> tuple[list[dict[str, Any]], str | None]:
        """One page of messages, newest-first. Returns (messages, next_page_token).

        Exposes Google's native page_token so search_messages can walk up to a
        bounded page cap without pulling everything into memory at once.
        """
        params: dict[str, str] = {
            "pageSize": str(min(page_size, 100)),
            "orderBy": "createTime desc",
        }
        if page_token:
            params["pageToken"] = page_token
        if created_after_iso:
            params["filter"] = f'createTime > "{created_after_iso}"'
        data = await self._get(
            f"{self._base_chat}/{space_id}/messages",
            access_token=access_token,
            params=params,
            endpoint_label="spaces.messages.list",
        )
        messages = data.get("messages", [])
        next_token = data.get("nextPageToken")
        if not isinstance(next_token, str) or not next_token:
            next_token = None
        if not isinstance(messages, list):
            messages = []
        return messages, next_token

    async def get_message(self, access_token: str, message_name: str) -> dict[str, Any]:
        """Fetch a single message by its full resource name (`spaces/{s}/messages/{m}`)."""
        return await self._get(
            f"{self._base_chat}/{message_name}",
            access_token=access_token,
            endpoint_label="spaces.messages.get",
        )

    async def update_message(
        self, access_token: str, message_name: str, text: str
    ) -> dict[str, Any]:
        """Edit the text of a message via PATCH `spaces.messages.patch`.

        `updateMask=text` scopes the patch to the text field only —
        attachments, cards, and other fields are left untouched.
        """
        body = _build_update_message_body(text=text)
        return await self._patch(
            f"{self._base_chat}/{message_name}",
            access_token=access_token,
            json=body,
            params={"updateMask": "text"},
            endpoint_label="spaces.messages.patch",
        )

    async def update_space(
        self,
        access_token: str,
        space_id: str,
        *,
        display_name: str | None = None,
        description: str | None = None,
    ) -> dict[str, Any]:
        """Rename a space or update its description via PATCH `spaces.patch`.

        Mask + body are derived from whichever of `display_name` /
        `description` are non-None — caller must guarantee at least one is
        set (the tool layer enforces this via `UpdateSpaceInput`).
        """
        body, mask = _build_update_space_body(display_name=display_name, description=description)
        return await self._patch(
            f"{self._base_chat}/{space_id}",
            access_token=access_token,
            json=body,
            params={"updateMask": mask},
            endpoint_label="spaces.patch",
        )

    async def delete_message(self, access_token: str, message_name: str) -> dict[str, Any]:
        """Delete a message via DELETE `spaces.messages.delete`."""
        return await self._delete(
            f"{self._base_chat}/{message_name}",
            access_token=access_token,
            endpoint_label="spaces.messages.delete",
        )

    async def add_reaction(
        self, access_token: str, message_name: str, unicode_emoji: str
    ) -> dict[str, Any]:
        """Add a unicode-emoji reaction to a message."""
        return await self._post(
            f"{self._base_chat}/{message_name}/reactions",
            access_token=access_token,
            json={"emoji": {"unicode": unicode_emoji}},
            endpoint_label="spaces.messages.reactions.create",
        )

    async def delete_reaction(self, access_token: str, reaction_name: str) -> dict[str, Any]:
        """Delete a reaction by its full resource name."""
        return await self._delete(
            f"{self._base_chat}/{reaction_name}",
            access_token=access_token,
            endpoint_label="spaces.messages.reactions.delete",
        )

    async def list_reactions(
        self,
        access_token: str,
        message_name: str,
        *,
        limit: int = 50,
        page_token: str | None = None,
        emoji_filter: str | None = None,
        user_filter: str | None = None,
    ) -> dict[str, Any]:
        """List reactions on a message. Optional server-side filter on emoji + user."""
        params: dict[str, str] = {"pageSize": str(min(limit, 200))}
        if page_token:
            params["pageToken"] = page_token
        filters: list[str] = []
        if emoji_filter:
            filters.append(f'emoji.unicode = "{emoji_filter}"')
        if user_filter:
            filters.append(f'user.name = "{user_filter}"')
        if filters:
            params["filter"] = " AND ".join(filters)
        return await self._get(
            f"{self._base_chat}/{message_name}/reactions",
            access_token=access_token,
            params=params,
            endpoint_label="spaces.messages.reactions.list",
        )

    async def list_messages_by_thread(
        self,
        access_token: str,
        space_id: str,
        thread_name: str,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        """List messages in a specific thread, oldest-first (natural reading order).

        Uses the documented filter grammar: `thread.name=spaces/{s}/threads/{t}`.
        """
        params: dict[str, str] = {
            "pageSize": str(min(limit, 100)),
            "filter": f'thread.name = "{thread_name}"',
            "orderBy": "createTime asc",
        }
        data = await self._get(
            f"{self._base_chat}/{space_id}/messages",
            access_token=access_token,
            params=params,
            endpoint_label="spaces.messages.list",
        )
        return data.get("messages", [])[:limit]

    # ---------- Memberships ----------

    async def add_member(self, access_token: str, space_id: str, user_email: str) -> dict[str, Any]:
        """Invite a user to a space via `spaces.members.create`."""
        body = _build_add_member_body(user_email=user_email)
        return await self._post(
            f"{self._base_chat}/{space_id}/members",
            access_token=access_token,
            json=body,
            endpoint_label="spaces.members.create",
        )

    async def remove_member(self, access_token: str, membership_name: str) -> dict[str, Any]:
        """Remove a membership by full resource name via `spaces.members.delete`."""
        return await self._delete(
            f"{self._base_chat}/{membership_name}",
            access_token=access_token,
            endpoint_label="spaces.members.delete",
        )

    # ---------- People ----------

    async def resolve_person(self, access_token: str, user_id: str) -> dict[str, Any] | None:
        """Resolve `users/{id}` -> primary email + display name. None on 404."""
        resource = user_id.replace("users/", "people/", 1)
        return await self._get_optional(
            f"{self._base_people}/{resource}",
            access_token=access_token,
            params={"personFields": "emailAddresses,names"},
            endpoint_label="people.get",
        )

    async def search_directory_people(
        self, access_token: str, query: str, limit: int
    ) -> list[dict[str, Any]]:
        """Search the caller's Workspace directory via `people:searchDirectoryPeople`.

        `sources[]` is REQUIRED by Google — omitting it returns 400. We pass
        both `DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE` (Workspace members) and
        `DIRECTORY_SOURCE_TYPE_DOMAIN_CONTACT` (domain-shared external
        contacts) so the same call covers both directory populations.
        """
        params: list[tuple[str, str]] = [
            ("query", query),
            ("readMask", "emailAddresses,names"),
            ("pageSize", str(min(limit, 500))),
            ("sources", "DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE"),
            ("sources", "DIRECTORY_SOURCE_TYPE_DOMAIN_CONTACT"),
        ]
        data = await self._get(
            f"{self._base_people}/people:searchDirectoryPeople",
            access_token=access_token,
            params=params,
            endpoint_label="people.searchDirectoryPeople",
        )
        people = data.get("people", [])
        return people if isinstance(people, list) else []

    async def search_contacts(
        self, access_token: str, query: str, limit: int
    ) -> list[dict[str, Any]]:
        """Search the caller's own contacts via `people:searchContacts`.

        This is the consumer-Gmail fallback for `search_people`: it matches
        the caller's personal contact list plus "other contacts"
        auto-populated from interactions. Returns `Person` objects wrapped
        in `{person: ...}` entries — we unwrap here so the result shape
        matches `search_directory_people`.
        """
        data = await self._get(
            f"{self._base_people}/people:searchContacts",
            access_token=access_token,
            params={
                "query": query,
                "readMask": "emailAddresses,names",
                "pageSize": str(min(limit, 30)),
            },
            endpoint_label="people.searchContacts",
        )
        raw = data.get("results", [])
        if not isinstance(raw, list):
            return []
        people: list[dict[str, Any]] = []
        for entry in raw:
            if isinstance(entry, dict):
                person = entry.get("person")
                if isinstance(person, dict):
                    people.append(person)
        return people

    # ---------- OIDC / identity ----------

    async def get_userinfo(self, access_token: str) -> dict[str, Any]:
        """Fetch the OIDC /userinfo payload for the authenticated user.

        Requires the `openid email profile` scope set (included in v2 scopes).
        Returns {sub, email, email_verified, name, picture, ...}.
        """
        return await self._get(
            f"{self._base_oidc}/userinfo",
            access_token=access_token,
            endpoint_label="oidc.userinfo",
        )

    # ---------- internals ----------

    async def _paginate(
        self,
        *,
        url: str,
        access_token: str,
        items_key: str,
        limit: int,
        endpoint_label: str,
        extra_params: Mapping[str, str] | None = None,
    ) -> list[dict[str, Any]]:
        """Follow a Google list endpoint's pagination until `limit` items are collected."""
        items: list[dict[str, Any]] = []
        page_token: str | None = None
        while True:
            remaining = limit - len(items)
            params: dict[str, str] = {"pageSize": str(min(remaining, 100))}
            if extra_params:
                params.update(extra_params)
            if page_token:
                params["pageToken"] = page_token
            data = await self._get(
                url, access_token=access_token, params=params, endpoint_label=endpoint_label
            )
            items.extend(data.get(items_key, []))
            page_token = data.get("nextPageToken")
            if not page_token or len(items) >= limit:
                break
        return items[:limit]

    async def _get_optional(
        self,
        url: str,
        access_token: str,
        params: _QueryParams | None = None,
        endpoint_label: str = "",
    ) -> dict[str, Any] | None:
        """GET that converts upstream 404 into None; other errors re-raise."""
        try:
            return await self._get(
                url,
                access_token=access_token,
                params=params,
                endpoint_label=endpoint_label,
            )
        except ChatApiError as exc:
            if exc.status_code == 404:
                return None
            raise

    async def _get(
        self,
        url: str,
        access_token: str,
        params: _QueryParams | None = None,
        endpoint_label: str = "",
    ) -> dict[str, Any]:
        return await self._request(
            "GET", url, access_token, params=params, endpoint_label=endpoint_label
        )

    async def _post(
        self,
        url: str,
        access_token: str,
        json: Mapping[str, Any],
        params: _QueryParams | None = None,
        endpoint_label: str = "",
    ) -> dict[str, Any]:
        return await self._request(
            "POST",
            url,
            access_token,
            json=json,
            params=params,
            endpoint_label=endpoint_label,
        )

    async def _patch(
        self,
        url: str,
        access_token: str,
        json: Mapping[str, Any],
        params: _QueryParams | None = None,
        endpoint_label: str = "",
    ) -> dict[str, Any]:
        return await self._request(
            "PATCH",
            url,
            access_token,
            json=json,
            params=params,
            endpoint_label=endpoint_label,
        )

    async def _delete(
        self,
        url: str,
        access_token: str,
        endpoint_label: str = "",
    ) -> dict[str, Any]:
        return await self._request(
            "DELETE",
            url,
            access_token,
            endpoint_label=endpoint_label,
        )

    async def _request(
        self,
        method: str,
        url: str,
        access_token: str,
        json: Mapping[str, Any] | None = None,
        params: _QueryParams | None = None,
        endpoint_label: str = "",
    ) -> dict[str, Any]:
        headers = {
            "Authorization": f"Bearer {access_token}",
            "Accept": "application/json",
        }
        if json is not None:
            headers["Content-Type"] = "application/json"
        attempt = 0
        while True:
            attempt += 1
            with mcp_google_api_latency_seconds.labels(endpoint_label).time():
                resp = await self._client.request(
                    method,
                    url,
                    headers=headers,
                    params=params,  # ty: ignore[invalid-argument-type]
                    json=json,
                )
            mcp_google_api_calls_total.labels(endpoint_label, str(resp.status_code)).inc()
            # Tighten the success branch to 2xx only — `follow_redirects=False`
            # means a 3xx falls into the success path under `< 400`, returning
            # an empty dict that masks the redirect as "Google returned no
            # data" and silently drops the Location header. Treat 3xx as a
            # ChatApiError so callers see what happened.
            if 200 <= resp.status_code < 300:
                return resp.json() if resp.content else {}
            if _is_retryable(resp.status_code) and attempt <= self._max_retries:
                sleep_for = _backoff_seconds(attempt, resp)
                logger.warning(
                    "upstream_retry",
                    endpoint=endpoint_label,
                    status=resp.status_code,
                    attempt=attempt,
                    sleep_seconds=round(sleep_for, 3),
                )
                await asyncio.sleep(sleep_for)
                continue
            message, google_status, google_reason = _parse_error_payload(resp)
            logger.error(
                "upstream_error",
                endpoint=endpoint_label,
                status=resp.status_code,
                url=_scrub_query(url, params),
            )
            raise ChatApiError(
                resp.status_code,
                message,
                endpoint_label,
                google_status=google_status,
                google_reason=google_reason,
            )


def _build_add_member_body(*, user_email: str) -> dict[str, Any]:
    """Pure builder for the `spaces.members.create` request body.

    Shared by the real POST path and `add_member`'s dry_run branch — same
    dry/real-parity pattern as `_build_send_message_body`.
    """
    return {"member": {"name": f"users/{user_email}", "type": "HUMAN"}}


def _build_setup_space_body(
    *,
    space_type: SpaceType,
    display_name: str | None,
    member_emails: list[str],
) -> dict[str, Any]:
    """Pure builder for the `spaces.setup` request body.

    `displayName` is included **only** when ``space_type == "SPACE"`` AND
    ``display_name`` is set. Google 400s on a displayName for DIRECT_MESSAGE
    or GROUP_CHAT, so the check is load-bearing — the tool I/O models cap
    it from above, this is the last-mile guard. Shared by real-post and
    dry_run paths (same invariant as `_build_send_message_body`).
    """
    space: dict[str, Any] = {"spaceType": space_type}
    if space_type == "SPACE" and display_name is not None:
        space["displayName"] = display_name
    body: dict[str, Any] = {
        "space": space,
        "memberships": [
            {"member": {"name": f"users/{email}", "type": "HUMAN"}} for email in member_emails
        ],
    }
    return body


def _build_update_message_body(*, text: str) -> dict[str, Any]:
    """Pure builder for the `spaces.messages.patch` request body.

    Text-only edits — `updateMask=text` is set on the URL by the caller,
    not the body. Shared by real-PATCH and `update_message`'s dry_run
    branch (same dry/real-parity contract as `_build_send_message_body`).
    """
    return {"text": text}


def _build_update_space_body(
    *, display_name: str | None, description: str | None
) -> tuple[dict[str, Any], str]:
    """Pure builder for the `spaces.patch` request body + `updateMask`.

    Shared by real-PATCH and `update_space`'s dry_run branch. Body shape
    and mask-path constraints (description nests under `spaceDetails`;
    `updateMask` accepts only top-level paths) are documented on
    `UpdateSpaceInput` in `src/models.py`.
    """
    body: dict[str, Any] = {}
    mask_parts: list[str] = []
    if display_name is not None:
        body["displayName"] = display_name
        mask_parts.append("displayName")
    if description is not None:
        body["spaceDetails"] = {"description": description}
        mask_parts.append("spaceDetails")
    return body, ",".join(mask_parts)


def _build_send_message_body(
    *, text: str, thread_name: str | None
) -> tuple[dict[str, Any], dict[str, str]]:
    """Pure builder for the spaces.messages.create request body + query params.

    Shared by the real POST path and `send_message`'s dry_run branch; dry/real
    parity is a test assertion — if this function returns X, a live POST sends X.
    """
    params: dict[str, str] = {}
    body: dict[str, Any] = {"text": text}
    if thread_name:
        body["thread"] = {"name": thread_name}
        params["messageReplyOption"] = "REPLY_MESSAGE_OR_FAIL"
    return body, params


def _is_retryable(status: int) -> bool:
    return status == 429 or 500 <= status < 600


def _backoff_seconds(attempt: int, resp: httpx.Response) -> float:
    retry_after = resp.headers.get("Retry-After")
    if retry_after:
        try:
            return float(retry_after)
        except ValueError:
            pass
    base = 0.5 * (2 ** (attempt - 1))
    jitter = random.uniform(0, 0.25 * base)  # noqa: S311 — jitter, not crypto
    return min(base + jitter, 30.0)


def _parse_error_payload(resp: httpx.Response) -> tuple[str, str | None, str | None]:
    """Pull (message, status, reason) from a Google error body (AIP-193 shape).

    Returns:
        message: human-readable error text (capped at 500 chars).
        status: textual status like "PERMISSION_DENIED" (None if absent).
        reason: first reason from error.details[].reason (None if absent).
    """
    try:
        payload = resp.json()
    except ValueError:
        return resp.text[:500], None, None
    if not isinstance(payload, dict):
        return str(payload)[:500], None, None
    err = payload.get("error", {})
    if not isinstance(err, dict):
        return str(payload)[:500], None, None
    message = str(err.get("message") or err)[:500]
    status_value = err.get("status")
    status = str(status_value) if isinstance(status_value, str) else None
    reason: str | None = None
    details = err.get("details")
    if isinstance(details, list):
        for item in details:
            if isinstance(item, dict):
                item_reason = item.get("reason")
                if isinstance(item_reason, str):
                    reason = item_reason
                    break
    return message, status, reason


def _scrub_query(url: str, params: _QueryParams | None) -> str:
    if not params:
        return url
    pairs = params.items() if isinstance(params, Mapping) else params
    safe = [(k, v) for k, v in pairs if k != "access_token"]
    return f"{url}?{urlencode(safe)}"  # ty: ignore[invalid-argument-type]


@asynccontextmanager
async def lifespan_client(
    **kwargs: Any,
) -> AsyncIterator[ChatClient]:
    """Open one ChatClient for the process lifetime."""
    client = ChatClient(**kwargs)
    try:
        yield client
    finally:
        await client.close()
