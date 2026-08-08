"""People API failures must degrade the email field, never drop the row.

Regression guard for the failure shape that has bitten this project twice: a
partial failure that renders as *emptiness*. `search_messages` reporting "0
matches" during the `markupSyntax` outage was the first; `get_messages` and
`list_members` returning `[]` whenever the People API refused a lookup was the
second. In both cases the caller is an LLM, and an empty list is a confident,
plausible, wrong answer — "there are no messages in this space" — where an
error would have been actionable.

The Chat payload is already in hand by the time enrichment runs. Losing an
email must cost the email and nothing else.
"""

from __future__ import annotations

from unittest.mock import patch

import httpx
import pytest
import respx
from src.models import GetMessagesInput, GetThreadInput, ListMembersInput
from src.observability import REGISTRY
from src.tools._common import ToolContext
from src.tools.get_messages import get_messages_handler
from src.tools.get_thread import get_thread_handler
from src.tools.list_members import list_members_handler

from .conftest import person_payload

# The 403 Google returns when `directory.readonly` was never granted. Not a
# hypothetical: the scope is separately consentable, so any deployer who
# declined it hits this on every read.
_SCOPE_403 = httpx.Response(
    403,
    json={
        "error": {
            "code": 403,
            "status": "PERMISSION_DENIED",
            "message": "Request had insufficient authentication scopes.",
            "details": [{"reason": "ACCESS_TOKEN_SCOPE_INSUFFICIENT"}],
        }
    },
)

_MESSAGES = {
    "messages": [
        {
            "name": "spaces/AAA/messages/M.1",
            "sender": {"name": "users/111", "displayName": "Alice"},
            "createTime": "2026-04-19T10:00:00Z",
            "text": "first",
            "thread": {"name": "spaces/AAA/threads/T.1"},
        },
        {
            "name": "spaces/AAA/messages/M.2",
            "sender": {"name": "users/222", "displayName": "Bob"},
            "createTime": "2026-04-19T10:01:00Z",
            "text": "second",
            "thread": {"name": "spaces/AAA/threads/T.1"},
        },
    ]
}

_MEMBERSHIPS = {
    "memberships": [
        {
            "name": "spaces/AAA/members/1",
            "state": "JOINED",
            "role": "ROLE_MEMBER",
            "member": {"name": "users/111", "type": "HUMAN", "displayName": "Alice"},
        },
        {
            "name": "spaces/AAA/members/2",
            "state": "JOINED",
            "role": "ROLE_MANAGER",
            "member": {"name": "users/222", "type": "HUMAN", "displayName": "Bob"},
        },
    ]
}


@pytest.fixture(autouse=True)
def _no_retry_ladder(tool_ctx: ToolContext) -> None:
    """Skip the retry/backoff sleeps — they are not what these tests assert.

    `ChatClient` retries 429/5xx with exponential backoff, so every failure
    case here would otherwise pay ~3.5s of real sleeping to reach the same
    degraded outcome. Retry behaviour has its own coverage in
    `test_chat_client.py`; what matters here is what happens once the lookup
    has definitively failed.
    """
    tool_ctx.client._max_retries = 0


def _degraded_count(tool: str) -> float:
    """Read the counter through the public registry API, not `._value`."""
    return REGISTRY.get_sample_value("mcp_people_lookup_failures_total", {"tool": tool}) or 0.0


def _mock_people(mock: respx.MockRouter, failure: httpx.Response | Exception) -> None:
    """Point every People-API lookup at one failure, response or transport-level."""
    route = mock.get(url__startswith="https://people.test/v1/people/")
    if isinstance(failure, httpx.Response):
        route.mock(return_value=failure)
    else:
        route.mock(side_effect=failure)


# Every way the People API can refuse. The transport entries matter as much as
# the status ones: `ChatClient._request` does not wrap transport errors, so a
# timeout arrives as a raw httpx exception rather than a `ChatApiError` — an
# `except ChatApiError` in the enrichment path would let it through and drop
# the row again, and a status-only test suite would never notice.
_PEOPLE_FAILURES = [
    pytest.param(_SCOPE_403, id="missing_directory_scope_403"),
    pytest.param(httpx.Response(429), id="rate_limited_429"),
    pytest.param(httpx.Response(500), id="upstream_500"),
    pytest.param(httpx.ConnectTimeout("timed out"), id="connect_timeout"),
    pytest.param(httpx.ReadTimeout("timed out"), id="read_timeout"),
    pytest.param(httpx.ConnectError("refused"), id="connect_error"),
]

