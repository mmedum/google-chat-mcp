"""Stdio transport entry: CLI login + subprocess MCP server.

Per-user deployment: each user installs this package, runs
`google-chat-mcp login --client-secret <their own client_secret.json>` to
exchange OAuth code for a refresh token (stored locally, Fernet-encrypted,
0600), then launches `google-chat-mcp` (or `mcp-server-google-chat`) as a
subprocess under an MCP client (Claude Code, opencode, Cursor, etc.).

No GoogleProvider, no FastMCP bearer JWT — the trust model is "the user is
the process owner". stdout is reserved for MCP JSON-RPC frames in `serve`
mode; structlog writes to stderr.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import hashlib
import http.server
import json
import os
import secrets
import socketserver
import ssl
import sys
import threading
import time
import urllib.parse
import urllib.request
import webbrowser
from collections.abc import Mapping
from pathlib import Path
from typing import Any

from cryptography.fernet import Fernet, InvalidToken

from .app import build_app
from .config import GOOGLE_OAUTH_SCOPES, Settings
from .observability import configure_logging
from .tools._common import AuthInfo

# ---------- constants ----------

_CONFIG_DIR_ENV = "GCM_CONFIG_DIR"
_TOKENS_PATH_ENV = "GCM_TOKENS_PATH"
_CLIENT_SECRET_ENV = "GCM_CLIENT_SECRET"
_DEFAULT_CONFIG_DIR = Path.home() / ".config" / "google-chat-mcp"
_TOKENS_FILE = "tokens.json"
_FERNET_KEY_FILE = "fernet.key"
_AUDIT_PEPPER_FILE = "audit_pepper"

_GOOGLE_AUTHZ = "https://accounts.google.com/o/oauth2/v2/auth"
_GOOGLE_TOKEN = "https://oauth2.googleapis.com/token"
_GOOGLE_REVOKE = "https://oauth2.googleapis.com/revoke"

# Leave a small refresh cushion so a request doesn't race token expiry.
_ACCESS_TOKEN_REFRESH_LEEWAY_SECONDS = 60


# ---------- config directory ----------


def _config_dir() -> Path:
    raw = os.environ.get(_CONFIG_DIR_ENV)
    return Path(raw) if raw else _DEFAULT_CONFIG_DIR


def _tokens_path() -> Path:
    raw = os.environ.get(_TOKENS_PATH_ENV)
    return Path(raw) if raw else _config_dir() / _TOKENS_FILE


def _ensure_config_dir() -> Path:
    """Create the config dir with 0700 perms if absent. Idempotent."""
    d = _config_dir()
    d.mkdir(parents=True, exist_ok=True)
    # mkdir honors umask, so mode may be 0755. Force tight perms on our dir.
    d.chmod(0o700)
    return d


def _atomic_write_bytes(path: Path, data: bytes, *, mode: int = 0o600) -> None:
    """Write bytes to `path` atomically (temp + os.replace), setting mode on success."""
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_bytes(data)
    tmp.chmod(mode)
    os.replace(tmp, path)


# ---------- local Fernet key ----------


def _load_or_create_fernet_key() -> bytes:
    """Return the per-installation Fernet key, generating + persisting one if absent."""
    _ensure_config_dir()
    key_path = _config_dir() / _FERNET_KEY_FILE
    if key_path.exists():
        return key_path.read_bytes().strip()
    key = Fernet.generate_key()
    _atomic_write_bytes(key_path, key)
    return key


def _load_or_create_audit_pepper() -> bytes:
    """Return the audit-log HMAC pepper, generating + persisting one if absent."""
    _ensure_config_dir()
    pepper_path = _config_dir() / _AUDIT_PEPPER_FILE
    if pepper_path.exists():
        return pepper_path.read_bytes().strip()
    pepper = secrets.token_bytes(32)
    _atomic_write_bytes(pepper_path, pepper)
    return pepper


# ---------- token store ----------


class TokenStore:
    """Fernet-encrypted JSON store for OAuth tokens and client credentials.

    On-disk shape (decrypted):
    {
      "client_id": "...",
      "client_secret": "...",
      "refresh_token": "...",
      "granted_scopes": ["openid", ...],
      "user_sub": "109876543210",
      "user_email": "alice@example.com",
    }
    """

    def __init__(self, path: Path, fernet: Fernet) -> None:
        self._path = path
        self._fernet = fernet

    def exists(self) -> bool:
        return self._path.exists()

    def load(self) -> dict[str, Any]:
        raw = self._path.read_bytes()
        try:
            decrypted = self._fernet.decrypt(raw)
        except InvalidToken as exc:
            raise RuntimeError(
                f"Cannot decrypt {self._path}. Either the Fernet key changed "
                "or the file is corrupt — run `google-chat-mcp logout` and "
                "re-login."
            ) from exc
        parsed = json.loads(decrypted.decode("utf-8"))
        if not isinstance(parsed, dict):
            raise RuntimeError(f"{self._path}: expected a JSON object")
        return parsed

    def save(self, data: Mapping[str, Any]) -> None:
        _ensure_config_dir()
        payload = json.dumps(dict(data), separators=(",", ":")).encode("utf-8")
        encrypted = self._fernet.encrypt(payload)
        _atomic_write_bytes(self._path, encrypted)

    def delete(self) -> None:
        if self._path.exists():
            self._path.unlink()


def _open_store() -> TokenStore:
    key = _load_or_create_fernet_key()
    return TokenStore(_tokens_path(), Fernet(key))


# ---------- OAuth helpers ----------


def _read_client_secret(path: Path) -> tuple[str, str]:
    """Parse Google's downloaded `client_secret.json` (installed-app format)."""
    data = json.loads(path.read_text())
    # Desktop apps key under "installed"; web apps under "web". We accept
    # either since both work for the loopback flow.
    for top_key in ("installed", "web"):
        section = data.get(top_key)
        if isinstance(section, dict):
            cid = section.get("client_id")
            csec = section.get("client_secret")
            if isinstance(cid, str) and isinstance(csec, str):
                return cid, csec
    raise ValueError(
        f"{path}: no installed/web section with client_id + client_secret. "
        "Download a Desktop OAuth client JSON from Google Cloud Console."
    )


