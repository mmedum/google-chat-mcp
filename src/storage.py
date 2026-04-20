"""SQLite persistence for audit_log + user_directory (email cache).

The OAuth token store is separate — it lives in a disk-backed py-key-value
store wrapped in Fernet encryption, managed entirely by FastMCP's GoogleProvider.
"""

from __future__ import annotations

import re
from collections.abc import AsyncIterator, Iterable
from contextlib import asynccontextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path

import aiosqlite

# Workspace profile IDs are numeric — the same namespace as Chat's users/{id}.
# Contact IDs from searchContacts are "people/c{hex}" which DO NOT round-trip
# to users/{id}; writing them would poison the cache for later sender lookups.
# This gate filters resourceName before any cache write.
_WORKSPACE_PERSON_ID = re.compile(r"^people/(\d+)$")


def workspace_user_id(resource_name: str) -> str | None:
    """Translate `people/{numeric}` → `users/{numeric}` or return None.

    The numeric Workspace profile ID is the one resource shape that shares
    a namespace with Chat's `sender.name = users/{id}`. `people/c{hex}`
    contact IDs do NOT round-trip — they belong to the caller's personal
    contact list, not Chat. Callers use this helper both to decide
    whether to surface a `user_id` on search results and to gate cache
    writes; the SQL cache enforces it again on write as a belt-and-suspenders.
    """
    match = _WORKSPACE_PERSON_ID.match(resource_name)
    if match is None:
        return None
    return f"users/{match.group(1)}"


_MIGRATIONS_DIR = Path(__file__).resolve().parent / "migrations"


class Database:
    """Thin async wrapper. One connection per request is fine with WAL."""

    def __init__(self, path: Path) -> None:
        self._path = path

    async def connect(self) -> aiosqlite.Connection:
        conn = await aiosqlite.connect(self._path)
        conn.row_factory = aiosqlite.Row
        await conn.execute("PRAGMA foreign_keys = ON;")
        # Wait up to 5s on a write lock before failing, so parallel audit writes
        # don't crash each other with "database is locked".
        await conn.execute("PRAGMA busy_timeout = 5000;")
        return conn

    @asynccontextmanager
    async def cursor(self) -> AsyncIterator[aiosqlite.Connection]:
        conn = await self.connect()
        try:
            yield conn
            await conn.commit()
        finally:
            await conn.close()

    async def migrate(self) -> None:
        """Apply every `.sql` file in migrations/ in lexical order."""
        self._path.parent.mkdir(parents=True, exist_ok=True)
        sql_files = sorted(_MIGRATIONS_DIR.glob("*.sql"))
        if not sql_files:
            raise RuntimeError(f"No migrations found at {_MIGRATIONS_DIR}")
        async with self.cursor() as conn:
            for path in sql_files:
                await conn.executescript(path.read_text())


# ---------- audit log ----------


async def write_audit_row(
    db: Database,
    *,
    user_sub: str,
    tool_name: str,
    success: bool,
    latency_ms: int,
    target_space_id: str | None = None,
    error_code: str | None = None,
) -> None:
    async with db.cursor() as conn:
        await conn.execute(
            """
            INSERT INTO audit_log
                (user_sub, tool_name, target_space_id, success, latency_ms, error_code)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            (user_sub, tool_name, target_space_id, int(success), latency_ms, error_code),
        )


async def prune_audit_log(db: Database, retention_days: int) -> int:
    """Delete rows older than retention. Returns deleted count."""
    cutoff = datetime.now(UTC) - timedelta(days=retention_days)
    async with db.cursor() as conn:
        cur = await conn.execute(
            "DELETE FROM audit_log WHERE timestamp < ?",
            (cutoff.isoformat(),),
        )
        return cur.rowcount


# ---------- user directory (People API email cache) ----------


class DirectoryCache:
    """id (`users/{id}`) -> email + display_name, with TTL-based refresh."""

    def __init__(self, db: Database, ttl_seconds: int) -> None:
        self._db = db
        self._ttl = timedelta(seconds=ttl_seconds)

    async def get(self, user_id: str) -> tuple[str, str | None] | None:
        """Return (email, display_name) if cached and fresh, else None."""
        async with self._db.cursor() as conn:
            cur = await conn.execute(
                "SELECT email, display_name, fetched_at FROM user_directory WHERE user_id = ?",
                (user_id,),
            )
            row = await cur.fetchone()
        if row is None:
            return None
        fetched_at = _parse_sqlite_ts(row["fetched_at"])
        if datetime.now(UTC) - fetched_at > self._ttl:
            return None
        return row["email"], row["display_name"]

    async def put(self, user_id: str, email: str, display_name: str | None) -> None:
        async with self._db.cursor() as conn:
            await conn.execute(
                """
                INSERT INTO user_directory (user_id, email, display_name, fetched_at)
                VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                ON CONFLICT(user_id) DO UPDATE SET
                    email = excluded.email,
                    display_name = excluded.display_name,
                    fetched_at = CURRENT_TIMESTAMP
                """,
                (user_id, email, display_name),
            )

    async def put_many(self, entries: Iterable[tuple[str, str, str | None]]) -> int:
        """Bulk-write a list of `(resource_name, email, display_name)` tuples.

        Gates each `resource_name` through `^people/\\d+$` — only Workspace
        profile IDs round-trip to Chat's `users/{id}` namespace. Contact IDs
        (`people/c{hex}`) from searchContacts are skipped; writing them would
        cause a later `sender.name = users/{id}` lookup to miss (or worse,
        match the wrong identity). Returns the count of rows actually written.
        """
        rows: list[tuple[str, str, str | None]] = []
        for resource_name, email, display_name in entries:
            user_id = workspace_user_id(resource_name)
            if user_id is None:
                continue
            rows.append((user_id, email, display_name))
        if not rows:
            return 0
        async with self._db.cursor() as conn:
            await conn.executemany(
                """
                INSERT INTO user_directory (user_id, email, display_name, fetched_at)
                VALUES (?, ?, ?, CURRENT_TIMESTAMP)
                ON CONFLICT(user_id) DO UPDATE SET
                    email = excluded.email,
                    display_name = excluded.display_name,
                    fetched_at = CURRENT_TIMESTAMP
                """,
                rows,
            )
        return len(rows)


def _parse_sqlite_ts(raw: str) -> datetime:
    # SQLite's CURRENT_TIMESTAMP emits "YYYY-MM-DD HH:MM:SS" without tz.
    return datetime.fromisoformat(raw.replace(" ", "T")).replace(tzinfo=UTC)


@asynccontextmanager
async def lifespan_database(path: Path) -> AsyncIterator[Database]:
    db = Database(path)
    await db.migrate()
    try:
        yield db
    finally:
        pass  # aiosqlite connections are per-cursor; nothing to close globally.


__all__ = [
    "Database",
    "DirectoryCache",
    "lifespan_database",
    "prune_audit_log",
    "workspace_user_id",
    "write_audit_row",
]
