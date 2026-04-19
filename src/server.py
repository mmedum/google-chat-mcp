"""HTTPS entry point.

Constructs the Google OAuth provider and hands it to the transport-agnostic
`build_app()` factory. Stdio mode has its own entry point at `src/stdio.py`.
"""

from __future__ import annotations

from cryptography.fernet import Fernet
from fastmcp.server.auth.providers.google import GoogleProvider
from key_value.aio.stores.disk import DiskStore
from key_value.aio.wrappers.encryption import FernetEncryptionWrapper

from .app import build_app
from .config import GOOGLE_OAUTH_SCOPES, Settings
from .observability import configure_logging


def build_auth(settings: Settings) -> GoogleProvider:
    """Wire FastMCP's GoogleProvider (OAuthProxy subclass) with encrypted disk storage."""
    kv_store = DiskStore(directory=str(settings.kv_store_path))
    encrypted_store = FernetEncryptionWrapper(
        key_value=kv_store,
        fernet=Fernet(settings.fernet_key.get_secret_value().encode()),
    )
    return GoogleProvider(
        client_id=settings.google_client_id.get_secret_value(),
        client_secret=settings.google_client_secret.get_secret_value(),
        base_url=settings.base_url,
        redirect_path="/oauth/callback",
        required_scopes=list(GOOGLE_OAUTH_SCOPES),
        valid_scopes=list(GOOGLE_OAUTH_SCOPES),
        allowed_client_redirect_uris=list(settings.allowed_client_redirects),
        client_storage=encrypted_store,
        jwt_signing_key=settings.jwt_signing_key.get_secret_value(),
        require_authorization_consent=True,
    )


def main() -> None:
    settings = Settings.from_env()
    configure_logging(settings.log_level)
    auth = build_auth(settings)
    app = build_app(settings, auth=auth)
    app.run(transport="http", host="0.0.0.0", port=8000)  # noqa: S104


if __name__ == "__main__":
    main()
