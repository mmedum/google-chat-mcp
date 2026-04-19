"""SQLite persistence for audit_log + user_directory (email cache).

The OAuth token store is separate — it lives in a disk-backed py-key-value
store wrapped in Fernet encryption, managed entirely by FastMCP's OAuthProxy.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path

import aiosqlite

_MIGRATIONS_DIR = Path(__file__).resolve().parent.parent / "migrations"


class Database:
    """Thin async wrapper. One connection per request is fine with WAL."""

    def __init__(self, path: Path) -> None:
        self._path = path

    async def connect(self) -> aiosqlite.Connection:
        conn = await aiosqlite.connect(self._path)
        conn.row_factory = aiosqlite.Row
        await conn.execute("PRAGMA foreign_keys = ON;")
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
    "write_audit_row",
]
