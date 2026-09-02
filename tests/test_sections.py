"""Sidebar sections: the seven `users.sections` / `users.sections.items` tools."""

from __future__ import annotations

import json
from collections.abc import Iterator
from contextlib import contextmanager

import httpx2
import pytest
from fastmcp.exceptions import ToolError
from pydantic import ValidationError
from src.chat_client import (
    _build_create_section_body,
    _build_position_section_body,
    _build_update_section_body,
)
from src.models import (
    CreateSectionInput,
    DeleteSectionInput,
    ListSectionItemsInput,
    ListSectionsInput,
    MoveSpaceToSectionInput,
    PositionSectionInput,
    RenameSectionInput,
)
from src.tools import (
    create_section_handler,
    delete_section_handler,
    list_section_items_handler,
    list_sections_handler,
    move_space_to_section_handler,
    position_section_handler,
    rename_section_handler,
)
from src.tools._common import ToolContext

from ._httpx2_mock import MockRouter, mock_api
from .conftest import scope_403

_CLIENTS = "users/123/sections/clients-1"
_DEFAULT_SPACES = "users/123/sections/default-spaces"
_SPACE = "spaces/AAA"
# Google derives the item id from the space: base64url of `spaces/AAA`,
# unpadded. Using the real encoding rather than a synthetic "AAA" means
# every test below exercises the shape the live API actually returns.
_ITEM_ID = "c3BhY2VzL0FBQQ"
_ITEM = f"{_DEFAULT_SPACES}/items/{_ITEM_ID}"


@contextmanager
def _api(mock_access_token, *, all_called: bool = True) -> Iterator[MockRouter]:
    """The mocked Chat API plus a patched upstream token — every test opens with it."""
    with (
        mock_api(base_url="https://chat.test/v1", assert_all_called=all_called) as mock,
        mock_access_token(),
    ):
        yield mock


# ---------- list_sections ----------


@pytest.mark.asyncio
async def test_list_sections_labels_system_sections(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """System sections carry no displayName; the tool supplies a readable one."""
    with _api(mock_access_token) as mock:
        mock.get("/users/me/sections").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "sections": [
                        {"name": _CLIENTS, "type": "CUSTOM_SECTION", "displayName": "Clients"},
                        {"name": _DEFAULT_SPACES, "type": "DEFAULT_SPACES", "sortOrder": 2},
                        {
                            "name": "users/123/sections/default-direct-messages",
                            "type": "DEFAULT_DIRECT_MESSAGES",
                        },
                        {"name": "users/123/sections/default-apps", "type": "DEFAULT_APPS"},
                    ]
                },
            )
        )
        result = await list_sections_handler(tool_ctx, ListSectionsInput())

    assert [s.display_name for s in result.sections] == [
        "Clients",
        "(spaces)",
        "(direct messages)",
        "(apps)",
    ]
    assert result.sections[0].type == "CUSTOM_SECTION"
    assert result.sections[1].sort_order == 2
    assert result.sections[0].sort_order is None
    assert result.next_page_token is None


@pytest.mark.asyncio
async def test_list_sections_buckets_unknown_type(tool_ctx: ToolContext, mock_access_token) -> None:
    """A section type we don't know yet must not fail the whole listing."""
    with _api(mock_access_token) as mock:
        mock.get("/users/me/sections").mock(
            return_value=httpx2.Response(
                200,
                json={"sections": [{"name": _DEFAULT_SPACES, "type": "DEFAULT_STARRED"}]},
            )
        )
        result = await list_sections_handler(tool_ctx, ListSectionsInput())

    assert result.sections[0].type == "SECTION_TYPE_UNSPECIFIED"
    assert result.sections[0].display_name == "(unnamed section)"


# ---------- list_section_items ----------


