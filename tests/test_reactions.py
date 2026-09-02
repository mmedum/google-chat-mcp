"""Reactions bundle: add_reaction, list_reactions, remove_reaction."""

from __future__ import annotations

import httpx2
import pytest
from fastmcp.exceptions import ToolError
from src.models import (
    AddReactionInput,
    ListReactionsInput,
    RemoveReactionInput,
)
from src.storage import Database
from src.tools import (
    add_reaction_handler,
    list_reactions_handler,
    remove_reaction_handler,
)
from src.tools._common import (
    CHAT_MESSAGES,
    CHAT_MESSAGES_REACTIONS,
    CHAT_MESSAGES_READONLY,
    AuthInfo,
    ToolContext,
)

from tests.conftest import person_payload

from ._httpx2_mock import mock_api


def _reaction_obj(rid: str, unicode_emoji: str, user_name: str) -> dict[str, object]:
    return {
        "name": f"spaces/AAA/messages/M.1/reactions/{rid}",
        "user": {"name": user_name},
        "emoji": {"unicode": unicode_emoji},
    }


@pytest.mark.asyncio
async def test_add_reaction_posts_unicode_emoji(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        route = mock.post("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(200, json=_reaction_obj("r1", "🙂", "users/me"))
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
async def test_add_reaction_409_presents_as_idempotent_success(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Chat API returns 409 on duplicate (emoji, user, message) instead of
    silently no-op'ing; the handler recovers by looking up the existing
    reaction via reactions.list and returning success with that reaction_name.
    """
    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        create_route = mock.post("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                409,
                json={
                    "error": {
                        "code": 409,
                        "message": "The caller has already created this reaction on the specified message.",
                        "status": "ALREADY_EXISTS",
                    }
                },
            )
        )
        list_route = mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200,
                json={"reactions": [_reaction_obj("r_existing", "🙂", "users/test-user-sub")]},
            )
        )
        out = await add_reaction_handler(
            tool_ctx, AddReactionInput(message_name="spaces/AAA/messages/M.1", emoji="🙂")
        )
    assert create_route.call_count == 1
    assert list_route.call_count == 1
    # Filter carries emoji + self-sub predicates.
    from urllib.parse import parse_qs, urlparse

    qs = parse_qs(urlparse(str(list_route.calls[0].request.url)).query)
    assert 'emoji.unicode = "🙂"' in qs["filter"][0]
    assert 'user.name = "users/test-user-sub"' in qs["filter"][0]
    assert out.reaction_name == "spaces/AAA/messages/M.1/reactions/r_existing"
    assert out.emoji == "🙂"


@pytest.mark.asyncio
async def test_add_reaction_409_with_empty_list_re_raises(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Edge case: reaction was deleted between the 409-create and the
    fallback list. Nothing to return — propagate the original error so the
    caller can retry (a fresh add will now succeed).
    """
    from fastmcp.exceptions import ToolError

    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        mock.post("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                409,
                json={
                    "error": {
                        "code": 409,
                        "message": "The caller has already created this reaction on the specified message.",
                        "status": "ALREADY_EXISTS",
                    }
                },
            )
        )
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(200, json={"reactions": []})
        )
        with pytest.raises(ToolError, match="409"):
            await add_reaction_handler(
                tool_ctx, AddReactionInput(message_name="spaces/AAA/messages/M.1", emoji="🙂")
            )


@pytest.mark.asyncio
async def test_list_reactions_paginates(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
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


@pytest.mark.parametrize(
    "granted",
    [CHAT_MESSAGES_REACTIONS, CHAT_MESSAGES_READONLY, CHAT_MESSAGES],
    ids=["reactions", "messages-readonly", "messages-umbrella"],
)
@pytest.mark.asyncio
async def test_list_reactions_accepts_any_scope_google_accepts(
    tool_ctx: ToolContext, granted: str
) -> None:
    """Google takes any of these for reactions.list, so the pre-flight must too.

    The umbrella case is the one that matters most: it is restricted-tier and
    a user who granted it has already paid the expensive consent, so denying
    them and demanding a second grant is the exact false denial this check
    exists to avoid.
    """

    async def resolver() -> AuthInfo:
        return AuthInfo(
            access_token="upstream-access-token",
            user_sub="test-user-sub",
            granted_scopes=(granted,),
        )

    tool_ctx.resolver = resolver
    with mock_api() as mock:
        route = mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(200, json={"reactions": []})
        )
        out = await list_reactions_handler(
            tool_ctx, ListReactionsInput(message_name="spaces/AAA/messages/M.1")
        )

    assert out.reactions == []
    assert route.call_count == 1


@pytest.mark.asyncio
async def test_list_reactions_denies_when_no_accepted_scope_granted(
    tool_ctx: ToolContext, db: Database
) -> None:
    """The deny case — without it, deleting the gate leaves the suite green.

    Asserts all three things the denial owes: no upstream request, a prompt
    naming the sensitive-tier scope rather than either restricted-tier
    alternative, and an audit row so the denial is accountable afterwards.
    """

    async def resolver() -> AuthInfo:
        return AuthInfo(
            access_token="upstream-access-token",
            user_sub="test-user-sub",
            granted_scopes=("openid",),
        )

    tool_ctx.resolver = resolver
    # assert_all_called=False: the route existing but never being hit is the
    # assertion, not an oversight.
    with mock_api(assert_all_called=False) as mock:
        route = mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(200, json={"reactions": []})
        )
        with pytest.raises(ToolError) as excinfo:
            await list_reactions_handler(
                tool_ctx, ListReactionsInput(message_name="spaces/AAA/messages/M.1")
            )

    assert route.call_count == 0
    assert CHAT_MESSAGES_REACTIONS in str(excinfo.value)
    assert CHAT_MESSAGES_READONLY not in str(excinfo.value)

    async with db.cursor() as conn:
        cur = await conn.execute(
            "SELECT tool_name, success, error_code FROM audit_log ORDER BY id DESC LIMIT 1"
        )
        row = await cur.fetchone()
    assert row is not None
    assert row["tool_name"] == "list_reactions"
    assert not row["success"]
    assert row["error_code"] == "missing_scope"


