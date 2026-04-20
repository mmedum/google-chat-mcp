"""search_messages — exact + regex filters, pagination cap, validation."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.models import SearchMessagesInput
from src.tools import search_messages_handler
from src.tools._common import ToolContext


def _msg(m_id: str, text: str, ts: str = "2026-04-19T10:00:00Z") -> dict[str, object]:
    return {
        "name": f"spaces/AAA/messages/{m_id}",
        "sender": {"name": "users/111", "displayName": "Alice"},
        "createTime": ts,
        "text": text,
        "thread": {"name": "spaces/AAA/threads/T.1"},
    }


@pytest.mark.asyncio
async def test_exact_substring_match_case_insensitive(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [
                        _msg("M.1", "hello world"),
                        _msg("M.2", "HELLO there"),
                        _msg("M.3", "goodbye"),
                    ]
                },
            )
        )
        out = await search_messages_handler(
            tool_ctx,
            SearchMessagesInput(space_id="spaces/AAA", query="hello"),
        )
    assert [m.message_id for m in out.matches] == [
        "spaces/AAA/messages/M.1",
        "spaces/AAA/messages/M.2",
    ]
    assert out.scanned == 3
    assert out.cap_reached is False


@pytest.mark.asyncio
async def test_regex_match(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [
                        _msg("M.1", "error: connection refused"),
                        _msg("M.2", "ok"),
                        _msg("M.3", "warn: slow query"),
                    ]
                },
            )
        )
        out = await search_messages_handler(
            tool_ctx,
            SearchMessagesInput(space_id="spaces/AAA", regex=r"^(error|warn):"),
        )
    assert {m.message_id for m in out.matches} == {
        "spaces/AAA/messages/M.1",
        "spaces/AAA/messages/M.3",
    }


@pytest.mark.asyncio
async def test_no_match_empty_result(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json={"messages": [_msg("M.1", "unrelated")]})
        )
        out = await search_messages_handler(
            tool_ctx,
            SearchMessagesInput(space_id="spaces/AAA", query="needle"),
        )
    assert out.matches == []
    assert out.scanned == 1
    assert out.cap_reached is False


@pytest.mark.asyncio
async def test_cap_reached_signals_partial_result(
    tool_ctx: ToolContext, mock_access_token, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Stop at max_pages even when nextPageToken is still present."""
    monkeypatch.setenv("GCM_SEARCH_MAX_PAGES", "1")
    with (
        respx.mock(assert_all_called=False) as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [_msg("M.1", "hello")],
                    "nextPageToken": "still-more",
                },
            )
        )
        out = await search_messages_handler(
            tool_ctx,
            SearchMessagesInput(space_id="spaces/AAA", query="hello"),
        )
    assert len(out.matches) == 1
    assert out.cap_reached is True


def test_missing_query_and_regex_rejected() -> None:
    with pytest.raises(ValueError, match="exactly one of"):
        SearchMessagesInput(space_id="spaces/AAA")


def test_both_query_and_regex_rejected() -> None:
    with pytest.raises(ValueError, match="exactly one of"):
        SearchMessagesInput(space_id="spaces/AAA", query="x", regex=r"x")


def test_empty_query_rejected() -> None:
    with pytest.raises(ValueError, match="non-empty"):
        SearchMessagesInput(space_id="spaces/AAA", query="   ")