def _pkce_pair() -> tuple[str, str]:
    """Return (code_verifier, code_challenge) for RFC 7636 S256 PKCE."""
    verifier = secrets.token_urlsafe(64)[:128]
    digest = hashlib.sha256(verifier.encode("ascii")).digest()
    challenge = base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")
    return verifier, challenge


def _build_authz_url(
    *,
    client_id: str,
    redirect_uri: str,
    scopes: list[str],
    state: str,
    code_challenge: str,
) -> str:
    params = {
        "client_id": client_id,
        "redirect_uri": redirect_uri,
        "response_type": "code",
        "scope": " ".join(scopes),
        "access_type": "offline",
        "prompt": "consent",
        "include_granted_scopes": "true",
        "state": state,
        "code_challenge": code_challenge,
        "code_challenge_method": "S256",
    }
    return f"{_GOOGLE_AUTHZ}?{urllib.parse.urlencode(params)}"


def _http_post_form(url: str, data: Mapping[str, str], *, timeout: float = 15.0) -> dict[str, Any]:
    """POST form-encoded body and return JSON response. Raises on non-2xx."""
    body = urllib.parse.urlencode(data).encode("ascii")
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        method="POST",
    )
    ctx = ssl.create_default_context()
    with urllib.request.urlopen(req, timeout=timeout, context=ctx) as resp:
        raw = resp.read().decode("utf-8")
    parsed = json.loads(raw) if raw else {}
    if not isinstance(parsed, dict):
        return {}
    return parsed


# ---------- loopback callback listener ----------


class _LoopbackCallbackHandler(http.server.BaseHTTPRequestHandler):
    """One-shot handler. Captures the code + state into class-level slots."""

    received_code: str | None = None
    received_state: str | None = None
    received_error: str | None = None

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        params = urllib.parse.parse_qs(parsed.query)
        type(self).received_code = (params.get("code") or [None])[0]
        type(self).received_state = (params.get("state") or [None])[0]
        type(self).received_error = (params.get("error") or [None])[0]
        body = (
            b"<html><body><h2>You may close this window.</h2>"
            b"<p>google-chat-mcp received the authorization code.</p>"
            b"</body></html>"
        )
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        # Silence the stdlib's stderr request logger — the login flow has its
        # own status prints; we don't want "GET /?code=... HTTP/1.1 200" mixed in.
        return


