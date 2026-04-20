"""Shared helpers for resource handlers — URI-prefix normalization."""

from __future__ import annotations


def ensure_space_name(space_id: str) -> str:
    """Return `spaces/{id}`, accepting either the bare ID or the full name."""
    return space_id if space_id.startswith("spaces/") else f"spaces/{space_id}"


def ensure_child_name(space_id: str, child_id: str, child_kind: str) -> str:
    """Return `spaces/{s}/{kind}/{id}`, accepting either bare IDs or full names.

    `child_kind` is "messages" or "threads".
    """
    parent = ensure_space_name(space_id)
    prefix = f"{parent}/{child_kind}/"
    return child_id if child_id.startswith(prefix) else f"{prefix}{child_id}"
