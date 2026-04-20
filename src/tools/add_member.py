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

    Duplicate-member handling: in practice Google's `spaces.members.create`
    is idempotent-by-nature — duplicate adds typically return HTTP 200 with
    the existing membership record, so `AddMemberResult.membership_name` is
    populated even when the user was already present. Older Workspace
    editions / some edge cases still return 409 ALREADY_EXISTS, which the
    handler wraps into a `ToolError("already a member")`. Both branches
    live; see `docs/runbook.md` ("add_member returns a membership_name
    when the user is already present") for the operator-facing framing.
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
