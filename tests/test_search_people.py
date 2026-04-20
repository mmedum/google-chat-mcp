"""search_people: hybrid directory + contacts lookup, dedupe, cache back-fill."""

from __future__ import annotations

import httpx
import pytest
import respx
from fastmcp.exceptions import ToolError
from src.models import SearchPeopleInput
from src.tools import search_people_handler
from src.tools._common import ToolContext


def _person(resource_name: str, email: str | None, display_name: str | None) -> dict:
    """Build a People API `Person` with primary email + display name."""
    payload: dict = {"resourceName": resource_name}
    if email is not None:
        payload["emailAddresses"] = [{"metadata": {"primary": True}, "value": email}]
    if display_name is not None:
        payload["names"] = [{"metadata": {"primary": True}, "displayName": display_name}]
    return payload


@pytest.mark.asyncio
async def test_directory_hit_populates_result_and_cache(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "people": [
                        _person("people/109876543210", "jesper@example.com", "Jesper"),
                    ]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="jesper", sources=["DIRECTORY"]),
        )
    assert result.total_returned == 1
    assert result.sources_succeeded == ["DIRECTORY"]
    hit = result.people[0]
    assert hit.email == "jesper@example.com"
    assert hit.display_name == "Jesper"
    assert hit.user_id == "users/109876543210"
    assert hit.source == "DIRECTORY"

    # Cache back-fill: a later lookup by users/{id} resolves without a new
    # People API call.
    cached = await tool_ctx.directory_cache.get("users/109876543210")
    assert cached is not None
    email, display_name = cached
    assert email == "jesper@example.com"
    assert display_name == "Jesper"


@pytest.mark.asyncio
async def test_contact_id_results_do_not_poison_cache(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Contact IDs (`people/c{hex}`) surface in the result but DO NOT write to cache."""
    with (
        respx.mock(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "results": [{"person": _person("people/c1234abcd", "kim@example.com", "Kim")}]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="kim", sources=["CONTACTS"]),
        )
    assert result.total_returned == 1
    hit = result.people[0]
    assert hit.email == "kim@example.com"
    assert hit.user_id is None  # contact IDs don't round-trip
    assert hit.source == "CONTACTS"

    # No cache entry — contact IDs are filtered at the DirectoryCache boundary
    # because they don't share the users/{id} namespace with Chat messages.
    assert await tool_ctx.directory_cache.get("users/c1234abcd") is None


@pytest.mark.asyncio
async def test_hybrid_fan_out_merges_and_dedupes(tool_ctx: ToolContext, mock_access_token) -> None:
    """When both sources return the same person, DIRECTORY wins on the dedupe."""
    with (
        respx.mock(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "people": [_person("people/111", "a@x.com", "Alice")],
                },
            )
        )
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "results": [
                        {"person": _person("people/111", "a@x.com", "Alice-contacts")},
                        {"person": _person("people/c999", "b@x.com", "Bob")},
                    ]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="a"),  # ty: ignore[missing-argument]
        )
    assert result.total_returned == 2
    by_email = {h.email: h for h in result.people}
    # people/111 deduped — DIRECTORY wins (listed first in default sources).
    assert by_email["a@x.com"].source == "DIRECTORY"
    assert by_email["a@x.com"].display_name == "Alice"
    # Contact-only hit survives.
    assert by_email["b@x.com"].source == "CONTACTS"
    assert by_email["b@x.com"].user_id is None


@pytest.mark.asyncio
async def test_one_source_missing_scope_continues_with_the_other(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """directory.readonly denied → fall back to contacts, still return hits."""
    with (
        respx.mock(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx.Response(
                403,
                json={
                    "error": {
                        "code": 403,
                        "message": "Request had insufficient authentication scopes.",
                        "status": "PERMISSION_DENIED",
                        "details": [
                            {
                                "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                                "reason": "ACCESS_TOKEN_SCOPE_INSUFFICIENT",
                            }
                        ],
                    }
                },
            )
        )
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx.Response(
                200,
                json={"results": [{"person": _person("people/c1", "b@x.com", "Bob")}]},
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="bob"),  # ty: ignore[missing-argument]
        )
    assert result.sources_attempted == ["DIRECTORY", "CONTACTS"]
    assert result.sources_succeeded == ["CONTACTS"]
    assert result.total_returned == 1
    assert result.people[0].email == "b@x.com"


@pytest.mark.asyncio
async def test_all_sources_missing_scope_raises(tool_ctx: ToolContext, mock_access_token) -> None:
    """If every requested source is missing-scope, raise (don't silently empty)."""
    missing_scope = httpx.Response(
        403,
        json={
            "error": {
                "code": 403,
                "message": "Request had insufficient authentication scopes.",
                "status": "PERMISSION_DENIED",
                "details": [
                    {
                        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                        "reason": "ACCESS_TOKEN_SCOPE_INSUFFICIENT",
                    }
                ],
            }
        },
    )
    with (
        respx.mock(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(return_value=missing_scope)
        mock.get(url__regex=r".*people:searchContacts.*").mock(return_value=missing_scope)
        with pytest.raises(ToolError, match="scope"):
            await search_people_handler(
                tool_ctx,
                SearchPeopleInput(query="nobody"),  # ty: ignore[missing-argument]
            )


def test_empty_query_rejected_at_model_boundary() -> None:
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        SearchPeopleInput(query="")  # ty: ignore[missing-argument]


def test_limit_bounds_enforced() -> None:
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        SearchPeopleInput(query="x", limit=0)  # ty: ignore[missing-argument]
    with pytest.raises(ValidationError):
        SearchPeopleInput(query="x", limit=101)  # ty: ignore[missing-argument]


def test_default_sources_is_hybrid() -> None:
    payload = SearchPeopleInput(query="x")  # ty: ignore[missing-argument]
    assert payload.sources == ["DIRECTORY", "CONTACTS"]
