"""stdio CLI — login flow, logout, headless fallback, stdout hygiene."""

from __future__ import annotations

import json
import stat
import subprocess
import sys
import threading
import time
import urllib.request
from pathlib import Path
from unittest.mock import patch

import pytest
from cryptography.fernet import Fernet
from src import stdio as stdio_mod

# ---------- fixtures ----------


@pytest.fixture
def stdio_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Isolate each test: config dir under tmp_path, no bleed from real ~/.config."""
    d = tmp_path / "gcm"
    monkeypatch.setenv("GCM_CONFIG_DIR", str(d))
    # Deletion note: stdio's serve path doesn't call these, but present them so
    # `_tokens_path()` resolves inside the sandbox even if GCM_TOKENS_PATH is unset.
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


# ---------- unit: client_secret parser ----------


def test_read_client_secret_parses_installed(client_secret_file: Path) -> None:
    cid, csec = stdio_mod._read_client_secret(client_secret_file)
    assert cid == "test-client-id.apps.googleusercontent.com"
    assert csec == "TEST_SECRET"


def test_read_client_secret_rejects_missing_fields(tmp_path: Path) -> None:
    bad = tmp_path / "bad.json"
    bad.write_text("{}")
    with pytest.raises(ValueError, match="installed/web"):
        stdio_mod._read_client_secret(bad)


# ---------- unit: PKCE ----------


def test_pkce_pair_is_s256_compliant() -> None:
    verifier, challenge = stdio_mod._pkce_pair()
    import base64
    import hashlib

    digest = hashlib.sha256(verifier.encode("ascii")).digest()
    expected = base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")
    assert challenge == expected
    assert 43 <= len(verifier) <= 128


# ---------- integration: login end-to-end with fake Google ----------


def test_login_end_to_end(
    stdio_home: Path,
    client_secret_file: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """Full login path with a stubbed Google token endpoint and a fake browser.

    The flow's own loopback listener runs on a background thread (per
    cmd_login). We patch webbrowser.open to parse the authz URL (it carries
    both the `state` to echo back and the `redirect_uri` with the listener's
    random port) and hit the callback directly from another thread.
    """
    import urllib.parse as _up

    seen_state: dict[str, str] = {}

    def fake_open(url: str, *_args: object, **_kwargs: object) -> bool:
        qs = _up.parse_qs(_up.urlparse(url).query)
        state = qs["state"][0]
        redirect_uri = qs["redirect_uri"][0]
        seen_state["state"] = state
        seen_state["redirect_uri"] = redirect_uri

        def hit_callback() -> None:
            callback_url = f"{redirect_uri}?code=TEST_CODE&state={state}"
            deadline = time.monotonic() + 5.0
            # Small retry: the listener may still be finishing bind() the
            # microsecond the browser stub is invoked.
            while True:
                try:
                    urllib.request.urlopen(callback_url, timeout=3.0).read()  # noqa: S310
                    return
                except OSError:
                    if time.monotonic() > deadline:
                        raise
                    time.sleep(0.01)

        threading.Thread(target=hit_callback, daemon=True).start()
        return True

    def fake_post_form(url: str, data: dict[str, str], **_kwargs: object) -> dict[str, object]:
        assert url == stdio_mod._GOOGLE_TOKEN
        assert data["grant_type"] == "authorization_code"
        assert data["code"] == "TEST_CODE"
        assert data["code_verifier"]  # PKCE verifier echoed back
        return {
            "access_token": "access-abc",
            "refresh_token": "refresh-xyz",
            "expires_in": 3600,
            "scope": "openid email profile https://www.googleapis.com/auth/chat.messages.create",
            "id_token": _fake_id_token(sub="109876543210", email="alice@example.com"),
        }

    with (
        patch.object(stdio_mod, "webbrowser") as mock_web,
        patch.object(stdio_mod, "_http_post_form", side_effect=fake_post_form),
    ):
        mock_web.open.side_effect = fake_open
        import argparse

        rc = stdio_mod.cmd_login(argparse.Namespace(client_secret=str(client_secret_file)))
    assert rc == 0

    store = stdio_mod._open_store()
    saved = store.load()
    assert saved["refresh_token"] == "refresh-xyz"
    assert saved["client_id"] == "test-client-id.apps.googleusercontent.com"
    assert saved["user_sub"] == "109876543210"
    assert saved["user_email"] == "alice@example.com"

    mode = stat.S_IMODE((stdio_home / "tokens.json").stat().st_mode)
    assert mode == 0o600

    captured_out = capsys.readouterr().out
    assert "Open this URL in a browser" in captured_out
    assert f"state={seen_state['state']}" in captured_out


def test_login_headless_fallback_prints_manual_instruction(
    stdio_home: Path,
    client_secret_file: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """webbrowser.open returning False triggers manual-paste instructions."""
    from src.stdio import _open_browser_with_fallback

    with patch("src.stdio.webbrowser") as mock_web:
        mock_web.open.return_value = False
        _open_browser_with_fallback("https://example.com/authz?state=x")

    out = capsys.readouterr().out
    assert "Open this URL in a browser" in out
    assert "Could not open a browser automatically" in out


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


# ---------- serve: needs tokens ----------


def test_serve_without_tokens_exits_2(stdio_home: Path, capsys: pytest.CaptureFixture[str]) -> None:
    import argparse

    rc = stdio_mod.cmd_serve(argparse.Namespace())
    assert rc == 2
    err = capsys.readouterr().err
    assert "no local credentials" in err


# ---------- argparse wiring ----------


def test_argparse_login_subcommand(client_secret_file: Path) -> None:
    parser = stdio_mod._build_parser()
    args = parser.parse_args(["login", "--client-secret", str(client_secret_file)])
    assert args.command == "login"
    assert args.client_secret == str(client_secret_file)
    assert args.func.__name__ == "cmd_login"


def test_argparse_default_is_serve() -> None:
    parser = stdio_mod._build_parser()
    args = parser.parse_args([])
    # No subcommand → main() routes to cmd_serve.
    assert getattr(args, "func", stdio_mod.cmd_serve) is stdio_mod.cmd_serve


# ---------- stdout hygiene regression ----------


def test_stdout_hygiene_in_subprocess(tmp_path: Path) -> None:
    """Spawn the CLI with no tokens — stderr may log, stdout MUST stay silent.

    Serve-mode stdout is reserved for JSON-RPC; login-mode prints are on stdout
    but not during serve. This test exercises the `serve` bail path: it should
    print the "no credentials" message to STDERR, nothing to stdout.
    """
    env = {
        "PATH": "/usr/bin:/bin",
        "HOME": str(tmp_path),
        "GCM_CONFIG_DIR": str(tmp_path / "gcm"),
    }
    # Invoke via the package so we don't depend on the console script being
    # installed on PATH inside the test env.
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
