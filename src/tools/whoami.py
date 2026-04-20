"""Tool: whoami — the authenticated user's identity (self-ID smoke test)."""

from __future__ import annotations

from ..models import WhoamiResult, _UserInfoResponse
from ._common import OPENID_SCOPE, ToolContext, invoke_tool


async def whoami_handler(ctx: ToolContext) -> WhoamiResult:
    """Return the authenticated user's sub + email + display name via OIDC /userinfo."""

    async def body(access_token: str, user_sub: str) -> WhoamiResult:
        raw = await ctx.client.get_userinfo(access_token)
        info = _UserInfoResponse(**raw)
        return WhoamiResult(
            user_sub=info.sub,
            email=info.email,
            display_name=info.name,
            picture_url=info.picture,
        )

    return await invoke_tool("whoami", ctx, body, required_scope=OPENID_SCOPE)
