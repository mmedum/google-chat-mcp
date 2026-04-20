"""create_space: named SPACE via spaces.setup, displayName required."""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from pydantic import ValidationError
from src.chat_client import _build_setup_space_body
from src.models import CreateSpaceInput
from src.tools import create_space_handler
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_happy_path_returns_space_id_display_name_and_member_count(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces:setup").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/S1",
                    "type": "SPACE",
                    "spaceType": "SPACE",
                    "displayName": "design-review",
                },
            )
        )
        result = await create_space_handler(
            tool_ctx,
            CreateSpaceInput(
                member_emails=["a@example.com"],
                display_name="design-review",
            ),
        )
    assert route.call_count == 1
    assert result.space_id == "spaces/S1"
    assert result.display_name == "design-review"
    assert result.member_count == 1
    assert result.dry_run is False


@pytest.mark.asyncio
async def test_dry_run_includes_display_name_and_does_not_post(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces:setup")
        result = await create_space_handler(
            tool_ctx,
            CreateSpaceInput(
                member_emails=["a@example.com", "b@example.com"],
                display_name="eng-standup",
                dry_run=True,
            ),
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.space_id is None
    assert result.display_name == "eng-standup"
    assert result.member_count == 2
    assert result.rendered_payload is not None
    # The load-bearing invariant: SPACE carries displayName.
    assert result.rendered_payload["space"] == {
        "spaceType": "SPACE",
        "displayName": "eng-standup",
    }


@pytest.mark.asyncio
async def test_dry_run_parity_with_real_post_body(tool_ctx: ToolContext, mock_access_token) -> None:
    payload = CreateSpaceInput(member_emails=["a@example.com"], display_name="parity")
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces:setup").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/SP",
                    "type": "SPACE",
                    "spaceType": "SPACE",
                    "displayName": "parity",
                },
            )
        )
        await create_space_handler(tool_ctx, payload)
    real_body = json.loads(route.calls[0].request.content.decode())
    dry_body = _build_setup_space_body(
        space_type="SPACE",
        display_name=payload.display_name,
        member_emails=list(payload.member_emails),
    )
    assert dry_body == real_body


def test_missing_display_name_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateSpaceInput(member_emails=["a@example.com"])  # ty: ignore[missing-argument]


def test_empty_display_name_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateSpaceInput(member_emails=["a@example.com"], display_name="")


def test_zero_members_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateSpaceInput(member_emails=[], display_name="empty")


def test_more_than_twenty_members_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateSpaceInput(
            member_emails=[f"u{i}@example.com" for i in range(21)],
            display_name="big",
        )
