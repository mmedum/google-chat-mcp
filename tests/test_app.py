"""build_app assembly — tool registration, server identity, annotations, resources."""

from __future__ import annotations

import pytest
from src.app import build_app
from src.config import Settings


@pytest.fixture
def settings() -> Settings:
    return Settings.from_env()


@pytest.mark.asyncio
async def test_build_app_registers_all_tools(settings: Settings) -> None:
    mcp = build_app(settings)
    tools = await mcp.list_tools()
    names = {t.name for t in tools}
    # v1 tools + the new ones that have shipped so far.
    expected_so_far = {
        "list_spaces",
        "find_direct_message",
        "send_message",
        "get_messages",
        "get_space",
        "list_members",
        "whoami",
    }
    assert expected_so_far.issubset(names)


@pytest.mark.asyncio
async def test_build_app_registers_space_resource(settings: Settings) -> None:
    mcp = build_app(settings)
    templates = await mcp.list_resource_templates()
    uris = {str(t.uri_template) for t in templates}
    assert "gchat://spaces/{space_id}" in uris


def test_server_identity(settings: Settings) -> None:
    mcp = build_app(settings)
    assert mcp.name == "google-chat-mcp"
    # Version comes from the installed package metadata; assert it resolves,
    # not a specific value (pyproject bumps shouldn't break this test).
    assert isinstance(mcp.version, str)
    assert len(mcp.version) > 0


@pytest.mark.asyncio
async def test_tool_annotations_match_mcp_alignment(settings: Settings) -> None:
    """Per MCP spec 2025-06-18: annotations drive client UI auto-approve decisions."""
    mcp = build_app(settings)
    tools = await mcp.list_tools()
    by_name = {t.name: t for t in tools}

    # Read-only tools: list_spaces, get_messages, get_space, list_members.
    for name in ("list_spaces", "get_messages", "get_space", "list_members"):
        ann = by_name[name].annotations
        assert ann is not None, f"{name} missing annotations"
        assert ann.readOnlyHint is True, f"{name} should be readOnlyHint=True"
        assert ann.openWorldHint is True, f"{name} should be openWorldHint=True"

    # find_direct_message — NOT read-only (create-on-miss side effect).
    fdm = by_name["find_direct_message"].annotations
    assert fdm is not None
    assert fdm.readOnlyHint is False, "find_direct_message creates DMs, not read-only"
    assert fdm.idempotentHint is True

    # send_message — writes, not destructive, not idempotent.
    sm = by_name["send_message"].annotations
    assert sm is not None
    assert sm.readOnlyHint is False
    assert sm.destructiveHint is False
    assert sm.idempotentHint is False


@pytest.mark.asyncio
async def test_stdio_mode_skips_http_routes(settings: Settings) -> None:
    """auth=None → no HTTP custom routes registered. stdio has no HTTP surface."""
    mcp = build_app(settings)  # no auth
    # FastMCP exposes custom routes via an internal collection; list via the
    # helper that feeds _Starlette setup.
    routes = mcp._get_additional_http_routes()
    route_paths = {getattr(r, "path", None) for r in routes}
    assert "/healthz" not in route_paths
    assert "/readyz" not in route_paths
    assert "/metrics" not in route_paths
