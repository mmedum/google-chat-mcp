"""Regression tests for Chat API response models.

Schema-drift quirks we've observed from the live Google Chat API:
- `type` can arrive as the pre-GA aliases `ROOM` / `DM` / `GROUP_DM`
- `lastActiveTime` is populated on spaces.list entries
- Message responses embed a `space` object whose shape we don't consume

The `extra="forbid"` guarantee must still hold for truly unknown fields so
future drift keeps surfacing instead of being silently dropped.
"""

from __future__ import annotations

from datetime import UTC, datetime, timezone

import pytest
from pydantic import ValidationError
from src.models import GetMessagesInput, _ChatMessageResponse, _ChatSpaceResponse


@pytest.mark.parametrize(
    ("raw_type", "normalized"),
    [
        ("ROOM", "SPACE"),
        ("DM", "DIRECT_MESSAGE"),
        ("GROUP_DM", "GROUP_CHAT"),
        ("SPACE", "SPACE"),
        ("DIRECT_MESSAGE", "DIRECT_MESSAGE"),
        ("GROUP_CHAT", "GROUP_CHAT"),
    ],
)
def test_space_response_normalizes_legacy_type_aliases(raw_type: str, normalized: str) -> None:
    model = _ChatSpaceResponse.model_validate({"name": "spaces/AAA", "type": raw_type})
    assert model.type_ == normalized


def test_space_response_prefers_space_type_over_deprecated_type() -> None:
    # Google often returns `type: "ROOM"` as a catch-all while the real
    # classification lives in `spaceType`. The canonical field wins.
    model = _ChatSpaceResponse.model_validate(
        {"name": "spaces/DM1", "type": "ROOM", "spaceType": "DIRECT_MESSAGE"}
    )
    assert model.type_ == "DIRECT_MESSAGE"


def test_space_response_falls_back_to_legacy_type_when_space_type_missing() -> None:
    # Older response shapes omit `spaceType` entirely; the legacy alias map
    # still covers them.
    model = _ChatSpaceResponse.model_validate({"name": "spaces/DM1", "type": "DM"})
    assert model.type_ == "DIRECT_MESSAGE"


def test_space_response_accepts_last_active_time() -> None:
    model = _ChatSpaceResponse.model_validate(
        {
            "name": "spaces/AAA",
            "type": "SPACE",
            "lastActiveTime": "2026-04-19T17:23:06.764415Z",
        }
    )
    assert model.last_active_time is not None
    assert model.last_active_time.year == 2026


def test_space_response_still_rejects_unknown_fields() -> None:
    with pytest.raises(ValidationError, match="extra_forbidden"):
        _ChatSpaceResponse.model_validate(
            {"name": "spaces/AAA", "type": "SPACE", "somethingNew": "foo"}
        )


@pytest.mark.parametrize(
    ("raw", "expected_tz"),
    [
        # Naive string (no TZ) — LLMs commonly send this shape.
        ("2026-04-18T00:00:00.000000", UTC),
        # Z-suffixed UTC string.
        ("2026-04-18T00:00:00Z", UTC),
        # Explicit offset.
        (
            "2026-04-18T00:00:00+02:00",
            timezone(datetime.now().astimezone().utcoffset() or UTC.utcoffset(datetime.now())),
        ),
    ],
)
def test_get_messages_input_parses_iso_since(raw: str, expected_tz: object) -> None:
    model = GetMessagesInput.model_validate({"space_id": "spaces/AAA", "since": raw})
    assert model.since is not None
    # Every parsed `since` carries tzinfo — naive strings are promoted to UTC.
    assert model.since.tzinfo is not None


def test_get_messages_input_accepts_datetime_instance() -> None:
    dt = datetime(2026, 4, 18, 0, 0, tzinfo=UTC)
    model = GetMessagesInput.model_validate({"space_id": "spaces/AAA", "since": dt})
    assert model.since == dt


def test_get_messages_input_rejects_non_iso_since() -> None:
    with pytest.raises(ValidationError, match="ISO-8601"):
        GetMessagesInput.model_validate({"space_id": "spaces/AAA", "since": "last week"})


_TRAVERSAL_SEGMENTS = ("..", ".", "...", "....", ".-_")


@pytest.mark.parametrize("seg", _TRAVERSAL_SEGMENTS)
def test_resource_name_regex_rejects_dot_only_segments(seg: str) -> None:
    """Regression for the path-traversal vector: bare dot/dash/underscore
    segments would pass the old `[A-Za-z0-9._-]+` regex and let httpx
    normalize the URL via RFC 3986 §5.2.4, rewriting the upstream target.

    The tightened regex requires at least one alphanumeric character per
    segment so `..`, `.`, `...` etc. are rejected at the Pydantic boundary
    before they ever reach `chat_client._request`.
    """
    from src.models import (
        DeleteMessageInput,
        GetThreadInput,
        ListMembersInput,
        RemoveMemberInput,
        UpdateMessageInput,
    )

    with pytest.raises(ValidationError):
        UpdateMessageInput(message_name=f"spaces/AAA/messages/{seg}", text="x")
    with pytest.raises(ValidationError):
        DeleteMessageInput(message_name=f"spaces/{seg}/messages/M.1")
    with pytest.raises(ValidationError):
        RemoveMemberInput(membership_name=f"spaces/AAA/members/{seg}")
    with pytest.raises(ValidationError):
        ListMembersInput(space_id=f"spaces/{seg}")
    with pytest.raises(ValidationError):
        GetThreadInput(space_id="spaces/AAA", thread_name=f"spaces/AAA/threads/{seg}")


def test_resource_name_regex_still_accepts_real_ids() -> None:
    """Sanity: tightening the regex must not break legitimate Chat IDs that
    contain dots/dashes/underscores (e.g. `M.fFwlJ6Q-v8`)."""
    from src.models import DeleteMessageInput, RemoveMemberInput

    DeleteMessageInput(message_name="spaces/AAA/messages/MfFwlJ6Q-v8.MfFwlJ6Q-v8")
    RemoveMemberInput(membership_name="spaces/AAQAcRvOo10/members/103509578229692690681")


def test_message_response_accepts_embedded_space_dict() -> None:
    model = _ChatMessageResponse.model_validate(
        {
            "name": "spaces/AAA/messages/111",
            "sender": {"name": "users/222"},
            "createTime": "2026-04-19T17:00:00Z",
            "thread": {"name": "spaces/AAA/threads/333"},
            "space": {"name": "spaces/AAA", "type": "SPACE", "any": "field"},
        }
    )
    assert model.space == {"name": "spaces/AAA", "type": "SPACE", "any": "field"}
