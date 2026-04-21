"""Structured logging (JSON stdout) + Prometheus metrics."""

from __future__ import annotations

import logging
import sys
from collections.abc import MutableMapping
from typing import Any, TextIO

import structlog
from prometheus_client import CollectorRegistry, Counter, Gauge, Histogram


def configure_logging(
    level: str = "INFO",
    *,
    stream: TextIO | None = None,
) -> None:
    """Configure structlog to emit JSON, suppressing sensitive keys.

    `stream` defaults to stdout (HTTPS transport). Stdio transport passes
    `sys.stderr` — stdout in that mode is reserved for JSON-RPC frames, and
    any non-protocol byte there corrupts the MCP stream. structlog has its
    own logger factory and does not route through stdlib logging, so both
    sinks must be pointed at `stream` for the redirection to be complete.
    """
    effective_stream = stream if stream is not None else sys.stdout
    logging.basicConfig(
        level=level.upper(),
        format="%(message)s",
        stream=effective_stream,
    )
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.processors.add_log_level,
            structlog.processors.TimeStamper(fmt="iso", utc=True),
            _redact_sensitive,
            structlog.processors.EventRenamer("event"),
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.make_filtering_bound_logger(
            getattr(logging, level.upper(), logging.INFO)
        ),
        logger_factory=structlog.PrintLoggerFactory(file=effective_stream),
        cache_logger_on_first_use=True,
    )


_SENSITIVE_KEYS = frozenset(
    {
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
    }
)


def _redact_value(value: Any) -> Any:
    """Recursively walk dicts/lists/tuples, redacting sensitive keys.

    Top-level-only redaction was a footgun: a future
    ``logger.info("upstream_response", payload=raw_dict)`` where
    ``raw_dict`` contains an ``access_token`` would log it in plaintext
    because ``payload`` isn't in the sensitive set. Walking the structure
    catches the nested case at a small cost (sensitive log events are rare).
    """
    if isinstance(value, dict):
        return {
            k: ("***redacted***" if k.lower() in _SENSITIVE_KEYS else _redact_value(v))
            for k, v in value.items()
        }
    if isinstance(value, list):
        return [_redact_value(item) for item in value]
    if isinstance(value, tuple):
        return tuple(_redact_value(item) for item in value)
    return value


def _redact_sensitive(
    _logger: Any, _name: str, event_dict: MutableMapping[str, Any]
) -> MutableMapping[str, Any]:
    for k in list(event_dict):
        if k.lower() in _SENSITIVE_KEYS:
            event_dict[k] = "***redacted***"
        else:
            event_dict[k] = _redact_value(event_dict[k])
    return event_dict


# ------- metrics -------

REGISTRY = CollectorRegistry()

mcp_tool_calls_total = Counter(
    "mcp_tool_calls_total",
    "MCP tool invocations.",
    labelnames=("tool", "status"),
    registry=REGISTRY,
)
mcp_tool_latency_seconds = Histogram(
    "mcp_tool_latency_seconds",
    "MCP tool latency.",
    labelnames=("tool",),
    registry=REGISTRY,
    buckets=(0.1, 0.25, 0.5, 1.0, 1.5, 2.5, 5.0, 10.0, 30.0),
)
mcp_google_api_calls_total = Counter(
    "mcp_google_api_calls_total",
    "Upstream calls to Google APIs.",
    labelnames=("endpoint", "status_code"),
    registry=REGISTRY,
)
mcp_google_api_latency_seconds = Histogram(
    "mcp_google_api_latency_seconds",
    "Upstream Google API latency.",
    labelnames=("endpoint",),
    registry=REGISTRY,
    buckets=(0.1, 0.25, 0.5, 1.0, 1.5, 2.5, 5.0, 10.0, 30.0),
)
mcp_rate_limit_hits_total = Counter(
    "mcp_rate_limit_hits_total",
    "Rate-limit rejections across all users.",
    registry=REGISTRY,
)
mcp_active_users = Gauge(
    "mcp_active_users",
    "Users with a request in the last 5 minutes.",
    registry=REGISTRY,
)

logger: structlog.stdlib.BoundLogger = structlog.get_logger(__name__)