@pytest.mark.asyncio
async def test_list_section_items_reads_one_section(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        route = mock.get(f"/{_CLIENTS}/items").mock(
            return_value=httpx2.Response(
                200,
                json={"sectionItems": [{"name": f"{_CLIENTS}/items/{_ITEM_ID}", "space": _SPACE}]},
            )
        )
        result = await list_section_items_handler(
            tool_ctx, ListSectionItemsInput(section_name=_CLIENTS)
        )

    assert "filter" not in str(route.calls[0].request.url)
    assert result.items[0].space_id == _SPACE
    assert result.items[0].section_name == _CLIENTS


@pytest.mark.asyncio
async def test_space_filter_searches_every_section(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """No section_name → Google's `-` wildcard parent, which is what locates a space."""
    with _api(mock_access_token) as mock:
        route = mock.get("/users/me/sections/-/items").mock(
            return_value=httpx2.Response(
                200, json={"sectionItems": [{"name": _ITEM, "space": _SPACE}]}
            )
        )
        result = await list_section_items_handler(tool_ctx, ListSectionItemsInput(space_id=_SPACE))

    assert route.calls[0].request.url.params["filter"] == f"space = {_SPACE}"
    assert result.items[0].section_name == _DEFAULT_SPACES


@pytest.mark.asyncio
async def test_section_item_without_a_space_survives(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """`SectionItem.item` is a union — a member type we don't model yet is not fatal."""
    with _api(mock_access_token) as mock:
        mock.get(f"/{_CLIENTS}/items").mock(
            return_value=httpx2.Response(
                200, json={"sectionItems": [{"name": f"{_CLIENTS}/items/{_ITEM_ID}"}]}
            )
        )
        result = await list_section_items_handler(
            tool_ctx, ListSectionItemsInput(section_name=_CLIENTS)
        )

    assert result.items[0].space_id is None


def test_list_section_items_requires_a_selector() -> None:
    # Neither selector → an unfiltered wildcard listing, a shape Google only
    # documents alongside a space filter.
    with pytest.raises(ValidationError):
        ListSectionItemsInput()


# ---------- create_section ----------


@pytest.mark.asyncio
async def test_create_section_posts_custom_type(tool_ctx: ToolContext, mock_access_token) -> None:
    with _api(mock_access_token) as mock:
        route = mock.post("/users/me/sections").mock(
            return_value=httpx2.Response(
                200, json={"name": _CLIENTS, "type": "CUSTOM_SECTION", "displayName": "Clients"}
            )
        )
        result = await create_section_handler(tool_ctx, CreateSectionInput(display_name="Clients"))

    body = json.loads(route.calls[0].request.content.decode())
    assert body == {"displayName": "Clients", "type": "CUSTOM_SECTION"}
    assert result.section_name == _CLIENTS
    assert result.dry_run is False


@pytest.mark.asyncio
async def test_create_section_dry_run_matches_the_real_body(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    payload = CreateSectionInput(display_name="Clients", dry_run=True)
    with _api(mock_access_token, all_called=False) as mock:
        route = mock.post("/users/me/sections")
        result = await create_section_handler(tool_ctx, payload)

    assert route.call_count == 0
    assert result.section_name is None
    assert result.rendered_payload == _build_create_section_body(display_name="Clients")


@pytest.mark.parametrize("name", ["", "x" * 81])
def test_create_section_display_name_bounds(name: str) -> None:
    with pytest.raises(ValidationError):
        CreateSectionInput(display_name=name)


# ---------- rename_section ----------


@pytest.mark.asyncio
async def test_rename_section_masks_display_name(tool_ctx: ToolContext, mock_access_token) -> None:
    with _api(mock_access_token) as mock:
        route = mock.patch(f"/{_CLIENTS}").mock(
            return_value=httpx2.Response(
                200, json={"name": _CLIENTS, "type": "CUSTOM_SECTION", "displayName": "Accounts"}
            )
        )
        result = await rename_section_handler(
            tool_ctx, RenameSectionInput(section_name=_CLIENTS, display_name="Accounts")
        )

    assert "updateMask=displayName" in str(route.calls[0].request.url)
    body = json.loads(route.calls[0].request.content.decode())
    assert body == _build_update_section_body(display_name="Accounts")
    assert result.display_name == "Accounts"


@pytest.mark.asyncio
async def test_rename_section_dry_run_does_not_patch(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token, all_called=False) as mock:
        route = mock.patch(f"/{_CLIENTS}")
        result = await rename_section_handler(
            tool_ctx,
            RenameSectionInput(section_name=_CLIENTS, display_name="Accounts", dry_run=True),
        )

    assert route.call_count == 0
    assert result.rendered_payload == {"displayName": "Accounts"}


def test_rename_rejects_a_malformed_section_name() -> None:
    with pytest.raises(ValidationError):
        RenameSectionInput(section_name="clients-1", display_name="Accounts")


# ---------- delete_section ----------


@pytest.mark.asyncio
async def test_delete_section_reports_deleted(tool_ctx: ToolContext, mock_access_token) -> None:
    with _api(mock_access_token) as mock:
        mock.delete(f"/{_CLIENTS}").mock(return_value=httpx2.Response(200, json={}))
        result = await delete_section_handler(tool_ctx, DeleteSectionInput(section_name=_CLIENTS))

    assert result.deleted is True


@pytest.mark.asyncio
async def test_delete_section_is_idempotent_on_404(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        mock.delete(f"/{_CLIENTS}").mock(
            return_value=httpx2.Response(
                404, json={"error": {"code": 404, "message": "Not found", "status": "NOT_FOUND"}}
            )
        )
        result = await delete_section_handler(tool_ctx, DeleteSectionInput(section_name=_CLIENTS))

    assert result.deleted is False


@pytest.mark.asyncio
async def test_delete_section_surfaces_a_plain_403(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Refusing to delete a system section is a real error, not an already-done.

    This is where `delete_section` deliberately diverges from `delete_message`,
    which treats 403 PERMISSION_DENIED as "already gone".
    """
    with _api(mock_access_token) as mock:
        mock.delete(f"/{_DEFAULT_SPACES}").mock(
            return_value=httpx2.Response(
                403,
                json={
                    "error": {
                        "code": 403,
                        "message": "Cannot delete a system section.",
                        "status": "PERMISSION_DENIED",
                    }
                },
            )
        )
        with pytest.raises(ToolError, match="Google Chat API error"):
            await delete_section_handler(tool_ctx, DeleteSectionInput(section_name=_DEFAULT_SPACES))


@pytest.mark.asyncio
async def test_delete_section_still_raises_on_missing_scope(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        mock.delete(f"/{_CLIENTS}").mock(return_value=scope_403())
        with pytest.raises(ToolError, match="scope"):
            await delete_section_handler(tool_ctx, DeleteSectionInput(section_name=_CLIENTS))


@pytest.mark.asyncio
async def test_delete_section_dry_run_does_not_call(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token, all_called=False) as mock:
        route = mock.delete(f"/{_CLIENTS}")
        result = await delete_section_handler(
            tool_ctx, DeleteSectionInput(section_name=_CLIENTS, dry_run=True)
        )

    assert route.call_count == 0
    assert result.deleted is False
    assert result.dry_run is True


# ---------- position_section ----------


@pytest.mark.asyncio
async def test_position_section_sends_absolute_order(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        route = mock.post(f"/{_CLIENTS}:position").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "section": {
                        "name": _CLIENTS,
                        "type": "CUSTOM_SECTION",
                        "displayName": "Clients",
                        "sortOrder": 1,
                    }
                },
            )
        )
        result = await position_section_handler(
            tool_ctx, PositionSectionInput(section_name=_CLIENTS, sort_order=1)
        )

    body = json.loads(route.calls[0].request.content.decode())
    assert body == {"sortOrder": 1}
    assert result.sort_order == 1


@pytest.mark.asyncio
async def test_position_section_sends_relative_position(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        route = mock.post(f"/{_CLIENTS}:position").mock(
            return_value=httpx2.Response(200, json={"section": {"name": _CLIENTS}})
        )
        result = await position_section_handler(
            tool_ctx, PositionSectionInput(section_name=_CLIENTS, relative_position="START")
        )

    body = json.loads(route.calls[0].request.content.decode())
    assert body == {"relativePosition": "START"}
    # Google returned no sortOrder — informational only, so it must not fail
    # a reorder that already landed.
    assert result.sort_order is None


@pytest.mark.asyncio
async def test_position_section_dry_run_does_not_post(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token, all_called=False) as mock:
        route = mock.post(f"/{_CLIENTS}:position")
        result = await position_section_handler(
            tool_ctx,
            PositionSectionInput(section_name=_CLIENTS, relative_position="END", dry_run=True),
        )

    assert route.call_count == 0
    assert result.rendered_payload == _build_position_section_body(
        sort_order=None, relative_position="END"
    )
    # `sort_order` means "the rank Google reports". Nothing was reordered, so
    # echoing the requested rank here would read as confirmation.
    assert result.sort_order is None


def test_position_body_builder_rejects_an_empty_union() -> None:
    """`ChatClient.position_section` defaults both to None — fail loudly there.

    Without this the builder emits `{"relativePosition": null}` and the caller
    meets Google's 400 a long way from the mistake.
    """
    with pytest.raises(ValueError, match="sort_order"):
        _build_position_section_body(sort_order=None, relative_position=None)


def test_position_rejects_both_fields() -> None:
    # Google models the two as a union; sending both is a 400.
    with pytest.raises(ValidationError):
        PositionSectionInput(section_name=_CLIENTS, sort_order=1, relative_position="START")


def test_position_rejects_neither_field() -> None:
    with pytest.raises(ValidationError):
        PositionSectionInput(section_name=_CLIENTS)


def test_position_rejects_zero_sort_order() -> None:
    with pytest.raises(ValidationError):
        PositionSectionInput(section_name=_CLIENTS, sort_order=0)


# ---------- move_space_to_section ----------


def _lookup_route(mock: MockRouter, *, found: bool = True, item: str = _ITEM):
    """Stub the wildcard lookup that resolves a space to its section item."""
    items = [{"name": item, "space": _SPACE}] if found else []
    return mock.get("/users/me/sections/-/items").mock(
        return_value=httpx2.Response(200, json={"sectionItems": items})
    )


@pytest.mark.asyncio
async def test_move_looks_up_then_moves(tool_ctx: ToolContext, mock_access_token) -> None:
    with _api(mock_access_token) as mock:
        _lookup_route(mock)
        move = mock.post(f"/{_ITEM}:move").mock(
            return_value=httpx2.Response(
                200, json={"sectionItem": {"name": f"{_CLIENTS}/items/{_ITEM_ID}"}}
            )
        )
        result = await move_space_to_section_handler(
            tool_ctx, MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS)
        )

    body = json.loads(move.calls[0].request.content.decode())
    assert body == {"targetSection": _CLIENTS}
    assert result.moved is True
    assert result.from_section == _DEFAULT_SPACES
    assert result.section_name == _CLIENTS
    # The item's name embeds its section, so the move renames it and the name
    # we arrived with now 404s. The tool description tells callers to feed
    # `item_name` back in for a bulk sort, so returning the stale one was
    # actively harmful.
    assert result.item_name == f"{_CLIENTS}/items/{_ITEM_ID}"
    assert result.item_name != _ITEM


@pytest.mark.asyncio
async def test_move_skips_the_call_when_already_filed(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Re-running a bulk sort must not re-POST every space."""
    already = f"{_CLIENTS}/items/{_ITEM_ID}"
    with _api(mock_access_token, all_called=False) as mock:
        _lookup_route(mock, item=already)
        move = mock.post(f"/{already}:move")
        result = await move_space_to_section_handler(
            tool_ctx, MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS)
        )

    assert move.call_count == 0
    assert result.moved is False
    assert result.from_section == _CLIENTS


@pytest.mark.asyncio
async def test_move_dry_run_resolves_the_source_without_writing(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """The lookup is a read, so a dry run can still report what it would leave."""
    with _api(mock_access_token, all_called=False) as mock:
        lookup = _lookup_route(mock)
        move = mock.post(f"/{_ITEM}:move")
        result = await move_space_to_section_handler(
            tool_ctx,
            MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS, dry_run=True),
        )

    assert lookup.call_count == 1
    assert move.call_count == 0
    assert result.moved is False
    assert result.from_section == _DEFAULT_SPACES
    assert result.rendered_payload == {"targetSection": _CLIENTS}


@pytest.mark.asyncio
async def test_move_errors_when_the_space_has_no_section_item(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token, all_called=False) as mock:
        _lookup_route(mock, found=False)
        mock.post(f"/{_ITEM}:move")
        with pytest.raises(ToolError, match="No section item found"):
            await move_space_to_section_handler(
                tool_ctx, MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS)
            )


def test_move_rejects_a_malformed_space_id() -> None:
    with pytest.raises(ValidationError):
        MoveSpaceToSectionInput(space_id="AAA", section_name=_CLIENTS)


@pytest.mark.asyncio
async def test_move_refuses_a_mismatched_lookup_result(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """The lookup trusts a server-side filter; a write must not.

    If Google stopped honouring `filter=space = ...` the first row back would
    be an arbitrary space, and moving it would silently reorganise the wrong
    conversation.
    """
    with _api(mock_access_token, all_called=False) as mock:
        mock.get("/users/me/sections/-/items").mock(
            return_value=httpx2.Response(
                200,
                json={"sectionItems": [{"name": _ITEM, "space": "spaces/SOMEONE-ELSE"}]},
            )
        )
        move = mock.post(f"/{_ITEM}:move")
        with pytest.raises(ToolError, match="Refusing to move"):
            await move_space_to_section_handler(
                tool_ctx, MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS)
            )

    assert move.call_count == 0


# ---------- truncation ----------


@pytest.mark.asyncio
async def test_list_sections_surfaces_the_page_token(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        mock.get("/users/me/sections").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "sections": [{"name": _CLIENTS, "type": "CUSTOM_SECTION", "displayName": "C"}],
                    "nextPageToken": "page-2",
                },
            )
        )
        result = await list_sections_handler(tool_ctx, ListSectionsInput(limit=1))

    assert result.next_page_token == "page-2"


@pytest.mark.asyncio
async def test_list_section_items_reports_a_truncated_section(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """A silently truncated listing is what makes a bulk re-sort skip spaces."""
    with _api(mock_access_token) as mock:
        route = mock.get(f"/{_CLIENTS}/items").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "sectionItems": [{"name": f"{_CLIENTS}/items/{_ITEM_ID}", "space": _SPACE}],
                    "nextPageToken": "page-2",
                },
            )
        )
        result = await list_section_items_handler(
            tool_ctx, ListSectionItemsInput(section_name=_CLIENTS, limit=1)
        )

    assert result.next_page_token == "page-2"
    assert len(result.items) == 1
    assert "pageToken" not in route.calls[0].request.url.params


