"""remove_member: idempotent delete by membership name; guarded 404/403 handling."""

from __future__ import annotations

import httpx
import pytest
import respx
from fastmcp.exceptions import ToolError
from src.models import RemoveMemberInput
from src.tools import remove_member_handler
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_happy_path_returns_removed_true(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.delete("/spaces/AAA/members/111").mock(
            return_value=httpx.Response(200, json={})
        )
        result = await remove_member_handler(
            tool_ctx, RemoveMemberInput(membership_name="spaces/AAA/members/111")
        )
    assert route.call_count == 1
    assert result.removed is True
    assert result.membership_name == "spaces/AAA/members/111"
    assert result.dry_run is False


@pytest.mark.asyncio
async def test_dry_run_does_not_delete(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1", assert_all_called=False) as mock,
        mock_access_token(),
    ):
        route = mock.delete("/spaces/AAA/members/111")
        result = await remove_member_handler(
            tool_ctx,
            RemoveMemberInput(membership_name="spaces/AAA/members/111", dry_run=True),
        )
    assert route.call_count == 0
    assert result.dry_run is True
    assert result.removed is False


@pytest.mark.asyncio
async def test_double_delete_returns_removed_false_on_404(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.delete("/spaces/AAA/members/111").mock(
            return_value=httpx.Response(
                404,
                json={"error": {"code": 404, "message": "gone", "status": "NOT_FOUND"}},
            )
        )
        result = await remove_member_handler(
            tool_ctx, RemoveMemberInput(membership_name="spaces/AAA/members/111")
        )
    assert result.removed is False


@pytest.mark.asyncio
async def test_permission_denied_returns_removed_false(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """403 PERMISSION_DENIED (non-missing-scope) → idempotent removed=false."""
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.delete("/spaces/AAA/members/111").mock(
            return_value=httpx.Response(
                403,
                json={
                    "error": {
                        "code": 403,
                        "message": "caller can't see this",
                        "status": "PERMISSION_DENIED",
                    }
                },
            )
        )
        result = await remove_member_handler(
            tool_ctx, RemoveMemberInput(membership_name="spaces/AAA/members/111")
        )
    assert result.removed is False


@pytest.mark.asyncio
async def test_missing_scope_403_still_raises_tool_error(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Missing-scope 403 must NOT be swallowed as idempotent success."""
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.delete("/spaces/AAA/members/111").mock(
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
            await remove_member_handler(
                tool_ctx, RemoveMemberInput(membership_name="spaces/AAA/members/111")
            )


def test_invalid_membership_name_rejected_at_model_boundary() -> None:
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        RemoveMemberInput(membership_name="not-a-membership-name")
