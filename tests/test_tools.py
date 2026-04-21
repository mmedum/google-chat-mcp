"""Tool handler integration tests — mock upstream, assert the real handler wiring."""

from __future__ import annotations

import httpx
import pytest
import respx
from fastmcp.exceptions import ToolError
from src.models import GetMessagesInput, ListMembersInput, ListSpacesInput, SendMessageInput
from src.tools import (
    find_direct_message_handler,
    get_messages_handler,
    get_space_handler,
    list_members_handler,
    list_spaces_handler,
    send_message_handler,
)
from src.tools._common import ToolContext


@pytest.mark.asyncio
async def test_list_spaces_handler_happy_path(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces").mock(
            return_value=httpx.Response(
                200,
                json={
                    "spaces": [
                        {"name": "spaces/AAA", "type": "SPACE", "displayName": "#eng"},
                        {"name": "spaces/BBB", "type": "DIRECT_MESSAGE"},
                    ]
                },
            )
        )
        out = await list_spaces_handler(tool_ctx, ListSpacesInput())
    ids = [s.space_id for s in out]
    assert ids == ["spaces/AAA", "spaces/BBB"]
    # DM without displayName falls back to synthetic label, not empty string.
    dm = next(s for s in out if s.type == "DIRECT_MESSAGE")
    assert dm.display_name == "(direct message)"


@pytest.mark.asyncio
async def test_list_spaces_respects_limit(tool_ctx: ToolContext, mock_access_token) -> None:
    # Upstream returns 3 on page 1 and would return more, but limit=2 must stop
    # pagination and slice the result.
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.get("/spaces").mock(
            return_value=httpx.Response(
                200,
                json={
                    "spaces": [
                        {"name": "spaces/A", "type": "SPACE", "displayName": "#one"},
                        {"name": "spaces/B", "type": "SPACE", "displayName": "#two"},
                        {"name": "spaces/C", "type": "SPACE", "displayName": "#three"},
                    ],
                    "nextPageToken": "would-be-page-2",
                },
            )
        )
        out = await list_spaces_handler(tool_ctx, ListSpacesInput(limit=2))
    assert [s.space_id for s in out] == ["spaces/A", "spaces/B"]
    # Only one upstream call even though nextPageToken was set.
    assert len(route.calls) == 1
    # pageSize narrowed to the remaining budget.
    assert route.calls[0].request.url.params["pageSize"] == "2"


@pytest.mark.asyncio
async def test_list_spaces_forwards_space_type_filter(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": []}))
        await list_spaces_handler(tool_ctx, ListSpacesInput(space_type="DIRECT_MESSAGE"))
    assert route.calls[0].request.url.params["filter"] == 'spaceType = "DIRECT_MESSAGE"'


@pytest.mark.asyncio
async def test_find_direct_message_creates_on_404(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces:findDirectMessage").mock(return_value=httpx.Response(404))
        mock.post("/spaces:setup").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/NEWDM",
                    "type": "DIRECT_MESSAGE",
                },
            )
        )
        result = await find_direct_message_handler(tool_ctx, "alice@example.com")
    assert result.space_id == "spaces/NEWDM"


@pytest.mark.asyncio
async def test_find_direct_message_missing_scope_surfaces_reauth_prompt(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    """Missing `chat.spaces.create` on the create-on-miss path must produce
    the scope-specific re-auth prompt, not the generic "is the user in your
    Workspace directory?" rewrite that the handler emits for other 4xx.
    """
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces:findDirectMessage").mock(return_value=httpx.Response(404))
        mock.post("/spaces:setup").mock(
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
                                "domain": "googleapis.com",
                            }
                        ],
                    }
                },
            )
        )
        with pytest.raises(ToolError) as exc_info:
            await find_direct_message_handler(tool_ctx, "alice@example.com")
    msg = str(exc_info.value)
    assert "Missing required OAuth scope" in msg
    # The create-on-miss path 403'd; the re-auth prompt must name
    # `chat.spaces.create` (the actually-missing scope) rather than the
    # pre-flight tag `chat.spaces.readonly` that invoke_tool would have
    # surfaced by default. The generic directory-lookup rewrite must also
    # not fire — it would mask the scope gap entirely.
    assert "chat.spaces.create" in msg
    assert "chat.spaces.readonly" not in msg
    assert "Workspace directory" not in msg


@pytest.mark.asyncio
async def test_send_message_posts_body_verbatim(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        route = mock.post("/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/AAA/messages/M.1",
                    "sender": {"name": "users/111"},
                    "createTime": "2026-04-19T10:00:00Z",
                    "text": "hi",
                    "thread": {"name": "spaces/AAA/threads/T.1"},
                },
            )
        )
        out = await send_message_handler(
            tool_ctx, SendMessageInput(space_id="spaces/AAA", text="hi")
        )
    assert out.message_id == "spaces/AAA/messages/M.1"
    # Body is sent verbatim — no client-identifying suffix.
    import json

    sent_body = json.loads(route.calls[0].request.content.decode())
    assert sent_body["text"] == "hi"