@pytest.mark.asyncio
async def test_page_token_is_sent_back_upstream(tool_ctx: ToolContext, mock_access_token) -> None:
    with _api(mock_access_token) as mock:
        route = mock.get(f"/{_CLIENTS}/items").mock(
            return_value=httpx2.Response(200, json={"sectionItems": []})
        )
        result = await list_section_items_handler(
            tool_ctx, ListSectionItemsInput(section_name=_CLIENTS, page_token="page-2")
        )

    assert route.calls[0].request.url.params["pageToken"] == "page-2"
    assert result.next_page_token is None


# ---------- move fast path ----------


@pytest.mark.asyncio
async def test_item_name_skips_the_lookup(tool_ctx: ToolContext, mock_access_token) -> None:
    """The whole point of the hint: a bulk sort shouldn't pay a lookup per space."""
    with _api(mock_access_token, all_called=False) as mock:
        lookup = mock.get("/users/me/sections/-/items")
        move = mock.post(f"/{_ITEM}:move").mock(
            return_value=httpx2.Response(200, json={"sectionItem": {"name": _ITEM}})
        )
        result = await move_space_to_section_handler(
            tool_ctx,
            MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS, item_name=_ITEM),
        )

    assert lookup.call_count == 0
    assert move.call_count == 1
    assert result.moved is True
    assert result.item_name == _ITEM
    assert result.from_section == _DEFAULT_SPACES
    # `space_id` is echoed from the input, never derived from the item id —
    # that id is undocumented base64 of the resource name.
    assert result.space_id == _SPACE


