"""End-to-end harness for the HTTPS entry point.

Builds the full FastMCP app (tool + resource registration, lifespan, custom
routes) with a stub `AuthProvider` so the HTTP surface comes up without a
real `GoogleProvider`. Exercises three paths:

- `/healthz` — plain text "ok"
- `/readyz`  — probes the SQLite DB + kv_store disk canary
- `/metrics` — Prometheus text output, proving the registry is wired
- one tool call (`whoami`) via the in-process `fastmcp.Client` so the
  lifespan populates `ToolContext` and the tool handler actually runs.

`src/server.py`'s real composition uses `GoogleProvider`; here a token-
verifier stub is enough because `build_app` treats `auth` as opaque and
only the presence-vs-absence of it gates custom-route registration.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path

import httpx
import pytest
import respx
from fastmcp import Client, FastMCP
from fastmcp.server.auth.auth import AccessToken, TokenVerifier
from pydantic import AnyHttpUrl
from src.app import build_app
from src.config import Settings


class _StubAuthProvider(TokenVerifier):
    """Accept any non-empty bearer as a well-formed upstream token.

    We don't exercise the HTTP `/mcp` endpoint in this harness (that would
    need a real client wiring a JWT), so `verify_token` is effectively
    unused — but `build_app` checks `auth is not None` to decide whether
    to register `/healthz`/`/readyz`/`/metrics`, and it asserts the
    provider has a `base_url`. A minimal `TokenVerifier` subclass is the
    cheapest way to satisfy both.
    """

    def __init__(self, base_url: str) -> None:
        super().__init__(base_url=AnyHttpUrl(base_url))

    async def verify_token(self, token: str) -> AccessToken | None:
        if not token:
            return None
        return AccessToken(
            token=token,
            client_id="stub-client",
            scopes=[],
            claims={"sub": "stub-user"},
        )


@pytest.fixture
def mcp_https() -> FastMCP:
    """build_app wired with a stub AuthProvider so HTTPS custom routes register."""
    settings = Settings.from_env()
    auth = _StubAuthProvider(base_url=settings.base_url)
    return build_app(settings, auth=auth)


@asynccontextmanager
async def _http_client(app: FastMCP) -> AsyncIterator[httpx.AsyncClient]:
    """ASGI-in-process HTTP client with lifespan driven manually.

    `httpx.ASGITransport` doesn't run ASGI lifespan events, so the
    build_app lifespan (which populates `ToolContext` — `readyz` reads it)
    has to be entered explicitly via Starlette's `lifespan_context`.
    """
    http_app = app.http_app()
    async with http_app.router.lifespan_context(http_app):
        transport = httpx.ASGITransport(app=http_app)
        async with httpx.AsyncClient(transport=transport, base_url="http://testserver") as client:
            yield client


async def test_healthz_returns_ok(mcp_https: FastMCP) -> None:
    async with _http_client(mcp_https) as client:
        resp = await client.get("/healthz")
    assert resp.status_code == 200
    assert resp.text == "ok"


async def test_readyz_reports_ready_after_lifespan(
    mcp_https: FastMCP, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Lifespan must populate ToolContext + a writable kv_store path.

    The default `data_dir` under the conftest fixture already points at
    tmp_path, and the kv_store canary write is what the route probes.
    """
    monkeypatch.setenv("GCM_DATA_DIR", str(tmp_path))
    mcp = build_app(
        Settings.from_env(), auth=_StubAuthProvider(base_url="https://mcp.example.test")
    )
    async with _http_client(mcp) as client:
        resp = await client.get("/readyz")
    assert resp.status_code == 200
    assert resp.json() == {"ready": True}


async def test_metrics_returns_prometheus_text(mcp_https: FastMCP) -> None:
    async with _http_client(mcp_https) as client:
        resp = await client.get("/metrics")
    assert resp.status_code == 200
    # prometheus_client sets an explicit Content-Type with `version=0.0.4`;
    # its exact shape is controlled by the library. Just assert the prefix.
    assert resp.headers["content-type"].startswith("text/plain")
    body = resp.text
    # Sanity: our custom metrics are registered even if their counters are 0.
    assert "mcp_tool_calls_total" in body or "mcp_active_users" in body


async def test_tool_call_via_in_process_client(mcp_https: FastMCP, mock_access_token) -> None:
    """build_app with auth= still dispatches tool calls through the lifespan-backed ctx.

    The in-process `Client(mcp)` does NOT traverse the HTTP auth layer —
    it uses the internal FastMCP connection. The `mock_access_token`
    fixture stubs `get_access_token()` so the tool handler's fallback
    resolver yields a fake upstream token; `whoami` then hits a respx'd
    OIDC /userinfo.
    """
    with (
        respx.mock(assert_all_called=False) as route_mock,
        mock_access_token(),
    ):
        route_mock.get(url__regex=r"https://openidconnect\.googleapis\.com/v1/userinfo").mock(
            return_value=httpx.Response(
                200,
                json={
                    "sub": "test-user-sub",
                    "email": "alice@example.com",
                    "name": "Alice",
                },
            )
        )
        async with Client(mcp_https) as client:
            result = await client.call_tool("whoami", {})
    assert result.structured_content is not None
    assert result.structured_content["email"] == "alice@example.com"
