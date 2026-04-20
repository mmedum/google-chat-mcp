"""Tool: add_member — invite a user to a space via spaces.members.create."""

from __future__ import annotations

from fastmcp.exceptions import ToolError

from ..chat_client import ChatApiError, _build_add_member_body
from ..models import AddMemberInput, AddMemberResult, _ChatMembershipResponse
from ._common import CHAT_MEMBERSHIPS, ToolContext, invoke_tool


async def add_member_handler(ctx: ToolContext, payload: AddMemberInput) -> AddMemberResult:
    """Invite `user_email` into `space_id`.

    `payload.dry_run=True` builds the request body without POSTing.
    Rate-limit bucket + audit row still fire.

    Duplicate-member handling: Google returns 409 ALREADY_EXISTS if the user
    is already in the space. That surfaces as a `ToolError("already a
    member")` rather than an idempotent success — the caller's intent
    ("invite this person") is a no-op semantically, but the membership_name
    shape differs depending on who added the user (the original inviter,
    not the current caller), so we can't faithfully return it.
    """
    user_email = str(payload.user_email)

    async def body(access_token: str, _user_sub: str) -> AddMemberResult:
        if payload.dry_run:
            rendered = _build_add_member_body(user_email=user_email)
            return AddMemberResult(
                space_id=payload.space_id,
                user_email=payload.user_email,
                dry_run=True,
                rendered_payload=rendered,
            )
        try:
            raw = await ctx.client.add_member(access_token, payload.space_id, user_email)
        except ChatApiError as exc:
            if _is_already_exists(exc):
                raise ToolError(f"{user_email} is already a member of {payload.space_id}.") from exc
            raise
        parsed = _ChatMembershipResponse(**raw)
        return AddMemberResult(
            membership_name=parsed.name,
            space_id=payload.space_id,
            user_email=payload.user_email,
        )

    return await invoke_tool(
        "add_member",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_MEMBERSHIPS,
    )


def _is_already_exists(exc: ChatApiError) -> bool:
    """Detect Google's duplicate-membership response (AIP-193 shape)."""
    if exc.status_code == 409:
        return True
    return exc.google_status == "ALREADY_EXISTS"