@pytest.mark.asyncio
async def test_item_name_already_in_target_skips_both_calls(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    already = f"{_CLIENTS}/items/{_ITEM_ID}"
    with _api(mock_access_token, all_called=False) as mock:
        lookup = mock.get("/users/me/sections/-/items")
        move = mock.post(f"/{already}:move")
        result = await move_space_to_section_handler(
            tool_ctx,
            MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS, item_name=already),
        )

    assert lookup.call_count == 0
    assert move.call_count == 0
    assert result.moved is False


@pytest.mark.asyncio
async def test_stale_item_name_fails_rather_than_moving_the_wrong_space(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """A hint from a listing taken before someone re-filed the space.

    `item_name` encodes the section the item was in, so a stale one addresses a
    path that no longer exists and Google 404s. That is the safety property
    that lets the fast path skip the mismatch guard.
    """
    stale = f"{_DEFAULT_SPACES}/items/{_ITEM_ID}"
    with _api(mock_access_token) as mock:
        mock.post(f"/{stale}:move").mock(
            return_value=httpx2.Response(
                404, json={"error": {"code": 404, "message": "Not found", "status": "NOT_FOUND"}}
            )
        )
        with pytest.raises(ToolError, match="Google Chat API error"):
            await move_space_to_section_handler(
                tool_ctx,
                MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS, item_name=stale),
            )


