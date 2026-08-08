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
# `users/{numeric}` form already in Chat's namespace — no translation needed.
# Used to gate the single-item DirectoryCache.put() so the docstring's
# belt-and-suspenders claim holds for both write paths.
_WORKSPACE_USER_ID = re.compile(r"^users/\d+$")


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

# Bootstrapped in code rather than as a migration file — it is the thing that
# records which migration files have run, so it cannot be one of them.
_SCHEMA_MIGRATIONS_DDL = """
CREATE TABLE IF NOT EXISTS schema_migrations (
    filename   TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)
"""


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
        """Apply each not-yet-applied `.sql` file in migrations/, in lexical order.

        Every file used to be re-executed on every startup, which forced each
        one to be idempotent and made anything that *transforms* data
        impossible to express. Applied filenames are recorded in
        `schema_migrations`, so a migration is skipped once it has run.

        That is a per-process check, not a distributed lock: the applied set is
        read outside any transaction and `executescript` commits as it goes, so
        two processes starting together — routine on stdio, where every MCP
        client spawns its own subprocess against the same file — can both apply
        the same migration. Harmless while every migration is replay-safe, which
        is the standing requirement below. A migration that genuinely must run
        once needs `PRAGMA user_version` checked and bumped inside its own
        `BEGIN IMMEDIATE`; add that before writing one.

        Databases created before this table existed have no record of `001`.
        That is why `001` must stay idempotent: it is re-applied once, against
        a schema it already created, and its `IF NOT EXISTS` guards make that a
        no-op.

        **Every migration must be safe to re-run from its own end state.**
        `executescript` issues an implicit COMMIT, so a file cannot share a
        transaction with the `schema_migrations` row that records it — the file
        commits first, and a crash in between replays it on the next start. A
        migration that is not replay-safe (`UPDATE x SET n = n * 2`) needs a
        different runner, not a comment. Anything that rewrites an existing
        table should also state `BEGIN IMMEDIATE`, not a deferred `BEGIN`: a
        deferred transaction takes its write lock late, and the resulting
        `SQLITE_BUSY_SNAPSHOT` is returned immediately rather than being
        retried by `busy_timeout`.
        """
        self._path.parent.mkdir(parents=True, exist_ok=True)
        sql_files = sorted(_MIGRATIONS_DIR.glob("*.sql"))
        if not sql_files:
            raise RuntimeError(f"No migrations found at {_MIGRATIONS_DIR}")
        conn = await self.connect()
        try:
            await conn.execute(_SCHEMA_MIGRATIONS_DDL)
            await conn.commit()
            cur = await conn.execute("SELECT filename FROM schema_migrations")
            applied = {row["filename"] for row in await cur.fetchall()}
            for path in sql_files:
                if path.name in applied:
                    continue
                # `executescript` performs no transaction control of its own,
                # so a migration needing atomicity states BEGIN/COMMIT itself.
                # Explicit encoding: the default follows the process locale, and
                # a C-locale container would fail to decode a non-ASCII comment.
                await conn.executescript(path.read_text(encoding="utf-8"))
                await conn.execute(
                    "INSERT OR REPLACE INTO schema_migrations (filename) VALUES (?)",
                    (path.name,),
                )
                await conn.commit()
        finally:
            await conn.close()


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
            (_sqlite_ts(cutoff),),
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

    async def get_many(self, user_ids: Iterable[str]) -> dict[str, tuple[str, str | None]]:
        """Fresh cache entries for `user_ids`, in one query. Misses are absent.

        `get()` opens its own connection, and callers resolve a page of senders
        or members concurrently — so the per-row form cost one `aiosqlite`
        connection, and one OS thread, per row. At the 200-member cap that is
        200 of each to read rows already sitting in one file. Batching turns
        the SQLite half of a fan-out into a single round-trip.
        """
        ids = list(dict.fromkeys(user_ids))
        if not ids:
            return {}
        placeholders = ",".join("?" * len(ids))
        async with self._db.cursor() as conn:
            cur = await conn.execute(
                "SELECT user_id, email, display_name, fetched_at "  # noqa: S608 — placeholders only
                f"FROM user_directory WHERE user_id IN ({placeholders})",
                ids,
            )
            rows = list(await cur.fetchall())
        now = datetime.now(UTC)
        return {
            row["user_id"]: (row["email"], row["display_name"])
            for row in rows
            if now - _parse_sqlite_ts(row["fetched_at"]) <= self._ttl
        }

    async def put(self, user_id: str, email: str, display_name: str | None) -> None:
        # Gate: only `users/{numeric}` is a Workspace profile — non-conforming
        # IDs (bots, apps, contact-derived shapes) are silently dropped to
        # match the put_many invariant. Without this, a future caller wired
        # to put() without going through workspace_user_id could poison the
        # cache for later sender-name lookups.
        if not _WORKSPACE_USER_ID.match(user_id):
            return
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

    async def put_many_users(self, entries: Iterable[tuple[str, str, str | None]]) -> int:
        """Bulk-write `(users/{id}, email, display_name)` in one connection.

        `put_many` takes People `resourceName`s; this takes ids already in
        Chat's namespace, which is what resolution produces. Same `users/{n}`
        gate as `put`. Batched because the cold path resolves a whole page
        concurrently, and a per-row write opened one connection — and one OS
        thread — per row, all contending for SQLite's single write lock.
        """
        rows = [
            (user_id, email, display_name)
            for user_id, email, display_name in entries
            if _WORKSPACE_USER_ID.match(user_id)
        ]
        return await self._write(rows)

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
        return await self._write(rows)

    async def _write(self, rows: list[tuple[str, str, str | None]]) -> int:
        """Upsert pre-gated `(users/{id}, email, display_name)` rows. Returns the count."""
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


_SQLITE_TS_FORMAT = "%Y-%m-%d %H:%M:%S"


def _sqlite_ts(value: datetime) -> str:
    """Render a UTC datetime the way SQLite's `CURRENT_TIMESTAMP` does.

    `timestamp` columns hold `CURRENT_TIMESTAMP`'s output — `YYYY-MM-DD
    HH:MM:SS`, UTC, space-separated, no offset — and `TIMESTAMP` carries
    NUMERIC affinity, so a bound string that isn't a number is compared
    lexicographically against it. `datetime.isoformat()` does NOT sort
    compatibly with that: its `T` (0x54) sorts *after* the stored space
    (0x20), so every row sharing the cutoff's date compared as older and was
    deleted regardless of time-of-day — silently discarding up to a further
    24h of audit history on each prune. Format the bound value to match the
    stored one instead. Keeping both sides plain text also keeps
    `idx_audit_log_timestamp` usable, which wrapping either side in
    `datetime()` would not.

    Naive input is rejected rather than assumed: `astimezone` would read it as
    *host-local* time, shifting the cutoff by the deployment's UTC offset and
    silently deleting or retaining up to a further half-day of history — the
    same class of quiet, timezone-shaped error this function exists to fix.
    """
    if value.tzinfo is None:
        raise ValueError("_sqlite_ts requires an aware datetime; naive input is ambiguous")
    return value.astimezone(UTC).strftime(_SQLITE_TS_FORMAT)


def _parse_sqlite_ts(raw: str) -> datetime:
    # Inverse of `_sqlite_ts`; `_SQLITE_TS_FORMAT` is the authority on the
    # layout. Parsed via `fromisoformat` rather than `strptime` so a value with
    # fractional seconds still reads — `CURRENT_TIMESTAMP` never emits one, but
    # a hand-written row or a future writer might.
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
