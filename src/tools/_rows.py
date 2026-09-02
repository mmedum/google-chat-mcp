"""Per-row validation shared by the section listings."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from ..observability import logger, mcp_schema_drift_total
from ._common import drift_fields


def _parse_rows[T](
    raw: list[dict[str, Any]], build: Callable[[dict[str, Any]], T], *, kind: str
) -> tuple[list[T], int]:
    """Validate rows one at a time, skipping and counting the ones that fail.

    Validating inline in a comprehension made a single unparseable row fail the
    entire page — `invoke_tool` turns the ValidationError into
    `ToolError("Internal error.")` for the whole listing. That is the
    total-outage shape `docs/architecture.md` says must not happen, and the one
    that has already cost this project two outages. Skipping is right; skipping
    silently is not, so the caller gets a count and the operator gets a log.
    """
    rows: list[T] = []
    unparsed = 0
    for row in raw:
        try:
            rows.append(build(row))
        except (TypeError, ValueError) as exc:
            if unparsed == 0:
                logger.warning(
                    f"{kind}_unparsed", error=type(exc).__name__, fields=drift_fields(exc)
                )
                mcp_schema_drift_total.labels(kind).inc()
            unparsed += 1
    return rows, unparsed