def test_move_rejects_a_malformed_item_name() -> None:
    with pytest.raises(ValidationError):
        MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS, item_name="not-an-item")


@pytest.mark.asyncio
async def test_position_coerces_a_numeric_string_sort_order(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Agree with `_ChatSectionResponse`, which pydantic coerces the same way.

    Returning None here while `list_sections` reported 3 made the two tools
    disagree about one field of one section.
    """
    with _api(mock_access_token) as mock:
        mock.post(f"/{_CLIENTS}:position").mock(
            return_value=httpx2.Response(
                200, json={"section": {"name": _CLIENTS, "sortOrder": "3"}}
            )
        )
        result = await position_section_handler(
            tool_ctx, PositionSectionInput(section_name=_CLIENTS, sort_order=3)
        )

    assert result.sort_order == 3


@pytest.mark.parametrize("drifted", [True, "not-a-number", 1.5, None])
@pytest.mark.asyncio
async def test_position_drops_a_sort_order_it_cannot_trust(
    tool_ctx: ToolContext, mock_access_token, drifted: object
) -> None:
    """`isinstance(True, int)` is True, and the strict result model rejects a bool.

    That turned a reorder which had already landed into the non-retryable
    "call SUCCEEDED but its response could not be read" error.
    """
    with _api(mock_access_token) as mock:
        mock.post(f"/{_CLIENTS}:position").mock(
            return_value=httpx2.Response(
                200, json={"section": {"name": _CLIENTS, "sortOrder": drifted}}
            )
        )
        result = await position_section_handler(
            tool_ctx, PositionSectionInput(section_name=_CLIENTS, sort_order=1)
        )

    assert result.sort_order is None


@pytest.mark.asyncio
async def test_position_tolerates_a_response_without_a_section(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Same contract as the non-integer case, for the envelope going missing."""
    with _api(mock_access_token) as mock:
        mock.post(f"/{_CLIENTS}:position").mock(return_value=httpx2.Response(200, json={}))
        result = await position_section_handler(
            tool_ctx, PositionSectionInput(section_name=_CLIENTS, relative_position="END")
        )

    assert result.sort_order is None
    assert result.dry_run is False


# ---------- resource-name shapes ----------


@pytest.mark.parametrize(
    "item_name",
    [
        # What Google's live API actually returns: base64url of the space name.
        "users/109876543210/sections/default-spaces/items/c3BhY2VzL0FBQQ==",
        # What Google's own docs write instead — the space name, unencoded.
        "users/me/sections/default-spaces/items/spaces/123456",
    ],
)
def test_both_documented_item_name_shapes_validate(item_name: str) -> None:
    """Google's docs and its live API disagree on this segment; accept either.

    Rejecting the shape we don't currently see would turn the day Google
    switches into a total outage on every section listing.
    """
    assert (
        MoveSpaceToSectionInput(
            space_id=_SPACE, section_name=_CLIENTS, item_name=item_name
        ).item_name
        == item_name
    )


@pytest.mark.parametrize(
    "item_name",
    [
        "users/me/sections/x/items/../../evil",
        "users/me/sections/x/items/spaces/../evil",
        "users/me/sections/x/items/spaces/a/b",
    ],
)
def test_item_name_still_rejects_traversal(item_name: str) -> None:
    """The optional `spaces/` prefix must not open a dot-segment path."""
    with pytest.raises(ValidationError):
        MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS, item_name=item_name)


