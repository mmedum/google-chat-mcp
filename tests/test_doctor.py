"""`doctor` must exit non-zero and name the drifted field."""

from __future__ import annotations

import httpx2
import pytest
from pydantic import ValidationError
from src.config import Settings
from src.stdio import _run_doctor
from src.tools._common import AuthInfo

from ._httpx2_mock import MockRouter, mock_api


def _settings() -> Settings:
    """Doctor builds its client from `Settings`, which validates the upstream base.

    Built from the environment `conftest._env` already seeds, rather than a
    literal mapping: hardcoding a `jwt_signing_key` here duplicated a value the
    fixture generates and tripped the secret scanner, which is working as
    intended — a secret-shaped literal in source is exactly what it is for.
    """
    return Settings.from_env()


def _settings_with_base(chat_api_base: str, monkeypatch: pytest.MonkeyPatch) -> Settings:
    """Same construction path as `cmd_doctor`, with the upstream base overridden."""
    monkeypatch.setenv("GCM_CHAT_API_BASE", chat_api_base)
    return Settings.from_env()


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


_USERINFO_URL = "https://openidconnect.googleapis.com/v1/userinfo"


def _mock_userinfo(mock: MockRouter) -> None:
    """`doctor` now samples the OIDC payload too — `whoami` parses it."""
    mock.get(_USERINFO_URL).mock(
        return_value=httpx2.Response(
            200, json={"sub": "u", "email": "alice@example.com", "name": "Alice"}
        )
    )


@pytest.mark.asyncio
async def test_doctor_passes_when_live_shapes_match(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GCM_CHAT_API_BASE", "https://chat.googleapis.com/v1")
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(200, json={"messages": [_message()]})
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)
    assert rc == 0


@pytest.mark.asyncio
async def test_doctor_reports_drift_and_exits_nonzero(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    """The whole point: catch a new field before a user hits Internal error."""
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(
                200,
                json={"messages": [_message({"fieldGoogleAddsIn2027": "x"})]},
            )
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)
    assert rc == 1
    err = capsys.readouterr().err
    assert "SCHEMA DRIFT" in err
    assert "unmodelled" in err
    assert "fieldGoogleAddsIn2027" in err
    assert "src/models.py" in err


@pytest.mark.asyncio
async def test_doctor_reports_a_field_that_changed_shape() -> None:
    """The hard-failure arm: a field we READ changing type still has to be caught.

    Unknown keys are absorbed now, so this branch is the only thing covering
    drift that actually breaks a tool.
    """
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(
                200, json={"messages": [_message({"createTime": "not-a-timestamp"})]}
            )
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)
    assert rc == 1


@pytest.mark.asyncio
async def test_doctor_sees_drift_on_nested_models(
    capsys: pytest.CaptureFixture[str],
) -> None:
    """`model_extra` is top-level only.

    Checking just that made doctor print "OK" while the runtime validator was
    logging drift on `sender` — a false clean bill of health from the one tool
    whose job is catching drift before a user does.
    """
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "messages": [_message({"sender": {"name": "users/1", "newSenderField": "x"}})]
                },
            )
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)
    assert rc == 1
    assert "sender.newSenderField" in capsys.readouterr().err


@pytest.mark.asyncio
async def test_doctor_checks_reaction_shapes(capsys: pytest.CaptureFixture[str]) -> None:
    """Reactions are parsed by `list_reactions` / `add_reaction`, so drift there
    breaks tools too. `doctor` sampled only spaces, messages and memberships,
    which let it report a clean bill of health for a shape it never looked at."""
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "messages": [
                        _message({"emojiReactionSummaries": [{"emoji": {"unicode": "👍"}}]})
                    ]
                },
            )
        )
        mock.get("/spaces/AAA/messages/M.1/reactions").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "reactions": [
                        {
                            "name": "spaces/AAA/messages/M.1/reactions/R.1",
                            "user": {"name": "users/1"},
                            "emoji": {"unicode": "👍"},
                            "reactionFieldAddedLater": "x",
                        }
                    ]
                },
            )
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)

    assert rc == 1
    err = capsys.readouterr().err
    assert "reactionFieldAddedLater" in err


@pytest.mark.asyncio
async def test_doctor_checks_userinfo_shape(capsys: pytest.CaptureFixture[str]) -> None:
    """`whoami` parses the OIDC payload; drift there was previously unsampled."""
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        mock.get(_USERINFO_URL).mock(
            return_value=httpx2.Response(
                200, json={"sub": "u", "email": "alice@example.com", "hostedDomainAddedLater": "x"}
            )
        )
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": []}))
        rc = await _run_doctor(_resolver, _settings(), limit=5)

    assert rc == 1
    err = capsys.readouterr().err
    assert "hostedDomainAddedLater" in err


@pytest.mark.asyncio
async def test_doctor_skips_reactions_for_messages_without_any() -> None:
    """No reaction summaries on the message means no reactions.list call."""
    with mock_api(base_url="https://chat.googleapis.com/v1", assert_all_called=False) as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(200, json={"messages": [_message()]})
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        reactions = mock.get("/spaces/AAA/messages/M.1/reactions")
        rc = await _run_doctor(_resolver, _settings(), limit=5)

    assert rc == 0
    assert not reactions.called


