"""Reactions bundle: add_reaction, list_reactions, remove_reaction."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.models import (
    AddReactionInput,
    ListReactionsInput,
    RemoveReactionInput,
)
from src.tools import (
    add_reaction_handler,
    list_reactions_handler,
    remove_reaction_handler,
)
from src.tools._common import ToolContext


def _reaction_obj(rid: str, unicode_emoji: str, user_name: str) -> dict[str, object]:
    return {
        "name": f"spaces/AAA/messages/M.1/reactions/{rid}",
        "user": {"name": user_name},
        "emoji": {"unicode": unicode_emoji},
    }


@pytest.mark.asyncio
async def test_add_reaction_posts_unicode_emoji(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        route = mock.post("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx.Response(200, json=_reaction_obj("r1", "🙂", "users/me"))
        )
        out = await add_reaction_handler(
            tool_ctx, AddReactionInput(message_name="spaces/AAA/messages/M.1", emoji="🙂")
        )
    import json

    body = json.loads(route.calls[0].request.content.decode())
    assert body == {"emoji": {"unicode": "🙂"}}
    assert out.reaction_name == "spaces/AAA/messages/M.1/reactions/r1"
    assert out.emoji == "🙂"


@pytest.mark.asyncio
async def test_list_reactions_paginates(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx.Response(
                200,
                json={
                    "reactions": [
                        _reaction_obj("r1", "🙂", "users/111"),
                        _reaction_obj("r2", "🎉", "users/222"),
                    ],
                    "nextPageToken": "cursor-xyz",
                },
            )
        )
        out = await list_reactions_handler(
            tool_ctx, ListReactionsInput(message_name="spaces/AAA/messages/M.1")
        )
    assert len(out.reactions) == 2
    assert out.reactions[0].emoji == "🙂"
    assert out.reactions[0].reaction_name == "spaces/AAA/messages/M.1/reactions/r1"
    assert out.next_page_token == "cursor-xyz"


@pytest.mark.asyncio
async def test_remove_reaction_by_name_direct_delete(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        route = mock.delete("https://chat.test/v1/spaces/AAA/messages/M.1/reactions/r1").mock(
            return_value=httpx.Response(200, json={})
        )
        out = await remove_reaction_handler(
            tool_ctx,
            RemoveReactionInput(reaction_name="spaces/AAA/messages/M.1/reactions/r1"),
        )
    assert route.call_count == 1
    assert out.removed is True
    assert out.reaction_name == "spaces/AAA/messages/M.1/reactions/r1"


@pytest.mark.asyncio
async def test_remove_reaction_by_filter_list_then_delete(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        # People-API resolve (users/email form); fetch_person behavior.
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice"}],
                },
            )
        )
        # Filtered list returns one hit.
        list_route = mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx.Response(
                200,
                json={"reactions": [_reaction_obj("r42", "🙂", "users/alice@example.com")]},
            )
        )
        del_route = mock.delete("https://chat.test/v1/spaces/AAA/messages/M.1/reactions/r42").mock(
            return_value=httpx.Response(200, json={})
        )
        out = await remove_reaction_handler(
            tool_ctx,
            RemoveReactionInput(
                message_name="spaces/AAA/messages/M.1",
                emoji="🙂",
                user_email="alice@example.com",
            ),
        )
    assert list_route.call_count == 1
    # Query carried the filter string.
    from urllib.parse import parse_qs, urlparse

    qs = parse_qs(urlparse(str(list_route.calls[0].request.url)).query)
    assert "filter" in qs
    assert 'emoji.unicode = "🙂"' in qs["filter"][0]
    assert 'user.name = "users/alice@example.com"' in qs["filter"][0]
    assert del_route.call_count == 1
    assert out.removed is True
    assert out.reaction_name == "spaces/AAA/messages/M.1/reactions/r42"


@pytest.mark.asyncio
async def test_remove_reaction_by_filter_no_match(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get(url__regex=r"https://people\.test/v1/people/.*").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice"}],
                },
            )
        )
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx.Response(200, json={"reactions": []})
        )
        del_route = mock.delete(url__regex=r".*/reactions/.*")
        out = await remove_reaction_handler(
            tool_ctx,
            RemoveReactionInput(
                message_name="spaces/AAA/messages/M.1",
                emoji="🎉",
                user_email="bob@example.com",
            ),
        )
    assert del_route.call_count == 0
    assert out.removed is False
    assert out.reaction_name is None


def test_remove_reaction_input_requires_exactly_one_shape() -> None:
    # Both shapes set — reject.
    with pytest.raises(ValueError, match="reaction_name OR"):
        RemoveReactionInput(
            reaction_name="spaces/AAA/messages/M.1/reactions/r1",
            message_name="spaces/AAA/messages/M.1",
            emoji="🙂",
            user_email="alice@example.com",
        )
    # Neither shape fully populated — reject.
    with pytest.raises(ValueError, match="reaction_name OR"):
        RemoveReactionInput(message_name="spaces/AAA/messages/M.1", emoji="🙂")