@pytest.mark.asyncio
async def test_get_messages_resolves_sender_via_people_api(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [
                        {
                            "name": "spaces/AAA/messages/M.1",
                            "sender": {"name": "users/111", "displayName": "Alice"},
                            "createTime": "2026-04-19T10:00:00Z",
                            "text": "hello",
                            "thread": {"name": "spaces/AAA/threads/T.1"},
                        }
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice Smith", "metadata": {"primary": True}}],
                },
            )
        )
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA", limit=5))
    assert len(out) == 1
    msg = out[0]
    assert msg.sender_email == "alice@example.com"
    assert msg.sender_display_name == "Alice Smith"


@pytest.mark.asyncio
async def test_get_messages_missing_people_entry_returns_display_only(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={
                    "messages": [
                        {
                            "name": "spaces/AAA/messages/M.1",
                            "sender": {"name": "users/999", "displayName": "External"},
                            "createTime": "2026-04-19T10:00:00Z",
                            "text": "x",
                            "thread": {"name": "spaces/AAA/threads/T.1"},
                        }
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/999").mock(return_value=httpx.Response(404))
        out = await get_messages_handler(tool_ctx, GetMessagesInput(space_id="spaces/AAA"))
    assert out[0].sender_email is None
    assert out[0].sender_display_name == "External"


@pytest.mark.asyncio
async def test_get_space_returns_details(tool_ctx: ToolContext, mock_access_token) -> None:
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces/AAA").mock(
            return_value=httpx.Response(
                200,
                json={
                    "name": "spaces/AAA",
                    "type": "ROOM",
                    "spaceType": "SPACE",
                    "displayName": "#eng",
                    "createTime": "2026-01-01T00:00:00Z",
                },
            )
        )
        out = await get_space_handler(tool_ctx, "spaces/AAA")
    assert out.space_id == "spaces/AAA"
    assert out.type == "SPACE"
    assert out.display_name == "#eng"
    assert out.create_time is not None


@pytest.mark.asyncio
async def test_list_members_resolves_humans_and_passes_through_groups(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/members").mock(
            return_value=httpx.Response(
                200,
                json={
                    "memberships": [
                        {
                            "name": "spaces/AAA/members/1",
                            "state": "JOINED",
                            "role": "ROLE_MEMBER",
                            "member": {
                                "name": "users/111",
                                "displayName": "Alice (fallback)",
                            },
                        },
                        {
                            "name": "spaces/AAA/members/2",
                            "state": "JOINED",
                            "role": "ROLE_MANAGER",
                            "groupMember": {
                                "name": "groups/eng-team",
                                "displayName": "Engineering",
                            },
                        },
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/111").mock(
            return_value=httpx.Response(
                200,
                json={
                    "emailAddresses": [
                        {"value": "alice@example.com", "metadata": {"primary": True}}
                    ],
                    "names": [{"displayName": "Alice Smith", "metadata": {"primary": True}}],
                },
            )
        )
        out = await list_members_handler(tool_ctx, ListMembersInput(space_id="spaces/AAA"))
    assert len(out) == 2
    human = next(m for m in out if m.kind == "HUMAN")
    assert human.member_id == "users/111"
    assert human.email == "alice@example.com"
    assert human.display_name == "Alice Smith"
    assert human.role == "ROLE_MEMBER"
    group = next(m for m in out if m.kind == "GROUP")
    assert group.member_id == "groups/eng-team"
    assert group.display_name == "Engineering"
    assert group.email is None
    assert group.role == "ROLE_MANAGER"


@pytest.mark.asyncio
async def test_list_members_handles_missing_people_entry(
    tool_ctx: ToolContext, mock_access_token
) -> None:
    with (
        respx.mock() as mock,
        mock_access_token(),
    ):
        mock.get("https://chat.test/v1/spaces/AAA/members").mock(
            return_value=httpx.Response(
                200,
                json={
                    "memberships": [
                        {
                            "name": "spaces/AAA/members/1",
                            "state": "JOINED",
                            "member": {"name": "users/999", "displayName": "External"},
                        }
                    ]
                },
            )
        )
        mock.get("https://people.test/v1/people/999").mock(return_value=httpx.Response(404))
        out = await list_members_handler(tool_ctx, ListMembersInput(space_id="spaces/AAA"))
    assert out[0].email is None
    assert out[0].display_name == "External"
    assert out[0].role == "ROLE_UNSPECIFIED"


@pytest.mark.asyncio
async def test_rate_limit_blocks_after_capacity(tool_ctx: ToolContext, mock_access_token) -> None:
    # Replace limiter with a tight one.
    from src.rate_limit import TokenBucketLimiter

    tool_ctx.limiter = TokenBucketLimiter(capacity=1)
    with (
        respx.mock(base_url="https://chat.test/v1") as mock,
        mock_access_token(),
    ):
        mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": []}))
        await list_spaces_handler(tool_ctx, ListSpacesInput())
        with pytest.raises(ToolError, match="Rate limit"):
            await list_spaces_handler(tool_ctx, ListSpacesInput())
