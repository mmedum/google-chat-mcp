"""create_group_chat: GROUP_CHAT via spaces.setup, no displayName."""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from pydantic import ValidationError
from src.chat_client import _build_setup_space_body
from src.models import CreateGroupChatInput
from src.tools import create_group_chat_handler
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_happy_path_returns_space_id_and_member_count(
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
                    "name": "spaces/GC1",
                    "type": "GROUP_CHAT",
                    "spaceType": "GROUP_CHAT",
                },
            )
        )
        result = await create_group_chat_handler(
            tool_ctx,
            CreateGroupChatInput(member_emails=["a@example.com", "b@example.com"]),
        )
    assert route.call_count == 1
    assert result.space_id == "spaces/GC1"
    assert result.member_count == 2
    assert result.dry_run is False
    assert result.rendered_payload is None


@pytest.mark.asyncio
async def test_dry_run_omits_display_name_and_does_not_post(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces:setup")
        result = await create_group_chat_handler(
            tool_ctx,
            CreateGroupChatInput(
                member_emails=["a@example.com", "b@example.com", "c@example.com"],
                dry_run=True,
            ),
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.space_id is None
    assert result.member_count == 3
    assert result.rendered_payload is not None
    # The load-bearing invariant: GROUP_CHAT MUST NOT carry displayName.
    assert "displayName" not in result.rendered_payload["space"]
    assert result.rendered_payload["space"]["spaceType"] == "GROUP_CHAT"
    # Members are rendered as users/{email} HUMAN memberships.
    assert result.rendered_payload["memberships"] == [
        {"member": {"name": "users/a@example.com", "type": "HUMAN"}},
        {"member": {"name": "users/b@example.com", "type": "HUMAN"}},
        {"member": {"name": "users/c@example.com", "type": "HUMAN"}},
    ]


@pytest.mark.asyncio
async def test_dry_run_parity_with_real_post_body(tool_ctx: ToolContext, mock_access_token) -> None:
    """rendered_payload on dry-run equals the body the real POST would send."""
    payload = CreateGroupChatInput(member_emails=["a@example.com", "b@example.com"])
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces:setup").mock(
            return_value=httpx.Response(
                200,
                json={"name": "spaces/GC2", "type": "GROUP_CHAT", "spaceType": "GROUP_CHAT"},
            )
        )
        await create_group_chat_handler(tool_ctx, payload)
    real_body = json.loads(route.calls[0].request.content.decode())
    dry_body = _build_setup_space_body(
        space_type="GROUP_CHAT",
        display_name=None,
        member_emails=list(payload.member_emails),
    )
    assert dry_body == real_body


def test_fewer_than_two_members_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateGroupChatInput(member_emails=["only@example.com"])


def test_more_than_twenty_members_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateGroupChatInput(
            member_emails=[f"u{i}@example.com" for i in range(21)],
        )


def test_invalid_email_rejected() -> None:
    with pytest.raises(ValidationError):
        CreateGroupChatInput(member_emails=["a@example.com", "not-an-email"])
