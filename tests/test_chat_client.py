"""ChatClient: retry/backoff behaviour, pagination, error handling."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.chat_client import ChatApiError, ChatClient


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
