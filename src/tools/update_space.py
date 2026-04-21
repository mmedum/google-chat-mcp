"""Tool: update_space — rename a space or edit its description via spaces.patch."""

from __future__ import annotations

from ..chat_client import _build_update_space_body
from ..models import UpdateSpaceInput, UpdateSpaceResult, _ChatSpaceResponse
from ._common import CHAT_SPACES, ToolContext, invoke_tool


async def update_space_handler(ctx: ToolContext, payload: UpdateSpaceInput) -> UpdateSpaceResult:
    """Patch a space's `displayName` and/or `description`.

    `payload.dry_run=True` builds the patch body + mask without PATCHing —
    same dry/real-parity contract as `update_message`. Rate-limit bucket
    + audit row still fire.

    Other space fields (permission settings, history state, space type)
    are out of scope for v0.4.0; the tool intentionally accepts only the
    two text fields a per-user OAuth caller most commonly needs to edit.
    """

    async def body(access_token: str, _user_sub: str) -> UpdateSpaceResult:
        rendered, mask = _build_update_space_body(
            display_name=payload.display_name,
            description=payload.description,
        )
        if payload.dry_run:
            return UpdateSpaceResult(
                space_id=payload.space_id,
                display_name=payload.display_name,
                description=payload.description,
                dry_run=True,
                rendered_payload=rendered,
                update_mask=mask,
            )
        raw = await ctx.client.update_space(
            access_token,
            payload.space_id,
            display_name=payload.display_name,
            description=payload.description,
        )
        # Validate the response shape (catches schema drift via extra="forbid")
        # but echo the input values back: a 2xx confirms Google applied them,
        # and `_ChatSpaceResponse` doesn't surface `description` at the top
        # level (it's nested under `spaceDetails`). Returning what the caller
        # asked for is unambiguous.
        parsed = _ChatSpaceResponse(**raw)
        return UpdateSpaceResult(
            space_id=parsed.name,
            display_name=payload.display_name,
            description=payload.description,
            dry_run=False,
            update_mask=mask,
        )

    return await invoke_tool(
        "update_space",
        ctx,
        body,
        target_space_id=payload.space_id,
        required_scope=CHAT_SPACES,
    )