# `get_thread` filters `spaces.messages.list`, the same endpoint `get_messages`
# pages — so both drive off one fixture and differ only in handler + input.
_MESSAGE_READERS = [
    pytest.param(
        get_messages_handler,
        GetMessagesInput(space_id="spaces/AAA", limit=20),
        id="get_messages",
    ),
    pytest.param(
        get_thread_handler,
        GetThreadInput(space_id="spaces/AAA", thread_name="spaces/AAA/threads/T.1"),
        id="get_thread",
    ),
]


@pytest.mark.parametrize("people_failure", _PEOPLE_FAILURES)
@pytest.mark.parametrize(("handler", "payload"), _MESSAGE_READERS)
@pytest.mark.asyncio
async def test_message_readers_keep_rows_when_people_api_fails(
    tool_ctx: ToolContext, mock_access_token, handler, payload, people_failure
) -> None:
    with respx.mock() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        _mock_people(mock, people_failure)
        out = await handler(tool_ctx, payload)

    # The whole point: two messages in, two messages out.
    assert [m.text for m in out] == ["first", "second"]
    assert all(m.sender_email is None for m in out)
    # Identity still comes through from the Chat payload — the caller is not
    # left guessing who spoke, only which address they use.
    assert [m.sender_display_name for m in out] == ["Alice", "Bob"]
    assert [m.sender_user_id for m in out] == ["users/111", "users/222"]


@pytest.mark.parametrize("people_failure", _PEOPLE_FAILURES)
@pytest.mark.asyncio
async def test_list_members_keeps_rows_when_people_api_fails(
    tool_ctx: ToolContext, mock_access_token, people_failure
) -> None:
    with respx.mock() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/members").mock(
            return_value=httpx.Response(200, json=_MEMBERSHIPS)
        )
        _mock_people(mock, people_failure)
        out = await list_members_handler(
            tool_ctx, ListMembersInput(space_id="spaces/AAA", limit=50)
        )

    assert len(out) == 2, "a space with two members must never report as empty"
    assert [m.member_id for m in out] == ["users/111", "users/222"]
    assert [m.display_name for m in out] == ["Alice", "Bob"]
    assert all(m.email is None for m in out)
    # Role/state come from the Chat payload and must survive the degradation.
    assert [m.role for m in out] == ["ROLE_MEMBER", "ROLE_MANAGER"]


@pytest.mark.asyncio
async def test_degraded_lookup_increments_counter(tool_ctx: ToolContext, mock_access_token) -> None:
    """The degradation is silent to the caller, so it must not be silent to the operator."""
    before = _degraded_count("get_messages")
    with respx.mock() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        _mock_people(mock, _SCOPE_403)
        await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))
    after = _degraded_count("get_messages")

    assert after - before == 2, "one increment per failed lookup"


@pytest.mark.asyncio
async def test_successful_lookup_still_resolves_email(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Degradation must not have become the happy path."""
    with respx.mock() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx.Response(
                200, json=person_payload("alice@example.com", "Alice Smith")
            )
        )
        mock.get("https://people.test/v1/people/222").mock(return_value=_SCOPE_403)
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))

    assert out[0].sender_email == "alice@example.com"
    assert out[0].sender_display_name == "Alice Smith"
    # The failing lookup degrades only its own row.
    assert out[1].sender_email is None
    assert out[1].sender_display_name == "Bob"


@pytest.mark.asyncio
async def test_cached_sender_skips_the_people_call(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """The cache-hit path is the whole point of consolidating the resolver.

    Nothing else covered it, so a regression that broke cache reuse — a flipped
    tuple order, a key that no longer matches — would have shipped green while
    quietly restoring the per-row round-trips.
    """
    await tool_ctx.directory_cache.put("users/111", "alice@example.com", "Alice Smith")
    await tool_ctx.directory_cache.put("users/222", "bob@example.com", "Bob Smith")

    # `assert_all_called=False`: the People route going uncalled is the result
    # under test, not a mis-specified mock.
    with respx.mock(assert_all_called=False) as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        people = mock.get(url__startswith="https://people.test/v1/people/").mock(
            return_value=_SCOPE_403
        )
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))

    assert not people.called, "a cached sender must not hit the People API"
    assert [m.sender_email for m in out] == ["alice@example.com", "bob@example.com"]
    assert [m.sender_display_name for m in out] == ["Alice Smith", "Bob Smith"]


@pytest.mark.asyncio
async def test_one_lookup_per_unique_sender_not_per_message(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Two messages from one sender must cost one lookup, not two."""
    same_sender = {
        "messages": [
            dict(_MESSAGES["messages"][0], name=f"spaces/AAA/messages/M.{i}") for i in range(6)
        ]
    }
    with respx.mock() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=same_sender)
        )
        people = mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx.Response(200, json=person_payload("alice@example.com", "Alice"))
        )
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))

    assert len(out) == 6
    assert len(people.calls) == 1, "six messages from one sender should resolve once"


