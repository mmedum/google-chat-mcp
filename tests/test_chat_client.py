"""ChatClient: retry/backoff behaviour, pagination, error handling."""

from __future__ import annotations

import math

import httpx
import pytest
import respx
from src.chat_client import (
    _MAX_BACKOFF_SECONDS,
    ChatApiError,
    ChatClient,
    _backoff_seconds,
)


@pytest.mark.asyncio
async def test_list_spaces_follows_pagination(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces").mock(
            side_effect=[
                httpx.Response(
                    200, json={"spaces": [{"name": "spaces/a"}], "nextPageToken": "tok"}
                ),
                httpx.Response(200, json={"spaces": [{"name": "spaces/b"}]}),
            ]
        )
        spaces = await chat_client.list_spaces(access_token="tok", limit=50)
    assert [s["name"] for s in spaces] == ["spaces/a", "spaces/b"]


@pytest.mark.asyncio
async def test_retries_on_5xx_then_succeeds(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces").mock(
            side_effect=[
                httpx.Response(503),
                httpx.Response(200, json={"spaces": []}),
            ]
        )
        spaces = await chat_client.list_spaces(access_token="tok", limit=50)
    assert spaces == []


@pytest.mark.asyncio
async def test_retries_on_429_with_retry_after(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces").mock(
            side_effect=[
                httpx.Response(429, headers={"Retry-After": "0"}),
                httpx.Response(200, json={"spaces": []}),
            ]
        )
        await chat_client.list_spaces(access_token="tok", limit=50)


@pytest.mark.parametrize(
    ("header", "expected"),
    [
        # Honoured as-is between the floor and the ceiling.
        ("5", 5.0),
        ("30", 30.0),
        # Clamped. An unclamped `Retry-After: 3600` parked one attempt for an
        # hour — no output, no error, indistinguishable from a hang.
        ("3600", _MAX_BACKOFF_SECONDS),
        ("86400", _MAX_BACKOFF_SECONDS),
    ],
)
def test_retry_after_honoured_up_to_the_ceiling(header: str, expected: float) -> None:
    resp = httpx.Response(429, headers={"Retry-After": header})
    assert _backoff_seconds(1, resp) == expected


@pytest.mark.parametrize("header", ["0", "-5", "0.001"])
def test_retry_after_below_the_floor_is_raised_to_it(header: str) -> None:
    """`Retry-After: 0` means "retry now", which is a burst, not backpressure.

    Without a floor these spend every attempt in microseconds against an
    upstream that just asked us to slow down. The floor carries jitter, so this
    asserts the band rather than an exact value: a bare floor would wake every
    concurrent retry at the identical instant, which is the herd the jitter on
    the computed path exists to break up.
    """
    delay = _backoff_seconds(1, httpx.Response(429, headers={"Retry-After": header}))
    base = 0.5
    assert base <= delay <= base * 1.25


_NO_HEADER = "<<absent>>"


@pytest.mark.parametrize(
    "header",
    [
        "not-a-number",
        "Wed, 21 Oct 2026 07:28:00 GMT",  # the header also permits an HTTP-date
        "",  # present, but empty
        "nan",  # parses as a float, and NaN would reach asyncio.sleep
        "inf",
        _NO_HEADER,
    ],
)
def test_unusable_retry_after_falls_back_to_exponential(header: str) -> None:
    """Never a crash, never a non-finite sleep.

    `float()` alone is not a sufficient filter: it accepts `"nan"` and
    `"inf"`, and NaN survives both `min` and `max` to reach `asyncio.sleep`,
    which rejects it outright on 3.13+ and poisons the timer heap before that.
    """
    headers = {} if header == _NO_HEADER else {"Retry-After": header}
    delay = _backoff_seconds(1, httpx.Response(429, headers=headers))
    assert math.isfinite(delay)
    assert 0 < delay <= _MAX_BACKOFF_SECONDS


def test_exponential_backoff_respects_ceiling() -> None:
    for attempt in range(1, 12):
        assert 0 < _backoff_seconds(attempt, httpx.Response(503)) <= _MAX_BACKOFF_SECONDS


