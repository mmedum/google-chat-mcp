"""add_member: invite by email, idempotent-adjacent handling on duplicate."""

from __future__ import annotations

import httpx
import pytest
import respx
from fastmcp.exceptions import ToolError
from src.chat_client import _build_add_member_body
from src.models import AddMemberInput
from src.tools import add_member_handler
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_happy_path_returns_membership_name(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces/AAA/members").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/AAA/members/111",
                    "state": "JOINED",
                    "role": "ROLE_MEMBER",
                    "member": {"name": "users/111", "type": "HUMAN"},
                },
            )
        )
        result = await add_member_handler(
            tool_ctx,
            AddMemberInput(space_id="spaces/AAA", user_email="new@example.com"),
        )
    assert route.call_count == 1
    assert result.membership_name == "spaces/AAA/members/111"
    assert result.space_id == "spaces/AAA"
    assert result.user_email == "new@example.com"
    assert result.dry_run is False


@pytest.mark.asyncio
async def test_dry_run_does_not_post_and_renders_body(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces/AAA/members")
        result = await add_member_handler(
            tool_ctx,
            AddMemberInput(space_id="spaces/AAA", user_email="new@example.com", dry_run=True),
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.membership_name is None
    assert result.rendered_payload == {"member": {"name": "users/new@example.com", "type": "HUMAN"}}


def test_dry_run_builder_matches_real_post() -> None:
    """_build_add_member_body is the single source of truth for both paths."""
    assert _build_add_member_body(user_email="x@y.com") == {
        "member": {"name": "users/x@y.com", "type": "HUMAN"}
    }


@pytest.mark.asyncio
async def test_already_a_member_raises_tool_error(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.post("/spaces/AAA/members").mock(
            return_value=httpx.Response(
                409,
                json={
                    "error": {
                        "code": 409,
                        "message": "already a member",
                        "status": "ALREADY_EXISTS",
                    }
                },
            )
        )
        with pytest.raises(ToolError, match="already a member"):
            await add_member_handler(
                tool_ctx,
                AddMemberInput(space_id="spaces/AAA", user_email="dup@example.com"),
            )


@pytest.mark.asyncio
async def test_unrelated_500_propagates_as_tool_error(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Non-409 errors still go through invoke_tool's ChatApiError → ToolError wrap."""
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        # respx retries handled by chat_client retry loop — return 500 four times
        # to exceed max_retries (3) + initial attempt.
        mock.post("/spaces/AAA/members").mock(
            return_value=httpx.Response(500, json={"error": {"message": "boom"}})
        )
        with pytest.raises(ToolError):
            await add_member_handler(
                tool_ctx,
                AddMemberInput(space_id="spaces/AAA", user_email="x@example.com"),
            )


def test_invalid_email_rejected_at_model_boundary() -> None:
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        AddMemberInput(space_id="spaces/AAA", user_email="not-an-email")