@pytest.mark.asyncio
async def test_directory_cache_failure_does_not_drop_rows(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """The cache is SQLite and has its own failure modes.

    A locked or full database must not cost the caller their messages — and
    must not be reported as Chat API schema drift, which is the alert operators
    are told to trust.
    """
    with (
        respx.mock() as mock,
        mock_access_token(),
        # `get_many` / `put_many_users` are what production calls — patching
        # the per-row `get`/`put` left the real guards with zero coverage.
        patch.object(
            tool_ctx.directory_cache, "get_many", side_effect=RuntimeError("database is locked")
        ),
        patch.object(
            tool_ctx.directory_cache,
            "put_many_users",
            side_effect=RuntimeError("database is locked"),
        ),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        mock.get(url__startswith="https://people.test/v1/people/").mock(
            return_value=httpx.Response(200, json=person_payload("alice@example.com", "Alice"))
        )
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))

    assert [m.text for m in out] == ["first", "second"]
    # The lookup still succeeded; only the memoisation was lost.
    assert all(m.sender_email == "alice@example.com" for m in out)


@pytest.mark.parametrize(
    "odd_address",
    [
        pytest.param("not an email", id="not_an_address_at_all"),
        pytest.param("i18n@exämple.test", id="internationalised_domain"),
        pytest.param("alice@localhost", id="no_tld"),
        pytest.param(" alice@example.com ", id="surrounding_whitespace"),
    ],
)
@pytest.mark.asyncio
async def test_addresses_google_returns_pass_through_and_keep_the_row(
    tool_ctx: ToolContext, mock_access_token, odd_address: str
) -> None:
    """People API is a separate upstream with its own notion of an address.

    Typing the *output* field as `EmailStr` gave a value pydantic happens to
    reject the power to delete a message that had been fetched successfully.
    These fields are `str`, so whatever Google says reaches the caller and the
    row survives — no sanitising step to forget on some path.
    """
    with respx.mock(assert_all_called=False) as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        mock.get(url__startswith="https://people.test/v1/people/").mock(
            return_value=httpx.Response(200, json=person_payload(odd_address, "Alice"))
        )
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))

    assert [m.text for m in out] == ["first", "second"]
    assert out[0].sender_email == odd_address


@pytest.mark.asyncio
async def test_cached_odd_address_keeps_the_row(tool_ctx: ToolContext, mock_access_token) -> None:
    """Rows already in `user_directory` reach the caller by a different path."""
    await tool_ctx.directory_cache.put("users/111", "not an email", "Alice Cached")
    await tool_ctx.directory_cache.put("users/222", "bob@example.com", "Bob Cached")

    with respx.mock(assert_all_called=False) as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json=_MESSAGES)
        )
        _mock_people(mock, _SCOPE_403)
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))

    assert [m.text for m in out] == ["first", "second"]
    assert out[0].sender_email == "not an email"
    assert out[1].sender_email == "bob@example.com"


@pytest.mark.asyncio
async def test_cached_members_cost_no_people_requests(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """A warm cache must satisfy a whole page without touching the People API.

    Reading the cache per row meant every member missed before any of them
    could write back, so a warm cache saved nothing on the first call and cost
    one SQLite connection — and one OS thread — per member besides.
    """
    await tool_ctx.directory_cache.put("users/111", "alice@example.com", "Alice")
    await tool_ctx.directory_cache.put("users/222", "bob@example.com", "Bob")

    with respx.mock(assert_all_called=False) as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/members").mock(
            return_value=httpx.Response(200, json=_MEMBERSHIPS)
        )
        people = mock.get(url__startswith="https://people.test/v1/people/").mock(
            return_value=_SCOPE_403
        )
        out = await list_members_handler(
            tool_ctx, ListMembersInput(space_id="spaces/AAA", limit=50)
        )

    assert not people.called
    assert [m.email for m in out] == ["alice@example.com", "bob@example.com"]


@pytest.mark.asyncio
async def test_only_uncached_members_are_looked_up(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """A partial cache costs exactly one request per genuine miss."""
    await tool_ctx.directory_cache.put("users/111", "alice@example.com", "Alice")

    with respx.mock() as mock, mock_access_token():
        mock.get("https://chat.test/v1/spaces/AAA/members").mock(
            return_value=httpx.Response(200, json=_MEMBERSHIPS)
        )
        people = mock.get("https://people.test/v1/people/222").mock(
            return_value=httpx.Response(200, json=person_payload("bob@example.com", "Bob"))
        )
        out = await list_members_handler(
            tool_ctx, ListMembersInput(space_id="spaces/AAA", limit=50)
        )

    assert len(people.calls) == 1
    assert [m.email for m in out] == ["alice@example.com", "bob@example.com"]