def _wait_for_callback(port_holder: dict[str, int]) -> tuple[str, str]:
    """Start a loopback listener, return (code, state). Blocks until the hit."""
    # Reset the class slots in case the process serves multiple login attempts
    # (e.g. in tests).
    _LoopbackCallbackHandler.received_code = None
    _LoopbackCallbackHandler.received_state = None
    _LoopbackCallbackHandler.received_error = None
    with socketserver.TCPServer(("127.0.0.1", 0), _LoopbackCallbackHandler) as httpd:
        port_holder["port"] = httpd.server_address[1]
        httpd.handle_request()
    err = _LoopbackCallbackHandler.received_error
    if err:
        raise RuntimeError(f"Google returned OAuth error: {err}")
    code = _LoopbackCallbackHandler.received_code
    state = _LoopbackCallbackHandler.received_state
    if not code or not state:
        raise RuntimeError("OAuth callback missing `code` or `state`.")
    return code, state


# ---------- login subcommand ----------


def _open_browser_with_fallback(url: str) -> None:
    """Print the URL to stdout (user sees it either way), then try the browser."""
    print(f"\nOpen this URL in a browser to authorize:\n\n  {url}\n")
    try:
        opened = webbrowser.open(url, new=1, autoraise=True)
    except Exception:
        opened = False
    if not opened:
        print(
            "Could not open a browser automatically. Copy the URL above and "
            "paste it into any browser on a machine that can reach your "
            "Google account. Return here — this process is listening.\n"
        )


def cmd_login(args: argparse.Namespace) -> int:
    client_secret_arg = args.client_secret or os.environ.get(_CLIENT_SECRET_ENV)
    if not client_secret_arg:
        print(
            "error: --client-secret is required (or set GCM_CLIENT_SECRET). "
            "Download Desktop-app credentials from Google Cloud Console.",
            file=sys.stderr,
        )
        return 2
    client_secret_path = Path(client_secret_arg).expanduser()
    if not client_secret_path.is_file():
        print(f"error: {client_secret_path} does not exist or is not a file", file=sys.stderr)
        return 2
    client_id, client_secret = _read_client_secret(client_secret_path)

    state = secrets.token_urlsafe(32)
    code_verifier, code_challenge = _pkce_pair()

    port_holder: dict[str, int] = {}

    # The redirect_uri depends on the listener's random port. Start the
    # listener on a thread, probe its port, then kick off the browser with
    # the correct redirect.
    callback_result: dict[str, str] = {}

    def _serve_once() -> None:
        try:
            code, returned_state = _wait_for_callback(port_holder)
            callback_result["code"] = code
            callback_result["state"] = returned_state
        except RuntimeError as exc:
            callback_result["error"] = str(exc)

    thread = threading.Thread(target=_serve_once, daemon=True)
    thread.start()

    # Spin until the listener has bound and published its port.
    deadline = time.monotonic() + 5.0
    while "port" not in port_holder:
        if time.monotonic() > deadline:
            print("error: loopback listener failed to start", file=sys.stderr)
            return 1
        time.sleep(0.01)
    redirect_uri = f"http://127.0.0.1:{port_holder['port']}/callback"

    authz_url = _build_authz_url(
        client_id=client_id,
        redirect_uri=redirect_uri,
        scopes=list(GOOGLE_OAUTH_SCOPES),
        state=state,
        code_challenge=code_challenge,
    )
    _open_browser_with_fallback(authz_url)
    thread.join(timeout=300.0)
    if thread.is_alive():
        print("error: timed out waiting for OAuth callback (5 minutes)", file=sys.stderr)
        return 1
    if "error" in callback_result:
        print(f"error: {callback_result['error']}", file=sys.stderr)
        return 1
    if callback_result.get("state") != state:
        print("error: OAuth state mismatch (possible CSRF)", file=sys.stderr)
        return 1

    token_resp = _http_post_form(
        _GOOGLE_TOKEN,
        {
            "code": callback_result["code"],
            "client_id": client_id,
            "client_secret": client_secret,
            "redirect_uri": redirect_uri,
            "grant_type": "authorization_code",
            "code_verifier": code_verifier,
        },
    )
    refresh_token = token_resp.get("refresh_token")
    access_token = token_resp.get("access_token")
    id_token = token_resp.get("id_token")
    granted_scope = token_resp.get("scope", "")
    if not isinstance(refresh_token, str) or not refresh_token:
        print("error: Google did not return a refresh_token", file=sys.stderr)
        return 1

    user_sub, user_email = (
        _identity_from_id_token(id_token)
        if isinstance(id_token, str)
        else (
            None,
            None,
        )
    )
    if user_sub is None and isinstance(access_token, str):
        # Fall back to OIDC /userinfo if the id_token wasn't returned.
        user_sub, user_email = _identity_from_userinfo(access_token)

    store = _open_store()
    store.save(
        {
            "client_id": client_id,
            "client_secret": client_secret,
            "refresh_token": refresh_token,
            "granted_scopes": granted_scope.split() if isinstance(granted_scope, str) else [],
            "user_sub": user_sub,
            "user_email": user_email,
        }
    )
    print(f"Saved credentials to {_tokens_path()}.")
    if user_email:
        print(f"Authenticated as {user_email} (sub: {user_sub}).")
    return 0


