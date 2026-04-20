"""Structured-logging redaction: sensitive keys mask to `***redacted***`."""

from __future__ import annotations

import io
import logging

import pytest
import structlog
from src.observability import _SENSITIVE_KEYS, _redact_sensitive, configure_logging


@pytest.mark.parametrize(
    "key",
    [
        "access_token",
        "refresh_token",
        "authorization",
        "client_secret",
        "fernet_key",
        "jwt_signing_key",
        "audit_pepper",
        "bearer",
        "cookie",
        "set-cookie",
        "id_token",
        "code",
        "state",
        "email",
        "user_sub",
        "sub",
    ],
)
def test_sensitive_key_is_redacted(key: str) -> None:
    event = {key: "super-secret-value", "other": "safe"}
    result = _redact_sensitive(None, "info", event)
    assert result[key] == "***redacted***"
    assert result["other"] == "safe"


def test_key_matching_is_case_insensitive() -> None:
    event = {"Authorization": "Bearer abc", "Email": "x@y.z"}
    result = _redact_sensitive(None, "info", event)
    assert result["Authorization"] == "***redacted***"
    assert result["Email"] == "***redacted***"


def test_non_sensitive_event_passes_through() -> None:
    event = {"msg": "hello", "count": 42}
    result = _redact_sensitive(None, "info", event)
    assert result == event


def test_sensitive_keys_superset_of_v1_baseline() -> None:
    # Regression guard: Step 0 widened the set. Don't accidentally shrink it.
    v1_baseline = {
        "access_token",
        "refresh_token",
        "authorization",
        "client_secret",
        "fernet_key",
        "bearer",
    }
    assert v1_baseline.issubset(_SENSITIVE_KEYS)


# ---------- stream plumbing ----------


def test_configure_logging_routes_structlog_to_given_stream() -> None:
    """Regression: structlog must write to the configured stream, not default stdout.

    The stdio transport passes `stream=sys.stderr` so MCP JSON-RPC frames on
    stdout stay uncorrupted. Before this fix, `configure_logging` only wired
    stdlib logging's stream and structlog kept writing to stdout.
    """
    buf = io.StringIO()
    try:
        configure_logging("INFO", stream=buf)
        # Rebind the cached module-level logger so it picks up the new factory.
        structlog.reset_defaults()
        configure_logging("INFO", stream=buf)
        log = structlog.get_logger("test_stream")
        log.info("hello_stream_check", marker=42)
    finally:
        structlog.reset_defaults()
        # Reset the root handler configured by logging.basicConfig so other tests
        # don't inherit the StringIO sink.
        root = logging.getLogger()
        for handler in list(root.handlers):
            root.removeHandler(handler)

    contents = buf.getvalue()
    assert "hello_stream_check" in contents
    assert '"marker": 42' in contents
