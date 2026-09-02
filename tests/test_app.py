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
        "create_group_chat",
        "create_space",
        "add_member",
        "remove_member",
        "search_people",
        "update_message",
        "delete_message",
        "update_space",
        "list_sections",
        "list_section_items",
        "create_section",
        "rename_section",
        "delete_section",
        "position_section",
        "move_space_to_section",
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
        assert ann.read_only_hint is True, f"{name} should be read_only_hint=True"
        assert ann.open_world_hint is True, f"{name} should be open_world_hint=True"

    # find_direct_message — NOT read-only (create-on-miss side effect).
    fdm = by_name["find_direct_message"].annotations
    assert fdm is not None
    assert fdm.read_only_hint is False, "find_direct_message creates DMs, not read-only"
    assert fdm.idempotent_hint is True

    # send_message — writes, not destructive, not idempotent.
    sm = by_name["send_message"].annotations
    assert sm is not None
    assert sm.read_only_hint is False
    assert sm.destructive_hint is False
    assert sm.idempotent_hint is False

    # create_group_chat + create_space + add_member — writes, not destructive,
    # not idempotent.
    for name in ("create_group_chat", "create_space", "add_member"):
        ann = by_name[name].annotations
        assert ann is not None, f"{name} missing annotations"
        assert ann.read_only_hint is False, f"{name} should be read_only_hint=False"
        assert ann.destructive_hint is False
        assert ann.idempotent_hint is False
        assert ann.open_world_hint is True

    # remove_member + delete_message — destructive + idempotent (double-delete
    # returns removed=false / deleted=false rather than erroring).
    for name in ("remove_member", "delete_message"):
        ann = by_name[name].annotations
        assert ann is not None, f"{name} missing annotations"
        assert ann.read_only_hint is False, f"{name} should be read_only_hint=False"
        assert ann.destructive_hint is True
        assert ann.idempotent_hint is True

    # update_message — writes, not destructive (text edit is reversible by
    # re-edit), not idempotent (new text replaces previous).
    um = by_name["update_message"].annotations
    assert um is not None
    assert um.read_only_hint is False
    assert um.destructive_hint is False
    assert um.idempotent_hint is False

    # search_people — read-only.
    sp = by_name["search_people"].annotations
    assert sp is not None
    assert sp.read_only_hint is True
    assert sp.open_world_hint is True


@pytest.mark.asyncio
async def test_section_tool_annotations(settings: Settings) -> None:
    """Sections change only the caller's own sidebar — nothing here touches a space."""
    mcp = build_app(settings)
    tools = await mcp.list_tools()
    by_name = {t.name: t for t in tools}

    # Section reads.
    for name in ("list_sections", "list_section_items"):
        ann = by_name[name].annotations
        assert ann is not None, f"{name} missing annotations"
        assert ann.read_only_hint is True, f"{name} should be read_only_hint=True"
        assert ann.open_world_hint is True

    # create_section — writes, not destructive, NOT idempotent: Google does not
    # deduplicate by name, so a repeat call leaves two sections.
    cs = by_name["create_section"].annotations
    assert cs is not None
    assert cs.read_only_hint is False
    assert cs.destructive_hint is False
    assert cs.idempotent_hint is False

    # rename / position / move — writes that land on the same state when
    # repeated with the same arguments, and none of them destroys anything.
    for name in ("rename_section", "position_section", "move_space_to_section"):
        ann = by_name[name].annotations
        assert ann is not None, f"{name} missing annotations"
        assert ann.read_only_hint is False, f"{name} should be read_only_hint=False"
        assert ann.destructive_hint is False
        assert ann.idempotent_hint is True

    # delete_section — destructive + idempotent, same shape as delete_message.
    ds = by_name["delete_section"].annotations
    assert ds is not None
    assert ds.read_only_hint is False
    assert ds.destructive_hint is True
    assert ds.idempotent_hint is True


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
