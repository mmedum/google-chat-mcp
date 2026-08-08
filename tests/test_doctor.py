"""`doctor` must exit non-zero and name the drifted field."""

from __future__ import annotations

import httpx
import pytest
import respx
from src.stdio import _run_doctor
from src.tools._common import AuthInfo


async def _resolver() -> AuthInfo:
    return AuthInfo(access_token="tok", user_sub="u")


def _space() -> dict[str, object]:
    return {"name": "spaces/AAA", "type": "SPACE", "displayName": "Team"}


def _message(extra: dict[str, object] | None = None) -> dict[str, object]:
    body: dict[str, object] = {
        "name": "spaces/AAA/messages/M.1",
        "sender": {"name": "users/1"},
        "createTime": "2026-04-19T10:00:00Z",
        "thread": {"name": "spaces/AAA/threads/T.1"},
        "text": "hi",
    }
    if extra:
        body.update(extra)
    return body


@pytest.mark.asyncio
async def test_doctor_passes_when_live_shapes_match(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GCM_CHAT_API_BASE", "https://chat.googleapis.com/v1")
    with respx.mock(base_url="https://chat.googleapis.com/v1") as mock:
        mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx.Response(200, json={"messages": [_message()]})
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, limit=5)
    assert rc == 0


@pytest.mark.asyncio
async def test_doctor_reports_drift_and_exits_nonzero(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    """The whole point: catch a new field before a user hits Internal error."""
    with respx.mock(base_url="https://chat.googleapis.com/v1") as mock:
        mock.get("/spaces").mock(return_value=httpx.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx.Response(
                200,
                json={"messages": [_message({"fieldGoogleAddsIn2027": "x"})]},
            )
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, limit=5)
    assert rc == 1
    err = capsys.readouterr().err
    assert "SCHEMA DRIFT" in err
    assert "fieldGoogleAddsIn2027" in err
    assert "src/models.py" in err
