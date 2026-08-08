"""SQLite persistence: audit log + directory cache."""

from __future__ import annotations

import asyncio
from datetime import UTC, datetime, timedelta
from pathlib import Path

import aiosqlite
import pytest
from src.storage import (
    _MIGRATIONS_DIR,
    Database,
    DirectoryCache,
    prune_audit_log,
    write_audit_row,
)


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
async def test_prune_audit_log_keeps_rows_inside_retention(db: Database) -> None:
    """Rows newer than the cutoff survive, including ones sharing its calendar date.

    The boundary is the whole test. `CURRENT_TIMESTAMP` stores
    `YYYY-MM-DD HH:MM:SS`; binding the cutoff as `datetime.isoformat()` made
    the comparison lexicographic against a `T` separator, which sorts after
    the stored space — so every row dated the same day as the cutoff compared
    as older and was deleted, discarding up to an extra 24h of audit history.
    A far-past fixture (the test above) cannot catch that; only a row within
    hours of the boundary can.
    """
    now = datetime.now(UTC)
    rows = {
        "well_inside": now - timedelta(hours=1),
        # Newer than a 7-day cutoff by an hour, but on the same calendar date.
        "just_inside": now - timedelta(days=7) + timedelta(hours=1),
        # Older than the cutoff by an hour, same calendar date.
        "just_outside": now - timedelta(days=7) - timedelta(hours=1),
        "well_outside": now - timedelta(days=30),
    }
    async with db.cursor() as conn:
        for name, ts in rows.items():
            await conn.execute(
                "INSERT INTO audit_log "
                "(timestamp, user_sub, tool_name, success, latency_ms) VALUES (?,?,?,1,1)",
                (ts.strftime("%Y-%m-%d %H:%M:%S"), "s", name),
            )

    removed = await prune_audit_log(db, retention_days=7)

    async with db.cursor() as conn:
        cur = await conn.execute("SELECT tool_name FROM audit_log ORDER BY timestamp")
        survivors = [r["tool_name"] for r in await cur.fetchall()]
    assert survivors == ["just_inside", "well_inside"]
    assert removed == 2


@pytest.mark.asyncio
async def test_prune_audit_log_matches_written_row_format(db: Database) -> None:
    """A row written right now must not be prunable under any positive retention."""
    await write_audit_row(db, user_sub="s", tool_name="fresh", success=True, latency_ms=1)
    assert await prune_audit_log(db, retention_days=1) == 0
    async with db.cursor() as conn:
        cur = await conn.execute("SELECT COUNT(*) AS n FROM audit_log")
        row = await cur.fetchone()
    assert row is not None
    assert row["n"] == 1


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


# ---------- migrations ----------

# The real 001, not a hand-copy: a replica silently drifts from what shipped
# (the first version of this fixture was already missing 001's three indexes),
# and 001 is frozen, so it is a stable fixture.
_LEGACY_SCHEMA = (_MIGRATIONS_DIR / "001_init.sql").read_text(encoding="utf-8")


@pytest.mark.asyncio
async def test_migrate_records_applied_files_and_skips_them(db: Database) -> None:
    """A migration must run once, not on every startup."""
    async with db.cursor() as conn:
        cur = await conn.execute("SELECT filename FROM schema_migrations ORDER BY filename")
        applied = [r["filename"] for r in await cur.fetchall()]
    assert applied == ["001_init.sql"]

    # Re-running must be a no-op, not a second rebuild.
    await write_audit_row(db, user_sub="s", tool_name="x", success=True, latency_ms=1)
    await db.migrate()
    async with db.cursor() as conn:
        cur = await conn.execute("SELECT COUNT(*) AS n FROM audit_log")
        row = await cur.fetchone()
    assert row is not None
    assert row["n"] == 1