def _identity_from_id_token(id_token: str) -> tuple[str | None, str | None]:
    """Best-effort sub + email from a Google ID token (no signature check).

    The ID token is fresh from Google's token endpoint over TLS — we trust it
    as the identity-in-transit for this one read. For hardened consumers of
    the sub value we verify via /userinfo with the access token instead.
    """
    try:
        # JWT: header.payload.signature — we only need payload.
        _, payload_b64, _ = id_token.split(".")
    except ValueError:
        return None, None
    padding = "=" * (-len(payload_b64) % 4)
    try:
        data = json.loads(base64.urlsafe_b64decode(payload_b64 + padding))
    except Exception as exc:
        if not isinstance(exc, ValueError | UnicodeDecodeError):
            raise
        return None, None
    if not isinstance(data, dict):
        return None, None
    sub = data.get("sub") if isinstance(data.get("sub"), str) else None
    email = data.get("email") if isinstance(data.get("email"), str) else None
    return sub, email


def _identity_from_userinfo(access_token: str) -> tuple[str | None, str | None]:
    """Hit Google's OIDC /userinfo synchronously with the access token."""
    req = urllib.request.Request(
        "https://openidconnect.googleapis.com/v1/userinfo",
        headers={"Authorization": f"Bearer {access_token}"},
    )
    ctx = ssl.create_default_context()
    try:
        with urllib.request.urlopen(req, timeout=10.0, context=ctx) as resp:
            parsed = json.loads(resp.read().decode("utf-8"))
    except Exception as exc:
        if not isinstance(exc, OSError | ValueError):
            raise
        return None, None
    if not isinstance(parsed, dict):
        return None, None
    sub = parsed.get("sub") if isinstance(parsed.get("sub"), str) else None
    email = parsed.get("email") if isinstance(parsed.get("email"), str) else None
    return sub, email


# ---------- logout subcommand ----------


def cmd_logout(_args: argparse.Namespace) -> int:
    tokens_path = _tokens_path()
    if not tokens_path.exists():
        print("No local tokens found — already logged out.")
        return 0
    # Attempt revoke on the upstream. Treat any non-200 as success — Google's
    # docs don't promise idempotency, and we're about to delete locally regardless.
    try:
        store = _open_store()
        data = store.load()
    except Exception as exc:
        print(
            f"warning: could not load tokens for revoke ({exc}); deleting anyway", file=sys.stderr
        )
        data = {}
    refresh_token = data.get("refresh_token")
    if isinstance(refresh_token, str) and refresh_token:
        try:
            _http_post_form(_GOOGLE_REVOKE, {"token": refresh_token})
            print("Revoked refresh token at Google.")
        except Exception as exc:
            print(f"warning: revoke returned {exc} — deleting local tokens anyway", file=sys.stderr)

    tokens_path.unlink(missing_ok=True)
    fernet_path = _config_dir() / _FERNET_KEY_FILE
    fernet_path.unlink(missing_ok=True)
    print(f"Deleted {tokens_path} and {fernet_path}.")
    return 0


# ---------- serve subcommand (default) ----------


