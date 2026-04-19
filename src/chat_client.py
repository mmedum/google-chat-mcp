"""Google Chat / People API HTTP client.

Shared `httpx.AsyncClient` with connection pooling, 10s timeout, exponential
backoff on 5xx and 429. Expects a per-request access token (the caller resolves
it via FastMCP's `get_access_token()` dependency).
"""

from __future__ import annotations

import asyncio
import random
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from typing import Any, Self
from urllib.parse import urlencode

import httpx

from .observability import (
    logger,
    mcp_google_api_calls_total,
    mcp_google_api_latency_seconds,
)


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
        client: httpx.AsyncClient | None = None,
    ) -> None:
        self._base_chat = base_chat
        self._base_people = base_people
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
        body = {
            "space": {"spaceType": "DIRECT_MESSAGE"},
            "memberships": [{"member": {"name": f"users/{user_email}", "type": "HUMAN"}}],
        }
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
        params: dict[str, str] = {}
        body: dict[str, Any] = {"text": text}
        if thread_name:
            body["thread"] = {"name": thread_name}
            params["messageReplyOption"] = "REPLY_MESSAGE_OR_FAIL"
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
        params: Mapping[str, str] | None = None,
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
        params: Mapping[str, str] | None = None,
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
        params: Mapping[str, str] | None = None,
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

    async def _request(
        self,
        method: str,
        url: str,
        access_token: str,
        json: Mapping[str, Any] | None = None,
        params: Mapping[str, str] | None = None,
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
                    params=params,
                    json=json,
                )
            mcp_google_api_calls_total.labels(endpoint_label, str(resp.status_code)).inc()
            if resp.status_code < 400:
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


def _scrub_query(url: str, params: Mapping[str, str] | None) -> str:
    if not params:
        return url
    return f"{url}?{urlencode({k: v for k, v in params.items() if k != 'access_token'})}"


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
