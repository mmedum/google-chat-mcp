"""stdio CLI — login flow, logout, stdout hygiene, token store, identity parsing."""

from __future__ import annotations

import json
import stat
import subprocess
import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from cryptography.fernet import Fernet
from src import stdio as stdio_mod

# ---------- fixtures ----------


@pytest.fixture
def stdio_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Isolate each test: config dir under tmp_path, no bleed from real ~/.config."""
    d = tmp_path / "gcm"
    monkeypatch.setenv("GCM_CONFIG_DIR", str(d))
    monkeypatch.delenv("GCM_TOKENS_PATH", raising=False)
    monkeypatch.delenv("GCM_CLIENT_SECRET", raising=False)
    return d


@pytest.fixture
def client_secret_file(tmp_path: Path) -> Path:
    path = tmp_path / "client_secret.json"
    path.write_text(
        json.dumps(
            {
                "installed": {
                    "client_id": "test-client-id.apps.googleusercontent.com",
                    "client_secret": "TEST_SECRET",
                    "redirect_uris": ["http://127.0.0.1"],
                    "auth_uri": "https://accounts.google.com/o/oauth2/v2/auth",
                    "token_uri": "https://oauth2.googleapis.com/token",
                }
            }
        )
    )
    return path


# ---------- unit: config dir + atomic write ----------


def test_ensure_config_dir_applies_0700(stdio_home: Path) -> None:
    d = stdio_mod._ensure_config_dir()
    assert d == stdio_home
    mode = stat.S_IMODE(d.stat().st_mode)
    assert mode == 0o700


def test_atomic_write_bytes_sets_0600(tmp_path: Path) -> None:
    target = tmp_path / "secret"
    stdio_mod._atomic_write_bytes(target, b"hello")
    assert target.read_bytes() == b"hello"
    mode = stat.S_IMODE(target.stat().st_mode)
    assert mode == 0o600


def test_fernet_key_created_on_first_read_and_reused(stdio_home: Path) -> None:
    k1 = stdio_mod._load_or_create_fernet_key()
    k2 = stdio_mod._load_or_create_fernet_key()
    assert k1 == k2
    Fernet(k1)  # round-trip — valid key
    mode = stat.S_IMODE((stdio_home / "fernet.key").stat().st_mode)
    assert mode == 0o600


def test_token_store_roundtrip(stdio_home: Path) -> None:
    store = stdio_mod._open_store()
    payload = {"client_id": "X", "refresh_token": "r", "user_sub": "42"}
    store.save(payload)
    assert store.load() == payload


def test_token_store_decrypt_failure_raises_actionable_error(
    stdio_home: Path,
) -> None:
    """Corrupt tokens.json → RuntimeError naming the logout remediation."""
    tokens_path = stdio_mod._tokens_path()
    stdio_mod._load_or_create_fernet_key()  # materialize the Fernet key
    # Write garbage under the right filename.
    stdio_mod._ensure_config_dir()
    tokens_path.write_bytes(b"not-a-fernet-token")
    store = stdio_mod._open_store()
    with pytest.raises(RuntimeError, match="logout"):
        store.load()


# ---------- unit: identity parsing ----------


def test_identity_from_id_token_extracts_sub_and_email() -> None:
    token = _fake_id_token(sub="109876543210", email="alice@example.com")
    sub, email = stdio_mod._identity_from_id_token(token)
    assert sub == "109876543210"
    assert email == "alice@example.com"


def test_identity_from_id_token_tolerates_malformed_input() -> None:
    sub, email = stdio_mod._identity_from_id_token("not-a-jwt")
    assert sub is None
    assert email is None


# ---------- integration: login end-to-end with InstalledAppFlow patched ----------


def test_login_end_to_end(
    stdio_home: Path,
    client_secret_file: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """Patches `InstalledAppFlow.run_local_server` to return fake Credentials.

    The Google OAuth library handles the loopback + PKCE + browser; we don't
    re-test its internals. What matters here is that cmd_login saves the
    right shape to tokens.json and extracts identity from the id_token.
    """
    fake_credentials = MagicMock()
    fake_credentials.client_id = "test-client-id.apps.googleusercontent.com"
    fake_credentials.client_secret = "TEST_SECRET"
    fake_credentials.refresh_token = "refresh-xyz"
    fake_credentials.token = "access-abc"
    fake_credentials.id_token = _fake_id_token(sub="109876543210", email="alice@example.com")
    fake_credentials.scopes = ["openid", "email", "profile"]

    fake_flow = MagicMock()
    fake_flow.run_local_server.return_value = fake_credentials

    with patch.object(
        stdio_mod.InstalledAppFlow,
        "from_client_secrets_file",
        return_value=fake_flow,
    ) as from_file:
        import argparse

        rc = stdio_mod.cmd_login(argparse.Namespace(client_secret=str(client_secret_file)))
    assert rc == 0

    # The flow was built from the user's client_secret.json with the v2 scopes.
    from_file.assert_called_once()
    _pos, kwargs = from_file.call_args
    assert "scopes" in kwargs
    assert "https://www.googleapis.com/auth/chat.messages.create" in kwargs["scopes"]

    # Loopback ran on 127.0.0.1 with an OS-assigned port.
    run_kwargs = fake_flow.run_local_server.call_args.kwargs
    assert run_kwargs["host"] == "127.0.0.1"
    assert run_kwargs["port"] == 0
    # URL prompt shown to the user (headless-safe).
    assert "Open this URL" in run_kwargs["authorization_prompt_message"]

    # Tokens saved; content decrypts and carries the stored identity.
    store = stdio_mod._open_store()
    saved = store.load()
    assert saved["refresh_token"] == "refresh-xyz"
    assert saved["client_id"] == "test-client-id.apps.googleusercontent.com"
    assert saved["user_sub"] == "109876543210"
    assert saved["user_email"] == "alice@example.com"

    mode = stat.S_IMODE((stdio_home / "tokens.json").stat().st_mode)
    assert mode == 0o600

    out = capsys.readouterr().out
    assert "alice@example.com" in out


def test_login_requires_client_secret(stdio_home: Path, capsys: pytest.CaptureFixture[str]) -> None:
    import argparse

    rc = stdio_mod.cmd_login(argparse.Namespace(client_secret=None))
    assert rc == 2
    assert "--client-secret is required" in capsys.readouterr().err


def test_login_rejects_missing_file(
    stdio_home: Path, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    import argparse

    rc = stdio_mod.cmd_login(
        argparse.Namespace(client_secret=str(tmp_path / "does-not-exist.json"))
    )
    assert rc == 2
    assert "does not exist" in capsys.readouterr().err


def test_login_refuses_when_refresh_token_missing(
    stdio_home: Path,
    client_secret_file: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """Google sometimes returns no refresh_token (e.g. prompt mismatch). We refuse to save."""
    fake_credentials = MagicMock()
    fake_credentials.refresh_token = None
    fake_flow = MagicMock()
    fake_flow.run_local_server.return_value = fake_credentials

    with patch.object(
        stdio_mod.InstalledAppFlow,
        "from_client_secrets_file",
        return_value=fake_flow,
    ):
        import argparse

        rc = stdio_mod.cmd_login(argparse.Namespace(client_secret=str(client_secret_file)))
    assert rc == 1
    assert "refresh_token" in capsys.readouterr().err


# ---------- logout ----------


def test_logout_revokes_and_deletes(stdio_home: Path) -> None:
    store = stdio_mod._open_store()
    store.save({"refresh_token": "r-to-revoke"})
    revoke_calls: list[dict[str, str]] = []

    def fake_post_form(url: str, data: dict[str, str], **_kwargs: object) -> dict[str, object]:
        revoke_calls.append({"url": url, "token": data["token"]})
        return {}

    import argparse

    with patch.object(stdio_mod, "_http_post_form", side_effect=fake_post_form):
        rc = stdio_mod.cmd_logout(argparse.Namespace())
    assert rc == 0
    assert revoke_calls == [{"url": stdio_mod._GOOGLE_REVOKE, "token": "r-to-revoke"}]
    assert not (stdio_home / "tokens.json").exists()
    assert not (stdio_home / "fernet.key").exists()


def test_logout_when_already_logged_out_exits_cleanly(stdio_home: Path) -> None:
    import argparse

    rc = stdio_mod.cmd_logout(argparse.Namespace())
    assert rc == 0


def test_logout_tolerates_non200_revoke(stdio_home: Path) -> None:
    """Google's docs don't promise revoke idempotency; non-200 still deletes locally."""
    store = stdio_mod._open_store()
    store.save({"refresh_token": "r-broken"})

    def raise_posting(*_a: object, **_k: object) -> dict[str, object]:
        raise RuntimeError("400 Bad Request: token already revoked")

    import argparse

    with patch.object(stdio_mod, "_http_post_form", side_effect=raise_posting):
        rc = stdio_mod.cmd_logout(argparse.Namespace())
    assert rc == 0
    assert not (stdio_home / "tokens.json").exists()


# ---------- serve ----------


def test_serve_without_tokens_exits_2(stdio_home: Path, capsys: pytest.CaptureFixture[str]) -> None:
    import argparse

    rc = stdio_mod.cmd_serve(argparse.Namespace())
    assert rc == 2
    err = capsys.readouterr().err
    assert "no local credentials" in err


# ---------- resolver ----------


@pytest.mark.asyncio
async def test_resolver_refreshes_and_persists_rotated_refresh_token(
    stdio_home: Path,
) -> None:
    """Credentials.refresh() is delegated to google-auth; on rotation, persist."""
    store = stdio_mod._open_store()
    store.save(
        {
            "client_id": "cid",
            "client_secret": "csec",
            "refresh_token": "original-refresh",
            "granted_scopes": ["openid"],
            "user_sub": "42",
        }
    )

    # Fake Credentials.refresh: mutate the same private backing fields
    # google-auth writes (`.token`, `._refresh_token`, `.expiry`).
    def fake_refresh(credentials, request):
        credentials.token = "access-fresh"
        credentials.expiry = None
        if credentials.refresh_token == "original-refresh":
            credentials._refresh_token = "rotated-refresh"

    with (
        patch.object(stdio_mod.Credentials, "refresh", autospec=True) as mock_refresh,
        patch.object(
            stdio_mod.Credentials,
            "expired",
            new_callable=lambda: property(lambda _self: True),
        ),
    ):
        mock_refresh.side_effect = fake_refresh
        resolver, _identity = stdio_mod._build_stdio_resolver(store)
        info = await resolver()

    assert info.access_token == "access-fresh"
    assert info.user_sub == "42"

    # The rotated refresh token was persisted back to disk.
    assert store.load()["refresh_token"] == "rotated-refresh"


# ---------- argparse ----------


def test_argparse_login_subcommand(client_secret_file: Path) -> None:
    parser = stdio_mod._build_parser()
    args = parser.parse_args(["login", "--client-secret", str(client_secret_file)])
    assert args.command == "login"
    assert args.client_secret == str(client_secret_file)
    assert args.func.__name__ == "cmd_login"


def test_argparse_default_is_serve() -> None:
    parser = stdio_mod._build_parser()
    args = parser.parse_args([])
    assert getattr(args, "func", stdio_mod.cmd_serve) is stdio_mod.cmd_serve


# ---------- stdout hygiene regression ----------


def test_stdout_hygiene_in_subprocess(tmp_path: Path) -> None:
    """Spawn the CLI with no tokens — stderr may log, stdout MUST stay silent."""
    env = {
        "PATH": "/usr/bin:/bin",
        "HOME": str(tmp_path),
        "GCM_CONFIG_DIR": str(tmp_path / "gcm"),
    }
    result = subprocess.run(
        [sys.executable, "-m", "src.stdio", "serve"],
        capture_output=True,
        timeout=10.0,
        env=env,
        check=False,
    )
    assert result.returncode == 2
    assert result.stdout == b"", f"stdout must be empty on serve bail; got {result.stdout!r}"
    assert b"no local credentials" in result.stderr


# ---------- helpers ----------


def _fake_id_token(*, sub: str, email: str) -> str:
    """Build an unsigned ID token with the claims we extract (payload only, no signature check)."""
    import base64

    header = base64.urlsafe_b64encode(b'{"alg":"RS256","typ":"JWT"}').rstrip(b"=").decode()
    payload_json = json.dumps({"sub": sub, "email": email, "iss": "test"})
    payload = base64.urlsafe_b64encode(payload_json.encode()).rstrip(b"=").decode()
    return f"{header}.{payload}.not-a-real-sig"
