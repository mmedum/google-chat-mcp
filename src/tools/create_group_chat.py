"""Tool: create_group_chat — unnamed multi-person DM via `spaces.setup`."""

from __future__ import annotations

from ..chat_client import _build_setup_space_body
from ..models import CreateGroupChatInput, CreateGroupChatResult, _ChatSpaceResponse
from ._common import CHAT_SPACES_CREATE, ToolContext, invoke_tool


async def create_group_chat_handler(
    ctx: ToolContext, payload: CreateGroupChatInput
) -> CreateGroupChatResult:
    """Create a `GROUP_CHAT` space with the given members.

    `payload.dry_run=True` builds the request body without POSTing, mirroring
    `send_message`'s dry-run contract. Rate-limit bucket + audit row still fire.
    """

    async def body(access_token: str, _user_sub: str) -> CreateGroupChatResult:
        # EmailStr subclasses str, so member_emails is already a list[str]
        # at runtime — no cast needed.
        member_emails = list(payload.member_emails)
        if payload.dry_run:
            rendered = _build_setup_space_body(
                space_type="GROUP_CHAT",
                display_name=None,
                member_emails=member_emails,
            )
            return CreateGroupChatResult(
                member_count=len(member_emails),
                dry_run=True,
                rendered_payload=rendered,
            )
        raw = await ctx.client.setup_space(
            access_token,
            space_type="GROUP_CHAT",
            display_name=None,
            member_emails=member_emails,
        )
        space = _ChatSpaceResponse(**raw)
        return CreateGroupChatResult(
            space_id=space.name,
            member_count=len(member_emails),
        )

    return await invoke_tool(
        "create_group_chat",
        ctx,
        body,
        required_scope=CHAT_SPACES_CREATE,
    )
