"""update_message: text-only edit via spaces.messages.patch."""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from pydantic import ValidationError
from src.chat_client import _build_update_message_body
from src.models import UpdateMessageInput
from src.tools import update_message_handler
from src.tools._common import ToolContext

_MESSAGE_NAME = "spaces/AAA/messages/M.1"


@pytest.mark.asyncio
async def test_happy_path_returns_updated_text(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_MESSAGE_NAME}").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": _MESSAGE_NAME,
                    "sender": {"name": "users/111"},
                    "createTime": "2026-04-21T10:00:00Z",
                    "text": "edited text",
                    "thread": {"name": "spaces/AAA/threads/T.1"},
                },
            )
        )
        result = await update_message_handler(
            tool_ctx,
            UpdateMessageInput(message_name=_MESSAGE_NAME, text="edited text"),
        )
    assert route.call_count == 1
    # updateMask=text is set as a query param so the patch is text-scoped.
    assert "updateMask=text" in str(route.calls[0].request.url)
    assert result.message_name == _MESSAGE_NAME
    assert result.text == "edited text"
    assert result.dry_run is False
    assert result.rendered_payload is None


@pytest.mark.asyncio
async def test_dry_run_renders_body_and_does_not_patch(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_MESSAGE_NAME}")
        result = await update_message_handler(
            tool_ctx,
            UpdateMessageInput(message_name=_MESSAGE_NAME, text="dry text", dry_run=True),
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.rendered_payload == {"text": "dry text"}


@pytest.mark.asyncio
async def test_dry_run_parity_with_real_patch_body(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """rendered_payload on dry-run equals the body the real PATCH would send."""
    payload = UpdateMessageInput(message_name=_MESSAGE_NAME, text="parity")
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_MESSAGE_NAME}").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": _MESSAGE_NAME,
                    "sender": {"name": "users/111"},
                    "createTime": "2026-04-21T10:00:00Z",
                    "text": "parity",
                    "thread": {"name": "spaces/AAA/threads/T.1"},
                },
            )
        )
        await update_message_handler(tool_ctx, payload)
    real_body = json.loads(route.calls[0].request.content.decode())
    dry_body = _build_update_message_body(text=payload.text)
    assert real_body == dry_body


def test_empty_text_rejected_at_model_boundary() -> None:
    with pytest.raises(ValidationError):
        UpdateMessageInput(message_name=_MESSAGE_NAME, text="")


def test_text_over_4096_chars_rejected() -> None:
    with pytest.raises(ValidationError):
        UpdateMessageInput(message_name=_MESSAGE_NAME, text="x" * 4097)


def test_invalid_message_name_rejected() -> None:
    with pytest.raises(ValidationError):
        UpdateMessageInput(message_name="not-a-message-name", text="x")
