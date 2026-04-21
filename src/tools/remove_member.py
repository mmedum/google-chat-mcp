"""Tool: remove_member — delete a membership via spaces.members.delete."""

from __future__ import annotations

from ..chat_client import ChatApiError
from ..models import RemoveMemberInput, RemoveMemberResult
from ._common import (
    CHAT_MEMBERSHIPS,
    ToolContext,
    invoke_tool,
    is_missing_scope_error,
)


async def remove_member_handler(ctx: ToolContext, payload: RemoveMemberInput) -> RemoveMemberResult:
    """Remove a membership by full resource name.

    Idempotent: double-delete returns `removed=False` on 404 (NOT_FOUND) or
    403 (PERMISSION_DENIED that isn't the missing-scope shape — e.g.
    `spaces/X/members/Y` that no longer exists can surface as 403 rather
    than 404 depending on the space's history state). Missing-scope 403s
    are excluded from the idempotent-success path so callers still see the
    re-auth prompt.
    """
    if payload.dry_run:

        async def dry_body(_access_token: str, _user_sub: str) -> RemoveMemberResult:
            return RemoveMemberResult(
                membership_name=payload.membership_name,
                removed=False,
                dry_run=True,
            )

        return await invoke_tool(
            "remove_member",
            ctx,
            dry_body,
            target_space_id=_space_id_from_membership(payload.membership_name),
            required_scope=CHAT_MEMBERSHIPS,
        )

    async def body(access_token: str, _user_sub: str) -> RemoveMemberResult:
        try:
            await ctx.client.remove_member(access_token, payload.membership_name)
        except ChatApiError as exc:
            if _is_gone_or_forbidden(exc):
                return RemoveMemberResult(
                    membership_name=payload.membership_name,
                    removed=False,
                )
            raise
        return RemoveMemberResult(
            membership_name=payload.membership_name,
            removed=True,
        )

    return await invoke_tool(
        "remove_member",
        ctx,
        body,
        target_space_id=_space_id_from_membership(payload.membership_name),
        required_scope=CHAT_MEMBERSHIPS,
    )


def _is_gone_or_forbidden(exc: ChatApiError) -> bool:
    """True when the membership is already gone. Excludes missing-scope 403s."""
    if is_missing_scope_error(exc):
        return False
    if exc.status_code == 404 or exc.google_status == "NOT_FOUND":
        return True
    return exc.status_code == 403 and exc.google_status == "PERMISSION_DENIED"


def _space_id_from_membership(membership_name: str) -> str:
    """Extract `spaces/{S}` from a `spaces/{S}/members/{M}` resource name."""
    return membership_name.rsplit("/members/", 1)[0]
