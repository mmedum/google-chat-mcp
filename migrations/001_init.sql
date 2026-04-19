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
