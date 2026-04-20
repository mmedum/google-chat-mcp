"""Stdio transport entry: CLI login + subprocess MCP server.

Per-user deployment: each user installs this package, runs
`google-chat-mcp login --client-secret <their own client_secret.json>` to
exchange OAuth code for a refresh token (stored locally, Fernet-encrypted,
0600), then launches `google-chat-mcp` (or `mcp-server-google-chat`) as a
subprocess under an MCP client (Claude Code, opencode, Cursor, etc.).

No GoogleProvider, no FastMCP bearer JWT — the trust model is "the user is
the process owner". stdout is reserved for MCP JSON-RPC frames in `serve`
mode; structlog writes to stderr.

OAuth flow (loopback, PKCE, state, token exchange) is delegated to
`google_auth_oauthlib.flow.InstalledAppFlow`. Refresh-on-expired uses
`google.oauth2.credentials.Credentials.refresh()`. Both come from the
`google-auth`/`google-auth-oauthlib` deps already pinned in pyproject.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import json
import os
import secrets
import sys
from collections.abc import Mapping
from pathlib import Path
from typing import Any
from urllib.parse import urlencode

from cryptography.fernet import Fernet, InvalidToken
from google.auth.transport.requests import Request as GoogleAuthRequest
from google.oauth2.credentials import Credentials
from google_auth_oauthlib.flow import InstalledAppFlow

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

_GOOGLE_TOKEN_URI = "https://oauth2.googleapis.com/token"
_GOOGLE_REVOKE = "https://oauth2.googleapis.com/revoke"
_GOOGLE_USERINFO = "https://openidconnect.googleapis.com/v1/userinfo"


def _relax_oauthlib_token_scope() -> None:
    """Apply Google's documented workaround for oauthlib's strict scope check.

    Google canonicalizes `email`/`profile` aliases into their `userinfo.*` URL
    forms on the token-endpoint response; without this, oauthlib's strict
    comparison rejects the response (on initial login) and emits warnings on
    every `Credentials.refresh()` (on serve). `setdefault` so an operator's
    explicit choice still wins.
    """
    os.environ.setdefault("OAUTHLIB_RELAX_TOKEN_SCOPE", "1")


# Placeholder Fernet key for stdio mode. `Settings.fernet_key` is a required
# SecretStr with `min_length=1`; stdio bypasses GoogleProvider so the value
# is never used. Using a constant avoids burning RNG entropy on every
# `serve` startup.
_STDIO_FERNET_PLACEHOLDER = Fernet.generate_key().decode()


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


# ---------- local Fernet key + audit pepper ----------


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


# ---------- OAuth revoke + /userinfo (logout + identity fallback) ----------


_http = GoogleAuthRequest()


def _http_post_form(url: str, data: Mapping[str, str], *, timeout: float = 15.0) -> dict[str, Any]:
    """POST form-encoded body, return JSON response. Raises on non-2xx."""
    body = urlencode(data).encode("ascii")
    resp = _http(
        url=url,
        method="POST",
        body=body,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        timeout=timeout,
    )
    if resp.status >= 400:
        raise RuntimeError(f"{url} returned {resp.status}: {resp.data!r}")
    raw = resp.data.decode("utf-8") if resp.data else ""
    parsed = json.loads(raw) if raw else {}
    return parsed if isinstance(parsed, dict) else {}


def _identity_from_id_token(id_token: str) -> tuple[str | None, str | None]:
    """Best-effort (sub, email) from a Google ID token JWT payload.

    The token came fresh from Google's token endpoint over TLS — we trust it
    as the identity-in-transit for this one read, no signature check.
    """
    try:
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
    """Hit OIDC /userinfo synchronously as a fallback when id_token is absent."""
    try:
        resp = _http(
            url=_GOOGLE_USERINFO,
            method="GET",
            headers={"Authorization": f"Bearer {access_token}"},
            timeout=10.0,
        )
        if resp.status >= 400:
            return None, None
        parsed = json.loads(resp.data.decode("utf-8"))
    except Exception as exc:
        if not isinstance(exc, OSError | ValueError):
            raise
        return None, None
    if not isinstance(parsed, dict):
        return None, None
    sub = parsed.get("sub") if isinstance(parsed.get("sub"), str) else None
    email = parsed.get("email") if isinstance(parsed.get("email"), str) else None
    return sub, email


# ---------- login subcommand ----------


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

    _relax_oauthlib_token_scope()

    flow = InstalledAppFlow.from_client_secrets_file(
        client_secret_path, scopes=list(GOOGLE_OAUTH_SCOPES)
    )
    # port=0 → OS picks a random loopback port (RFC 8252 desktop flow).
    # The flow library handles PKCE + state, opens the browser, prints the URL
    # first (so headless users can paste), and blocks until the callback.
    credentials = flow.run_local_server(
        host="127.0.0.1",
        port=0,
        open_browser=True,
        authorization_prompt_message=(
            "\nOpen this URL in a browser to authorize google-chat-mcp:\n\n  {url}\n"
        ),
        success_message=("You may close this window. google-chat-mcp received the code."),
    )

    if not credentials.refresh_token:
        print("error: Google did not return a refresh_token", file=sys.stderr)
        return 1

    user_sub, user_email = (None, None)
    if isinstance(credentials.id_token, str):
        user_sub, user_email = _identity_from_id_token(credentials.id_token)
    if user_sub is None and isinstance(credentials.token, str):
        user_sub, user_email = _identity_from_userinfo(credentials.token)

    store = _open_store()
    store.save(
        {
            "client_id": credentials.client_id,
            "client_secret": credentials.client_secret,
            "refresh_token": credentials.refresh_token,
            "granted_scopes": list(credentials.scopes or ()),
            "user_sub": user_sub,
            "user_email": user_email,
        }
    )
    print(f"Saved credentials to {_tokens_path()}.")
    if user_email:
        print(f"Authenticated as {user_email} (sub: {user_sub}).")
    return 0


# ---------- logout subcommand ----------


def cmd_logout(_args: argparse.Namespace) -> int:
    tokens_path = _tokens_path()
    if not tokens_path.exists():
        print("No local tokens found — already logged out.")
        return 0
    # Attempt revoke upstream; treat any non-2xx as success — Google's docs
    # don't promise idempotency and we're about to delete locally regardless.
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


def _build_stdio_resolver(store: TokenStore, identity: dict[str, Any]):
    """Return an AuthResolver closure over `identity`.

    Uses `google.oauth2.credentials.Credentials.refresh()` for the standard
    refresh path (in-place update of `.token`, `.expiry`, and `.refresh_token`
    if Google rotates it). Persists `identity` back to `store` when the
    refresh token rotates.
    """
    credentials = Credentials(
        token=None,
        refresh_token=identity["refresh_token"],
        client_id=identity["client_id"],
        client_secret=identity["client_secret"],
        token_uri=_GOOGLE_TOKEN_URI,
        scopes=identity.get("granted_scopes") or list(GOOGLE_OAUTH_SCOPES),
    )

    async def resolver() -> AuthInfo:
        if credentials.token is None or credentials.expired:
            await asyncio.to_thread(credentials.refresh, _http)
            if credentials.refresh_token and credentials.refresh_token != identity["refresh_token"]:
                identity["refresh_token"] = credentials.refresh_token
                store.save(identity)
        user_sub = identity.get("user_sub") or "stdio-user"
        return AuthInfo(access_token=str(credentials.token), user_sub=str(user_sub))

    return resolver


def _build_stdio_settings(identity: Mapping[str, Any]) -> Settings:
    """Construct Settings for stdio mode.

    HTTPS-only fields (base_url, JWT/Fernet keys for GoogleProvider, redirect
    allowlist) get placeholders. stdio bypasses GoogleProvider so nothing
    touches them at runtime.
    """
    tmp_data_dir = _ensure_config_dir() / "data"
    tmp_data_dir.mkdir(exist_ok=True)
    tmp_data_dir.chmod(0o700)  # match the parent config dir's 0700 invariant
    return Settings.from_mapping(
        {
            "base_url": "http://127.0.0.1/stdio",
            "data_dir": str(tmp_data_dir),
            "log_level": os.environ.get("GCM_LOG_LEVEL", "INFO"),
            "allowed_client_redirects": [],
            "google_client_id": identity.get("client_id", "unused-in-stdio"),
            "google_client_secret": identity.get("client_secret", "unused-in-stdio"),
            "fernet_key": _STDIO_FERNET_PLACEHOLDER,
            "jwt_signing_key": "unused-in-stdio" * 4,
            "audit_pepper": _load_or_create_audit_pepper().hex(),
            # stdio is single-user; hashing adds no privacy beyond local disk.
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
    _relax_oauthlib_token_scope()
    configure_logging(os.environ.get("GCM_LOG_LEVEL", "INFO"), stream=sys.stderr)
    identity = store.load()
    resolver = _build_stdio_resolver(store, identity)
    settings = _build_stdio_settings(identity)
    app = build_app(settings, resolver=resolver)
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