def _build_stdio_resolver(store: TokenStore) -> tuple[object, dict[str, Any]]:
    """Return an AuthResolver closure + the stored identity dict.

    The closure refreshes access tokens on demand (using the stored refresh
    token + client secret) and caches them in-memory with a small expiry
    cushion. It does NOT write the decrypted token to disk.
    """
    identity = store.load()
    cache: dict[str, Any] = {"access_token": None, "expires_at": 0.0}

    async def resolver() -> AuthInfo:
        now = time.time()
        access: str | None = cache["access_token"]
        if access is None or now >= cache["expires_at"] - _ACCESS_TOKEN_REFRESH_LEEWAY_SECONDS:
            refreshed = await asyncio.to_thread(
                _http_post_form,
                _GOOGLE_TOKEN,
                {
                    "client_id": identity["client_id"],
                    "client_secret": identity["client_secret"],
                    "refresh_token": identity["refresh_token"],
                    "grant_type": "refresh_token",
                },
            )
            new_access = refreshed.get("access_token")
            expires_in = refreshed.get("expires_in", 3600)
            if not isinstance(new_access, str) or not new_access:
                raise RuntimeError("Google token refresh did not return an access_token")
            cache["access_token"] = new_access
            try:
                cache["expires_at"] = now + float(expires_in)
            except Exception as exc:  # float() on non-numeric: TypeError or ValueError
                if not isinstance(exc, TypeError | ValueError):
                    raise
                cache["expires_at"] = now + 3600.0
            access = new_access
            # If Google rotates the refresh token, persist the new one.
            new_refresh = refreshed.get("refresh_token")
            if (
                isinstance(new_refresh, str)
                and new_refresh
                and new_refresh != identity["refresh_token"]
            ):
                identity["refresh_token"] = new_refresh
                store.save(identity)
        user_sub = identity.get("user_sub") or "stdio-user"
        return AuthInfo(access_token=access, user_sub=str(user_sub))

    return resolver, identity


def _build_stdio_settings(identity: Mapping[str, Any]) -> Settings:
    """Construct Settings for stdio mode.

    Fields that only matter under HTTPS (base_url, JWT/Fernet keys for
    GoogleProvider, redirect allowlist) get placeholders. stdio bypasses
    GoogleProvider so they're unused in this process.
    """
    tmp_data_dir = _ensure_config_dir() / "data"
    tmp_data_dir.mkdir(exist_ok=True)
    return Settings.from_mapping(
        {
            "base_url": "http://127.0.0.1/stdio",
            "data_dir": str(tmp_data_dir),
            "log_level": os.environ.get("GCM_LOG_LEVEL", "INFO"),
            "allowed_client_redirects": [],
            "google_client_id": identity.get("client_id", "unused-in-stdio"),
            "google_client_secret": identity.get("client_secret", "unused-in-stdio"),
            "fernet_key": Fernet.generate_key().decode(),
            "jwt_signing_key": "unused-in-stdio" * 4,
            "audit_pepper": _load_or_create_audit_pepper().hex(),
            # stdio is single-user; hashing adds no privacy beyond the local
            # disk protection. Keep raw subs to make local debugging easier.
            "audit_hash_user_sub": False,
        }
    )


def cmd_serve(_args: argparse.Namespace) -> int:
    store = _open_store()
    if not store.exists():
        print(
            "error: no local credentials. Run `google-chat-mcp login "
            "--client-secret <path>` first.",
            file=sys.stderr,
        )
        return 2
    configure_logging(os.environ.get("GCM_LOG_LEVEL", "INFO"), stream=sys.stderr)
    resolver, identity = _build_stdio_resolver(store)
    settings = _build_stdio_settings(identity)
    app = build_app(settings, resolver=resolver)  # ty: ignore[invalid-argument-type]
    app.run()  # Default transport is stdio.
    return 0


# ---------- argparse ----------


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="google-chat-mcp",
        description="Google Chat MCP server (stdio transport) + OAuth CLI.",
    )
    sub = parser.add_subparsers(dest="command")

    login = sub.add_parser(
        "login",
        help="Authorize a Google account via loopback OAuth + store tokens locally.",
    )
    login.add_argument(
        "--client-secret",
        help=(
            "Path to Google's downloaded Desktop-app client_secret.json. "
            f"Env: {_CLIENT_SECRET_ENV}."
        ),
    )
    login.set_defaults(func=cmd_login)

    logout = sub.add_parser(
        "logout", help="Revoke the refresh token and delete local tokens + key."
    )
    logout.set_defaults(func=cmd_logout)

    serve = sub.add_parser(
        "serve", help="Run the MCP server over stdio (default; equivalent to no subcommand)."
    )
    serve.set_defaults(func=cmd_serve)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    func = getattr(args, "func", cmd_serve)
    return func(args)


if __name__ == "__main__":
    raise SystemExit(main())