@pytest.mark.asyncio
async def test_doctor_is_clean_on_a_realistic_userinfo_payload() -> None:
    """An alert that always fires is the same as no alert.

    Google returns `given_name`/`family_name`/`locale` for any account with the
    `profile` scope and `hd` for any Workspace account. With those unmodelled,
    `doctor` reported SCHEMA DRIFT on every run for every user, telling them to
    add fields to `models.py` that nothing reads.
    """
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        mock.get(_USERINFO_URL).mock(
            return_value=httpx2.Response(
                200,
                json={
                    "sub": "1234567890",
                    "email": "alice@example.com",
                    "email_verified": True,
                    "name": "Alice Example",
                    "given_name": "Alice",
                    "family_name": "Example",
                    "picture": "https://lh3.googleusercontent.com/a/x",
                    "locale": "en",
                    "hd": "example.com",
                },
            )
        )
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": []}))
        rc = await _run_doctor(_resolver, _settings(), limit=5)

    assert rc == 0


@pytest.mark.asyncio
async def test_doctor_reports_a_failed_fetch_instead_of_crashing(
    capsys: pytest.CaptureFixture[str],
) -> None:
    """A missing scope is a finding, not a traceback.

    `check()` guards parsing, not fetching. With userinfo sampled first, a token
    lacking `openid` used to kill the process before spaces, messages or
    memberships were looked at — same exit code as real drift, with the other
    findings discarded.
    """
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        mock.get(_USERINFO_URL).mock(
            return_value=httpx2.Response(
                403, json={"error": {"code": 403, "status": "PERMISSION_DENIED"}}
            )
        )
        mock.get("/spaces").mock(
            return_value=httpx2.Response(200, json={"spaces": [dict(_space(), brandNewField="x")]})
        )
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(200, json={"messages": []})
        )
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(200, json={"memberships": []})
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)

    # Real drift outranks the unreachable call: exit 1, and both are printed.
    assert rc == 1
    err = capsys.readouterr().err
    assert "userinfo: could not be fetched" in err
    assert "not schema drift" in err
    # The findings collected after the failure must still be reported.
    assert "brandNewField" in err


@pytest.mark.asyncio
async def test_doctor_survives_a_message_deleted_mid_run(
    capsys: pytest.CaptureFixture[str],
) -> None:
    """reactions.list can 404 on a message deleted between calls — a plausible race."""
    with mock_api(base_url="https://chat.googleapis.com/v1") as mock:
        _mock_userinfo(mock)
        mock.get("/spaces").mock(return_value=httpx2.Response(200, json={"spaces": [_space()]}))
        mock.get("/spaces/AAA/messages").mock(
            return_value=httpx2.Response(
                200,
                json={
                    "messages": [
                        _message({"emojiReactionSummaries": [{"emoji": {"unicode": "👍"}}]})
                    ]
                },
            )
        )
        mock.get("/spaces/AAA/messages/M.1/reactions").mock(return_value=httpx2.Response(404))
        mock.get("/spaces/AAA/members").mock(
            return_value=httpx2.Response(
                200, json={"memberships": [{"name": "spaces/AAA/members/1", "state": "JOINED"}]}
            )
        )
        rc = await _run_doctor(_resolver, _settings(), limit=5)

    # 2, not 1: nothing drifted, but some shapes were never sampled, so this is
    # neither a clean bill of health nor a schema-drift report.
    assert rc == 2
    err = capsys.readouterr().err
    assert "reactions on spaces/AAA/messages/M.1: could not be fetched" in err
    assert "not schema drift" in err
    assert "SCHEMA DRIFT" not in err


def test_doctor_client_refuses_a_non_google_upstream(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """`doctor` must not send the live access token wherever an env var points.

    `Settings._restrict_upstream_base` is the control; reading the env directly
    skipped it. For a stdio server those variables live in the MCP client's
    config file, so this was reachable by anything able to edit that file.
    """
    monkeypatch.delenv("GCM_DEV_MODE", raising=False)
    with pytest.raises(ValidationError):
        _settings_with_base("http://127.0.0.1:9/v1", monkeypatch)


@pytest.mark.asyncio
async def test_doctor_reports_a_revoked_token_instead_of_a_traceback(
    capsys: pytest.CaptureFixture[str],
) -> None:
    """The likeliest doctor failure of all, and the one `fetch` cannot cover.

    Nothing has been fetched yet when the resolver runs, so a revoked or
    expired refresh token used to exit with a raw `RefreshError` traceback —
    the same exit code as real drift, and no hint to re-login.
    """

    async def _revoked() -> AuthInfo:
        raise RuntimeError("invalid_grant: Token has been expired or revoked.")

    rc = await _run_doctor(_revoked, _settings(), limit=5)

    assert rc == 2
    err = capsys.readouterr().err
    assert "CANNOT AUTHENTICATE" in err
    assert "google-chat-mcp login" in err
    assert "SCHEMA DRIFT" not in err
