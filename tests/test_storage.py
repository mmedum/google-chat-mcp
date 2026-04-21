"""SQLite persistence: audit log + directory cache."""

from __future__ import annotations

import asyncio

import pytest
from src.storage import Database, DirectoryCache, prune_audit_log, write_audit_row


@pytest.mark.asyncio
async def test_migrate_creates_tables(db: Database) -> None:
    async with db.cursor() as conn:
        cur = await conn.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
        rows = await cur.fetchall()
    names = {r["name"] for r in rows}
    assert "audit_log" in names
    assert "user_directory" in names


@pytest.mark.asyncio
async def test_audit_log_round_trip(db: Database) -> None:
    await write_audit_row(
        db,
        user_sub="sub-1",
        tool_name="list_spaces",
        success=True,
        latency_ms=42,
    )
    async with db.cursor() as conn:
        cur = await conn.execute("SELECT * FROM audit_log")
        row = await cur.fetchone()
    assert row is not None
    assert row["tool_name"] == "list_spaces"
    assert row["latency_ms"] == 42
    assert row["success"] == 1


@pytest.mark.asyncio
async def test_prune_audit_log_removes_old(db: Database) -> None:
    await write_audit_row(db, user_sub="s", tool_name="x", success=True, latency_ms=1)
    # Force-age the row.
    async with db.cursor() as conn:
        await conn.execute("UPDATE audit_log SET timestamp = '2000-01-01 00:00:00'")
    removed = await prune_audit_log(db, retention_days=30)
    assert removed == 1


@pytest.mark.asyncio
async def test_directory_cache_put_get(db: Database) -> None:
    cache = DirectoryCache(db, ttl_seconds=3600)
    await cache.put("users/111", "alice@example.com", "Alice")
    hit = await cache.get("users/111")
    assert hit == ("alice@example.com", "Alice")


@pytest.mark.asyncio
async def test_directory_cache_honors_ttl(db: Database) -> None:
    cache = DirectoryCache(db, ttl_seconds=0)
    await cache.put("users/222", "bob@example.com", "Bob")
    await asyncio.sleep(0.01)
    assert await cache.get("users/222") is None


@pytest.mark.asyncio
async def test_directory_cache_upsert_updates(db: Database) -> None:
    cache = DirectoryCache(db, ttl_seconds=3600)
    await cache.put("users/333", "old@example.com", "Old")
    await cache.put("users/333", "new@example.com", "New")
    hit = await cache.get("users/333")
    assert hit == ("new@example.com", "New")


@pytest.mark.asyncio
async def test_directory_cache_put_silently_drops_non_workspace_ids(
    db: Database,
) -> None:
    """Regression: the workspace_user_id gate must hold for both put paths
    (single + bulk). Bot/app/contact-derived IDs that aren't `users/{numeric}`
    are silently dropped to prevent cache poisoning."""
    cache = DirectoryCache(db, ttl_seconds=3600)
    for non_workspace in ("users/c1234", "users/bot-name", "users/app", "people/123"):
        await cache.put(non_workspace, "bad@example.com", "Bad")
        assert await cache.get(non_workspace) is None
