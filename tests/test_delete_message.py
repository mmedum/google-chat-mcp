"""delete_message: idempotent delete via spaces.messages.delete."""

from __future__ import annotations

import httpx
import pytest
import respx
from fastmcp.exceptions import ToolError
from pydantic import ValidationError
from src.models import DeleteMessageInput
from src.tools import delete_message_handler
from src.tools._common import ToolContext

_MESSAGE_NAME = "spaces/AAA/messages/M.1"


@pytest.mark.asyncio
async def test_happy_path_returns_deleted_true(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.delete(f"/{_MESSAGE_NAME}").mock(return_value=httpx.Response(200, json={}))
        result = await delete_message_handler(
            tool_ctx, DeleteMessageInput(message_name=_MESSAGE_NAME)
        )
    assert route.call_count == 1
    assert result.deleted is True
    assert result.message_name == _MESSAGE_NAME
    assert result.dry_run is False


@pytest.mark.asyncio
async def test_dry_run_does_not_delete(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.delete(f"/{_MESSAGE_NAME}")
        result = await delete_message_handler(
            tool_ctx, DeleteMessageInput(message_name=_MESSAGE_NAME, dry_run=True)
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.deleted is False


@pytest.mark.asyncio
async def test_double_delete_returns_deleted_false_on_404(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.delete(f"/{_MESSAGE_NAME}").mock(
            return_value=httpx.Response(
                404,
                json={"error": {"code": 404, "message": "gone", "status": "NOT_FOUND"}},
            )
        )
        result = await delete_message_handler(
            tool_ctx, DeleteMessageInput(message_name=_MESSAGE_NAME)
        )
    assert result.deleted is False


@pytest.mark.asyncio
async def test_permission_denied_returns_deleted_false(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Non-scope 403 → idempotent deleted=false (mirrors remove_member)."""
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.delete(f"/{_MESSAGE_NAME}").mock(
            return_value=httpx.Response(
                403,
                json={
                    "error": {
                        "code": 403,
                        "message": "caller can't delete this",
                        "status": "PERMISSION_DENIED",
                    }
                },
            )
        )
        result = await delete_message_handler(
            tool_ctx, DeleteMessageInput(message_name=_MESSAGE_NAME)
        )
    assert result.deleted is False


@pytest.mark.asyncio
async def test_missing_scope_403_still_raises(tool_ctx: ToolContext, mock_access_token) -> None:
    """Missing-scope 403 must NOT be swallowed as idempotent success."""
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.delete(f"/{_MESSAGE_NAME}").mock(
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
        with pytest.raises(ToolError, match="scope"):
            await delete_message_handler(tool_ctx, DeleteMessageInput(message_name=_MESSAGE_NAME))


def test_invalid_message_name_rejected_at_model_boundary() -> None:
    with pytest.raises(ValidationError):
        DeleteMessageInput(message_name="not-a-message-name")
