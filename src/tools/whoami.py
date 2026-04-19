"""Tool: whoami — the authenticated user's identity (self-ID smoke test)."""

from __future__ import annotations

from ..models import WhoamiResult, _UserInfoResponse
from ._common import ToolContext, invoke_tool


async def whoami_handler(ctx: ToolContext) -> WhoamiResult:
    """Return the authenticated user's sub + email + display name via OIDC /userinfo."""

    async def body(access_token: str, user_sub: str) -> WhoamiResult:
        raw = await ctx.client.get_userinfo(access_token)
        info = _UserInfoResponse(**raw)
        # /userinfo sub should match the resolver's sub — if not, it's a
        # token/identity mismatch worth flagging in the response.
        return WhoamiResult(
            user_sub=info.sub,
            email=info.email,
            display_name=info.name,
            picture_url=info.picture,
        )

    # whoami requires the openid/email/profile scopes for /userinfo. All three
    # are in GOOGLE_OAUTH_SCOPES, so a missing-scope error here is unlikely
    # but still wants scope-named surfacing if it ever happens.
    return await invoke_tool("whoami", ctx, body, required_scope="openid")