@pytest.mark.asyncio
async def test_gives_up_after_max_retries(chat_client: ChatClient) -> None:
    # Force the retry cap to 1 so the test is fast.
    chat_client._max_retries = 1
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces").mock(return_value=httpx.Response(503))
        with pytest.raises(ChatApiError) as exc:
            await chat_client.list_spaces(access_token="tok", limit=50)
    assert exc.value.status_code == 503


@pytest.mark.asyncio
async def test_find_direct_message_404_returns_none(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces:findDirectMessage").mock(return_value=httpx.Response(404))
        result = await chat_client.find_direct_message(
            access_token="tok", user_email="alice@example.com"
        )
    assert result is None


@pytest.mark.asyncio
async def test_send_message_includes_thread_reply_option(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        route = mock.post("/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/AAA/messages/M.1",
                    "sender": {"name": "users/111"},
                    "createTime": "2026-04-19T10:00:00Z",
                    "text": "hello",
                    "thread": {"name": "spaces/AAA/threads/T.1"},
                },
            )
        )
        await chat_client.send_message(
            access_token="tok",
            space_id="spaces/AAA",
            text="hello",
            thread_name="spaces/AAA/threads/T.1",
        )
    req = route.calls[0].request
    assert req.url.params.get("messageReplyOption") == "REPLY_MESSAGE_OR_FAIL"


@pytest.mark.asyncio
async def test_get_space_hits_spaces_resource(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        route = mock.get("/spaces/AAA").mock(
            return_value=httpx.Response(
                200, json={"name": "spaces/AAA", "type": "SPACE", "displayName": "#eng"}
            )
        )
        out = await chat_client.get_space(access_token="tok", space_id="spaces/AAA")
    assert out["displayName"] == "#eng"
    assert len(route.calls) == 1


@pytest.mark.asyncio
async def test_list_members_respects_limit_across_pages(
    chat_client: ChatClient,
) -> None:
    # Two-page response; limit=2 stops after the first page even though a
    # second is offered, and pageSize on the second call would shrink to
    # remaining budget.
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces/AAA/members").mock(
            side_effect=[
                httpx.Response(
                    200,
                    json={
                        "memberships": [
                            {
                                "name": "spaces/AAA/members/1",
                                "state": "JOINED",
                                "member": {"name": "users/111"},
                            }
                        ],
                        "nextPageToken": "p2",
                    },
                ),
                httpx.Response(
                    200,
                    json={
                        "memberships": [
                            {
                                "name": "spaces/AAA/members/2",
                                "state": "JOINED",
                                "member": {"name": "users/222"},
                            }
                        ]
                    },
                ),
            ]
        )
        out = await chat_client.list_members(access_token="tok", space_id="spaces/AAA", limit=2)
    assert [m["name"] for m in out] == [
        "spaces/AAA/members/1",
        "spaces/AAA/members/2",
    ]


@pytest.mark.asyncio
async def test_non_retryable_4xx_raises_immediately(chat_client: ChatClient) -> None:
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces").mock(
            return_value=httpx.Response(403, json={"error": {"message": "forbidden"}})
        )
        with pytest.raises(ChatApiError) as exc:
            await chat_client.list_spaces(access_token="tok", limit=50)
    assert exc.value.status_code == 403
    assert "forbidden" in str(exc.value)


@pytest.mark.asyncio
async def test_3xx_response_raises_chat_api_error(chat_client: ChatClient) -> None:
    """Regression: pre-fix `< 400` swallowed 3xx as success and returned an
    empty dict, masking the redirect. Now any 3xx surfaces as a
    ChatApiError so the caller can decide what to do."""
    with respx.mock(base_url="https://chat.test/v1") as mock:
        mock.get("/spaces").mock(
            return_value=httpx.Response(
                302,
                headers={"Location": "https://attacker.example.com/v1/spaces"},
            )
        )
        with pytest.raises(ChatApiError) as exc:
            await chat_client.list_spaces(access_token="tok", limit=50)
    assert exc.value.status_code == 302
