"""Tool: create_space — named space via `spaces.setup`."""

from __future__ import annotations

from ..chat_client import _build_setup_space_body
from ..models import CreateSpaceInput, CreateSpaceResult, _ChatSpaceResponse
from ._common import CHAT_SPACES_CREATE, ToolContext, invoke_tool


async def create_space_handler(ctx: ToolContext, payload: CreateSpaceInput) -> CreateSpaceResult:
    """Create a `SPACE` with the given display_name + members.

    `payload.dry_run=True` short-circuits the POST and returns the rendered
    body instead. Rate-limit bucket + audit row still fire.
    """

    async def body(access_token: str, _user_sub: str) -> CreateSpaceResult:
        # EmailStr subclasses str, so member_emails is already a list[str]
        # at runtime — no cast needed.
        member_emails = list(payload.member_emails)
        if payload.dry_run:
            rendered = _build_setup_space_body(
                space_type="SPACE",
                display_name=payload.display_name,
                member_emails=member_emails,
            )
            return CreateSpaceResult(
                display_name=payload.display_name,
                member_count=len(member_emails),
                dry_run=True,
                rendered_payload=rendered,
            )
        raw = await ctx.client.setup_space(
            access_token,
            space_type="SPACE",
            display_name=payload.display_name,
            member_emails=member_emails,
        )
        space = _ChatSpaceResponse(**raw)
        return CreateSpaceResult(
            space_id=space.name,
            display_name=payload.display_name,
            member_count=len(member_emails),
        )

    return await invoke_tool(
        "create_space",
        ctx,
        body,
        required_scope=CHAT_SPACES_CREATE,
    )
