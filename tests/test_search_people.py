"""search_people: hybrid directory + contacts lookup, dedupe, cache back-fill."""

from __future__ import annotations

import httpx2
import pytest
from fastmcp.exceptions import ToolError
from src.models import SearchPeopleInput
from src.tools import search_people_handler
from src.tools._common import ToolContext

from ._httpx2_mock import mock_api


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
        mock_api(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "people": [
                        _person("people/109876543210", "janedoe@example.com", "Jane Doe"),
                    ]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="janedoe", sources=["DIRECTORY"]),
        )
    assert result.total_returned == 1
    assert result.sources_succeeded == ["DIRECTORY"]
    hit = result.people[0]
    assert hit.email == "janedoe@example.com"
    assert hit.display_name == "Jane Doe"
    assert hit.user_id == "users/109876543210"
    assert hit.source == "DIRECTORY"

    # Cache back-fill: a later lookup by users/{id} resolves without a new
    # People API call.
    cached = await tool_ctx.directory_cache.get("users/109876543210")
    assert cached is not None
    email, display_name = cached
    assert email == "janedoe@example.com"
    assert display_name == "Jane Doe"


@pytest.mark.asyncio
async def test_contact_id_results_do_not_poison_cache(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Contact IDs (`people/c{hex}`) surface in the result but DO NOT write to cache."""
    with (
        mock_api(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "results": [
                        {"person": _person("people/c1234abcd", "johndoe@example.com", "John Doe")}
                    ]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="johndoe", sources=["CONTACTS"]),
        )
    assert result.total_returned == 1
    hit = result.people[0]
    assert hit.email == "johndoe@example.com"
    assert hit.user_id is None  # contact IDs don't round-trip
    assert hit.source == "CONTACTS"

    # No cache entry — contact IDs are filtered at the DirectoryCache boundary
    # because they don't share the users/{id} namespace with Chat messages.
    assert await tool_ctx.directory_cache.get("users/c1234abcd") is None


@pytest.mark.asyncio
async def test_hybrid_fan_out_merges_and_dedupes(tool_ctx: ToolContext, mock_access_token) -> None:
    """When both sources return the same person, DIRECTORY wins on the dedupe."""
    with (
        mock_api(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "people": [_person("people/111", "a@x.com", "Alice")],
                },
            )
        )
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx2.Response(
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
            SearchPeopleInput(query="a"),
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
        mock_api(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx2.Response(
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
            return_value=httpx2.Response(
                200,
                json={"results": [{"person": _person("people/c1", "b@x.com", "Bob")}]},
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="bob"),
        )
    assert result.sources_attempted == ["DIRECTORY", "CONTACTS"]
    assert result.sources_succeeded == ["CONTACTS"]
    assert result.total_returned == 1
    assert result.people[0].email == "b@x.com"


@pytest.mark.asyncio
async def test_directory_sharing_disabled_degrades_to_contacts(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Workspace admin has disabled directory sharing → 403 without the
    missing-scope reason. The hybrid must still return CONTACTS hits rather
    than failing the whole call."""
    with (
        mock_api(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx2.Response(
                403,
                json={
                    "error": {
                        "code": 403,
                        "message": "The G Suite domain admin has disabled external directory sharing.",
                        "status": "PERMISSION_DENIED",
                    }
                },
            )
        )
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "results": [{"person": _person("people/c1", "janedoe@example.com", "Jane Doe")}]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx,
            SearchPeopleInput(query="janedoe"),
        )
    assert result.sources_succeeded == ["CONTACTS"]
    assert result.total_returned == 1
    assert result.people[0].email == "janedoe@example.com"


@pytest.mark.asyncio
async def test_all_sources_non_scope_error_raises_with_reasons(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """When every source fails with a non-scope error (network, admin
    config, etc), raise with the upstream messages — the admin needs
    to see what actually broke, not a misleading re-consent prompt."""
    directory_error = httpx2.Response(
        403,
        json={
            "error": {
                "code": 403,
                "message": "directory sharing disabled",
                "status": "PERMISSION_DENIED",
            }
        },
    )
    contacts_error = httpx2.Response(
        500, json={"error": {"code": 500, "message": "internal error"}}
    )
    with (
        mock_api(base_url="https://people.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(return_value=directory_error)
        # _request has a 5xx retry loop (max_retries=3) before ChatApiError
        # propagates. respx returns the same response for each retry.
        mock.get(url__regex=r".*people:searchContacts.*").mock(return_value=contacts_error)
        with pytest.raises(ToolError, match="all sources failed"):
            await search_people_handler(
                tool_ctx,
                SearchPeopleInput(query="x"),
            )


@pytest.mark.asyncio
async def test_all_sources_missing_scope_raises(tool_ctx: ToolContext, mock_access_token) -> None:
    """If every requested source is missing-scope, raise (don't silently empty)."""
    missing_scope = httpx2.Response(
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
        mock_api(base_url="https://people.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(return_value=missing_scope)
        mock.get(url__regex=r".*people:searchContacts.*").mock(return_value=missing_scope)
        with pytest.raises(ToolError, match="scope"):
            await search_people_handler(
                tool_ctx,
                SearchPeopleInput(query="nobody"),
            )


def test_empty_query_rejected_at_model_boundary() -> None:
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        SearchPeopleInput(query="")


def test_limit_bounds_enforced() -> None:
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        SearchPeopleInput(query="x", limit=0)
    with pytest.raises(ValidationError):
        SearchPeopleInput(query="x", limit=101)


def test_default_sources_is_hybrid() -> None:
    payload = SearchPeopleInput(query="x")
    assert payload.sources == ["DIRECTORY", "CONTACTS"]


@pytest.mark.parametrize(
    "address",
    [
        pytest.param("bob@nas.local", id="internal_hostname"),
        pytest.param("admin@router", id="single_label_domain"),
        pytest.param("j.doe@example.com.", id="trailing_dot"),
    ],
)
@pytest.mark.asyncio
async def test_user_entered_contact_address_still_returns_a_hit(
    tool_ctx: ToolContext, mock_access_token, address: str
) -> None:
    """The CONTACTS source returns the caller's own address book.

    Those cards are typed by a human, not issued by Workspace, so they hold
    whatever the user saved — an internal hostname, a bare domain, a stray
    trailing dot. `PersonHit.email` used to be a strict `EmailStr`, so one such
    card made the *entire* `search_people` call fail with `Internal error.`,
    hiding every other hit in the result. These fields are `str` now: the value
    reaches the caller exactly as Google returned it.
    """
    with mock_api(base_url="https://people.test/v1") as mock, mock_access_token():
        mock.get(url__regex=r".*people:searchDirectoryPeople.*").mock(
            return_value=httpx2.Response(200, json={"people": []})
        )
        mock.get(url__regex=r".*people:searchContacts.*").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "results": [
                        {"person": _person("people/c111", address, "Saved Contact")},
                        {"person": _person("people/c222", "ok@example.com", "Other Contact")},
                    ]
                },
            )
        )
        result = await search_people_handler(
            tool_ctx, SearchPeopleInput(query="contact", sources=["DIRECTORY", "CONTACTS"])
        )

    emails = [hit.email for hit in result.people]
    assert address in emails, "the odd card must not be dropped"
    assert "ok@example.com" in emails, "and must not take the other hits down with it"
    assert result.total_returned == 2