@pytest.mark.asyncio
async def test_remove_reaction_by_name_direct_delete(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        route = mock.delete("https://chat.test/v1/spaces/AAA/messages/M.1/reactions/r1").mock(
            return_value=httpx2.Response(200, json={})
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
    """Filter path: server-side filter on emoji only, then People-API resolve per reaction."""
    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        list_route = mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "reactions": [
                        _reaction_obj("r10", "🙂", "users/111"),
                        _reaction_obj("r42", "🙂", "users/222"),
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx2.Response(200, json=person_payload("bob@example.com", "Bob"))
        )
        mock.get("https://people.test/v1/people/222").mock(
            return_value=httpx2.Response(200, json=person_payload("alice@example.com", "Alice"))
        )
        del_route = mock.delete("https://chat.test/v1/spaces/AAA/messages/M.1/reactions/r42").mock(
            return_value=httpx2.Response(200, json={})
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
    from urllib.parse import parse_qs, urlparse

    qs = parse_qs(urlparse(str(list_route.calls[0].request.url)).query)
    assert "filter" in qs
    # Emoji-only filter; no `user.name` predicate (Chat API 500s on users/{email}).
    assert 'emoji.unicode = "🙂"' in qs["filter"][0]
    assert "user.name" not in qs["filter"][0]
    assert del_route.call_count == 1
    assert out.removed is True
    assert out.reaction_name == "spaces/AAA/messages/M.1/reactions/r42"


@pytest.mark.asyncio
async def test_remove_reaction_by_filter_email_case_insensitive(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Mismatched case on user_email still matches — Google emails are case-insensitive."""
    with (
        mock_api() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200,
                json={"reactions": [_reaction_obj("r1", "🙂", "users/111")]},
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx2.Response(200, json=person_payload("Alice@Example.com"))
        )
        del_route = mock.delete("https://chat.test/v1/spaces/AAA/messages/M.1/reactions/r1").mock(
            return_value=httpx2.Response(200, json={})
        )
        out = await remove_reaction_handler(
            tool_ctx,
            RemoveReactionInput(
                message_name="spaces/AAA/messages/M.1",
                emoji="🙂",
                user_email="alice@example.com",
            ),
        )
    assert del_route.call_count == 1
    assert out.removed is True


@pytest.mark.asyncio
async def test_remove_reaction_by_filter_no_match_empty_list(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Empty reaction list → removed=False, no DELETE."""
    with (
        mock_api(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(200, json={"reactions": []})
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


@pytest.mark.asyncio
async def test_remove_reaction_by_filter_no_email_match(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Emoji matched, but no resolved email matches the target → removed=False."""
    with (
        mock_api(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200,
                json={"reactions": [_reaction_obj("r1", "🙂", "users/999")]},
            )
        )
        mock.get("https://people.test/v1/people/999").mock(
            return_value=httpx2.Response(200, json=person_payload("someone.else@example.com"))
        )
        del_route = mock.delete(url__regex=r".*/reactions/.*")
        out = await remove_reaction_handler(
            tool_ctx,
            RemoveReactionInput(
                message_name="spaces/AAA/messages/M.1",
                emoji="🙂",
                user_email="alice@example.com",
            ),
        )
    assert del_route.call_count == 0
    assert out.removed is False
    assert out.reaction_name is None


@pytest.mark.parametrize("payload", ['x"y', "x\\y", "x y", 'x" OR "y'])
def test_emoji_rejects_filter_injection_chars(payload: str) -> None:
    """Regression for AIP-160 filter injection: `"`, `\\`, and whitespace
    in `emoji` could break out of the quoted filter string in
    `chat_client.list_reactions` (`emoji.unicode = "{value}"`) and broaden
    the match. The Pydantic pattern rejects them at the model boundary."""
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        AddReactionInput(message_name="spaces/AAA/messages/M.1", emoji=payload)
    with pytest.raises(ValidationError):
        RemoveReactionInput(
            message_name="spaces/AAA/messages/M.1",
            emoji=payload,
            user_email="a@example.com",
        )


def test_emoji_still_accepts_unicode_glyphs() -> None:
    """Sanity: legitimate unicode emoji + ZWJ sequences must still validate."""
    AddReactionInput(message_name="spaces/AAA/messages/M.1", emoji="🙂")
    AddReactionInput(message_name="spaces/AAA/messages/M.1", emoji="👨‍👩‍👧")


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


@pytest.mark.asyncio
async def test_remove_reaction_refuses_to_claim_already_gone_when_a_lookup_fails(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """`removed: false` means "already gone" — it must not also mean "we couldn't tell".

    The filter shape matches reactors by email. Once a People API failure
    degrades to a null email instead of raising, an unresolvable reactor is
    silently skipped, and the tool used to fall through to
    `removed: false` — telling a calling model the reaction was already
    removed while it is still there.
    """
    tool_ctx.client._max_retries = 0
    # `assert_all_called=False`: the DELETE route going uncalled is the result
    # under test, not a mis-specified mock.
    with mock_api(assert_all_called=False) as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200, json={"reactions": [_reaction_obj("r10", "🙂", "users/111")]}
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx2.Response(
                403,
                json={
                    "error": {
                        "code": 403,
                        "status": "PERMISSION_DENIED",
                        "message": "Request had insufficient authentication scopes.",
                    }
                },
            )
        )
        deleted = mock.delete(url__startswith="https://chat.test/v1/spaces/AAA/messages/M.1")
        with pytest.raises(ToolError) as exc:
            await remove_reaction_handler(
                tool_ctx,
                RemoveReactionInput(
                    message_name="spaces/AAA/messages/M.1",
                    emoji="🙂",
                    user_email="alice@example.com",
                ),
            )

    assert "could not verify" in str(exc.value).lower()
    assert "list_reactions" in str(exc.value)
    assert not deleted.called, "nothing may be deleted on an unverified match"


@pytest.mark.asyncio
async def test_remove_reaction_still_reports_already_gone_on_a_clean_miss(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """When every reactor resolves and none match, `removed: false` is the truth."""
    with mock_api() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200, json={"reactions": [_reaction_obj("r10", "🙂", "users/111")]}
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx2.Response(200, json=person_payload("bob@example.com", "Bob"))
        )
        out = await remove_reaction_handler(
            tool_ctx,
            RemoveReactionInput(
                message_name="spaces/AAA/messages/M.1",
                emoji="🙂",
                user_email="alice@example.com",
            ),
        )

    assert out.removed is False
    assert out.reaction_name is None
