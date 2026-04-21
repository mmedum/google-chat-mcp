"""update_space: rename or patch description via spaces.patch."""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from pydantic import ValidationError
from src.chat_client import _build_update_space_body
from src.models import UpdateSpaceInput
from src.tools import update_space_handler
from src.tools._common import ToolContext

_SPACE_ID = "spaces/AAA"


def _space_response(display_name: str = "renamed") -> dict:
    """Minimal `_ChatSpaceResponse`-compatible payload. `type` is required."""
    return {
        "name": _SPACE_ID,
        "type": "SPACE",
        "displayName": display_name,
    }


@pytest.mark.asyncio
async def test_display_name_only_uses_narrow_mask(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_SPACE_ID}").mock(
            return_value=httpx.Response(200, json=_space_response("renamed"))
        )
        result = await update_space_handler(
            tool_ctx,
            UpdateSpaceInput(space_id=_SPACE_ID, display_name="renamed"),
        )
    assert route.call_count == 1
    assert "updateMask=displayName" in str(route.calls[0].request.url)
    assert "description" not in str(route.calls[0].request.url)
    assert result.space_id == _SPACE_ID
    assert result.display_name == "renamed"
    assert result.description is None
    assert result.dry_run is False
    assert result.update_mask == "displayName"


@pytest.mark.asyncio
async def test_description_only_uses_narrow_mask(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_SPACE_ID}").mock(
            return_value=httpx.Response(200, json=_space_response())
        )
        result = await update_space_handler(
            tool_ctx,
            UpdateSpaceInput(space_id=_SPACE_ID, description="project goals"),
        )
    assert route.call_count == 1
    url = str(route.calls[0].request.url)
    assert "updateMask=spaceDetails" in url
    assert "displayName" not in url
    assert result.description == "project goals"
    assert result.update_mask == "spaceDetails"
    body = json.loads(route.calls[0].request.content.decode())
    assert body == {"spaceDetails": {"description": "project goals"}}


@pytest.mark.asyncio
async def test_both_fields_joined_in_mask(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_SPACE_ID}").mock(
            return_value=httpx.Response(200, json=_space_response("new-name"))
        )
        result = await update_space_handler(
            tool_ctx,
            UpdateSpaceInput(
                space_id=_SPACE_ID,
                display_name="new-name",
                description="more context",
            ),
        )
    # URL-encoded comma is %2C; respx normalizes on urllib.parse so either form is fine.
    url = str(route.calls[0].request.url)
    assert ("updateMask=displayName%2CspaceDetails" in url) or (
        "updateMask=displayName,spaceDetails" in url
    )
    assert result.update_mask == "displayName,spaceDetails"
    body = json.loads(route.calls[0].request.content.decode())
    assert body == {
        "displayName": "new-name",
        "spaceDetails": {"description": "more context"},
    }


@pytest.mark.asyncio
async def test_dry_run_renders_body_and_does_not_patch(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_SPACE_ID}")
        result = await update_space_handler(
            tool_ctx,
            UpdateSpaceInput(
                space_id=_SPACE_ID,
                display_name="preview",
                dry_run=True,
            ),
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.rendered_payload == {"displayName": "preview"}
    assert result.update_mask == "displayName"


@pytest.mark.asyncio
async def test_dry_run_parity_with_real_patch_body(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """rendered_payload on dry-run equals the body the real PATCH would send."""
    payload = UpdateSpaceInput(
        space_id=_SPACE_ID,
        display_name="parity",
        description="matching body",
    )
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.patch(f"/{_SPACE_ID}").mock(
            return_value=httpx.Response(200, json=_space_response("parity"))
        )
        await update_space_handler(tool_ctx, payload)
    real_body = json.loads(route.calls[0].request.content.decode())
    dry_body, _mask = _build_update_space_body(
        display_name=payload.display_name, description=payload.description
    )
    assert real_body == dry_body


def test_empty_update_rejected_at_model_boundary() -> None:
    # Neither field supplied → Google would 400 on empty updateMask. Reject at
    # the tool-input layer so agent flows get a clearer error.
    with pytest.raises(ValidationError):
        UpdateSpaceInput(space_id=_SPACE_ID)


def test_display_name_min_length_enforced() -> None:
    with pytest.raises(ValidationError):
        UpdateSpaceInput(space_id=_SPACE_ID, display_name="")


def test_display_name_over_128_chars_rejected() -> None:
    with pytest.raises(ValidationError):
        UpdateSpaceInput(space_id=_SPACE_ID, display_name="x" * 129)


def test_description_over_150_chars_rejected() -> None:
    with pytest.raises(ValidationError):
        UpdateSpaceInput(space_id=_SPACE_ID, description="x" * 151)


def test_invalid_space_id_rejected() -> None:
    with pytest.raises(ValidationError):
        UpdateSpaceInput(space_id="not-a-space", display_name="x")