@pytest.mark.asyncio
async def test_migrate_upgrades_a_pre_versioning_database(tmp_path: Path) -> None:
    """A database from before `schema_migrations` must migrate without losing data.

    It holds real audit history and an email cache and has no record of `001`,
    so the runner re-applies `001` once. That is only safe because every
    statement in it is guarded by `IF NOT EXISTS` — this test is what pins that.
    """
    path = tmp_path / "legacy.sqlite"
    async with aiosqlite.connect(path) as conn:
        await conn.executescript(_LEGACY_SCHEMA)
        await conn.execute(
            "INSERT INTO audit_log (id, timestamp, user_sub, tool_name, success, latency_ms) "
            "VALUES (7, '2026-08-01 10:00:00', 'sub-1', 'send_message', 1, 42)",
        )
        await conn.execute(
            "INSERT INTO user_directory (user_id, email, display_name, fetched_at) "
            "VALUES ('users/111', 'alice@example.com', 'Alice', '2026-08-01 10:00:00')",
        )
        await conn.commit()

    db = Database(path)
    await db.migrate()

    async with db.cursor() as conn:
        cur = await conn.execute("SELECT * FROM audit_log")
        rows = list(await cur.fetchall())
        assert len(rows) == 1
        assert rows[0]["id"] == 7
        assert rows[0]["tool_name"] == "send_message"
        assert rows[0]["timestamp"] == "2026-08-01 10:00:00"
        assert rows[0]["latency_ms"] == 42

        cur = await conn.execute("SELECT * FROM user_directory")
        cached = list(await cur.fetchall())
        assert len(cached) == 1
        assert cached[0]["email"] == "alice@example.com"

    # The re-applied 001 must not have clobbered the sequence.
    await write_audit_row(db, user_sub="s", tool_name="whoami", success=True, latency_ms=1)
    async with db.cursor() as conn:
        cur = await conn.execute("SELECT MAX(id) AS m FROM audit_log")
        row = await cur.fetchone()
    assert row is not None
    assert row["m"] > 7


@pytest.mark.asyncio
async def test_get_many_returns_only_fresh_entries(db: Database) -> None:
    """Batched read must apply the same TTL as the per-row one."""
    fresh = DirectoryCache(db, ttl_seconds=3600)
    await fresh.put("users/1", "one@example.com", "One")
    await fresh.put("users/2", "two@example.com", None)

    hits = await fresh.get_many(["users/1", "users/2", "users/404"])
    assert hits == {
        "users/1": ("one@example.com", "One"),
        "users/2": ("two@example.com", None),
    }, "misses are absent, not None-valued"

    stale = DirectoryCache(db, ttl_seconds=0)
    await asyncio.sleep(0.01)
    assert await stale.get_many(["users/1", "users/2"]) == {}


@pytest.mark.asyncio
async def test_get_many_handles_empty_and_duplicate_ids(db: Database) -> None:
    cache = DirectoryCache(db, ttl_seconds=3600)
    await cache.put("users/1", "one@example.com", "One")
    assert await cache.get_many([]) == {}
    assert await cache.get_many(["users/1", "users/1"]) == {"users/1": ("one@example.com", "One")}


@pytest.mark.asyncio
async def test_a_non_idempotent_migration_runs_exactly_once(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The property the runner exists to enable, which nothing else pins.

    `001` is all `IF NOT EXISTS`, so re-running it is a no-op with or without
    the skip guard — deleting the guard leaves the rest of this file green. Only
    a migration that *transforms* data can tell the difference, which is exactly
    the kind the runner was added to make expressible.
    """
    migrations = tmp_path / "migrations"
    migrations.mkdir()
    (migrations / "001_init.sql").write_text(
        (_MIGRATIONS_DIR / "001_init.sql").read_text(encoding="utf-8"), encoding="utf-8"
    )
    (migrations / "002_seed.sql").write_text(
        "INSERT INTO audit_log (user_sub, tool_name, success, latency_ms) "
        "VALUES ('seed', 'seeded', 1, 1);",
        encoding="utf-8",
    )
    monkeypatch.setattr("src.storage._MIGRATIONS_DIR", migrations)

    db = Database(tmp_path / "once.sqlite")
    await db.migrate()
    await db.migrate()
    await db.migrate()

    async with db.cursor() as conn:
        cur = await conn.execute("SELECT COUNT(*) AS n FROM audit_log WHERE tool_name = 'seeded'")
        row = await cur.fetchone()
    assert row is not None
    assert row["n"] == 1, "a non-idempotent migration must not re-apply on later startups"