@pytest.mark.asyncio
async def test_users_me_target_matches_googles_canonical_name(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """`users/me/...` and `users/{id}/...` are the same section, so this no-ops.

    Google takes `me` on the way in and canonicalises on the way out, so a
    caller who sorts using `me` names compares them against ids coming back.
    Raw string equality would call every already-filed space a move.
    """
    canonical_item = f"{_CLIENTS}/items/c3BhY2VzL0FBQQ=="
    with _api(mock_access_token, all_called=False) as mock:
        move = mock.post(f"/{canonical_item}:move")
        result = await move_space_to_section_handler(
            tool_ctx,
            MoveSpaceToSectionInput(
                space_id=_SPACE,
                section_name="users/me/sections/clients-1",
                item_name=canonical_item,
            ),
        )

    assert move.call_count == 0, "already in the target section — must not re-POST"
    assert result.moved is False


@pytest.mark.asyncio
async def test_dry_run_preview_distinguishes_skips_from_moves(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """`moved=False` means both "dry run" and "already filed" — the payload disambiguates."""
    already = f"{_CLIENTS}/items/{_ITEM_ID}"
    with _api(mock_access_token, all_called=False) as mock:
        mock.post(f"/{already}:move")
        skipped = await move_space_to_section_handler(
            tool_ctx,
            MoveSpaceToSectionInput(
                space_id=_SPACE, section_name=_CLIENTS, item_name=already, dry_run=True
            ),
        )
        would_move = await move_space_to_section_handler(
            tool_ctx,
            MoveSpaceToSectionInput(
                space_id=_SPACE, section_name=_CLIENTS, item_name=_ITEM, dry_run=True
            ),
        )

    assert skipped.moved is False
    assert would_move.moved is False
    assert skipped.rendered_payload is None, "already filed — nothing would be written"
    assert would_move.rendered_payload == {"targetSection": _CLIENTS}


@pytest.mark.asyncio
async def test_moved_item_name_falls_back_when_the_response_is_unreadable(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """The move already landed, so an unreadable response must not become an error."""
    with _api(mock_access_token) as mock:
        _lookup_route(mock)
        mock.post(f"/{_ITEM}:move").mock(return_value=httpx2.Response(200, json={}))
        result = await move_space_to_section_handler(
            tool_ctx, MoveSpaceToSectionInput(space_id=_SPACE, section_name=_CLIENTS)
        )

    assert result.moved is True
    # Derived, because the id survives the move even though the parent changes.
    assert result.item_name == f"{_CLIENTS}/items/{_ITEM_ID}"


@pytest.mark.asyncio
async def test_one_bad_row_does_not_fail_the_whole_listing(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """The total-outage shape this project has already been burned by twice.

    Validating rows inline in a comprehension meant a single unparseable row
    turned into `ToolError("Internal error.")` for the entire page — which a
    calling model reads as "this section is empty".
    """
    with _api(mock_access_token) as mock:
        mock.get(f"/{_CLIENTS}/items").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "sectionItems": [
                        {"name": f"{_CLIENTS}/items/{_ITEM_ID}", "space": _SPACE},
                        {"space": {"name": "spaces/AAA"}},  # nested union member we don't model
                        {"name": f"{_CLIENTS}/items/other", "space": "spaces/BBB"},
                    ]
                },
            )
        )
        result = await list_section_items_handler(
            tool_ctx, ListSectionItemsInput(section_name=_CLIENTS)
        )

    assert len(result.items) == 2, "good rows must survive a bad neighbour"
    assert result.unparsed == 1, "and the caller must be told the page is incomplete"


@pytest.mark.asyncio
async def test_a_clean_listing_reports_no_unparsed_rows(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with _api(mock_access_token) as mock:
        mock.get("/users/me/sections").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "sections": [{"name": _CLIENTS, "type": "CUSTOM_SECTION", "displayName": "C"}]
                },
            )
        )
        result = await list_sections_handler(tool_ctx, ListSectionsInput())

    assert result.unparsed == 0
