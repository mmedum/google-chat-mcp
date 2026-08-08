-- Migration 001: initial schema.
--
-- Two tables; everything else (OAuth clients, upstream refresh tokens) is stored
-- by FastMCP's GoogleProvider in the disk-backed key-value store, encrypted with Fernet.
--
-- audit_log    - every tool invocation, 90-day retention (no message content)
-- user_directory - id -> email cache from People API, 24h TTL

PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

-- Already applied everywhere. Do not edit beyond comments, and keep every
-- statement idempotent: databases created before `schema_migrations` existed
-- have no record of this file, so the runner re-applies it once against a
-- schema it already created.
--
-- CAUTION on the TIMESTAMP columns below. SQLite has no timestamp type, and
-- "TIMESTAMP" matches none of the affinity keywords, so these columns take
-- NUMERIC affinity while holding CURRENT_TIMESTAMP's text. A bound value that
-- is not a well-formed number is therefore compared *lexicographically*, which
-- is how an ISO-8601 cutoff — whose "T" sorts after the stored space —
-- silently pruned an extra day of audit history. Render bound values with
-- `storage._sqlite_ts` so both sides share one representation; that is the
-- guard, and it is required whatever the column is declared as.
CREATE TABLE IF NOT EXISTS audit_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_sub        TEXT NOT NULL,
    tool_name       TEXT NOT NULL,
    target_space_id TEXT,
    success         INTEGER NOT NULL,
    latency_ms      INTEGER NOT NULL,
    error_code      TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_user ON audit_log(user_sub);

CREATE TABLE IF NOT EXISTS user_directory (
    user_id      TEXT PRIMARY KEY,
    email        TEXT NOT NULL,
    display_name TEXT,
    fetched_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_user_directory_email ON user_directory(email);
