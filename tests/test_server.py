"""HTTPS entry-point composition tests.

``src/server.py`` is the HTTPS composition root: it wires the upstream Google
OAuth provider into ``build_app``. The in-process integration harness
(``tests/test_integration_https.py``) bypasses it with a stub ``TokenVerifier``
so the custom routes can be probed without real secrets flowing through
``GoogleProvider``.

These tests fill the gap — they actually call ``build_auth`` + ``main`` so the
composition root is covered by ``--cov=src``.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

import pytest
from fastmcp.server.auth.providers.google import GoogleProvider
from src import server
from src.config import Settings


def test_build_auth_returns_google_provider(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Smoke: build_auth constructs a GoogleProvider with the expected redirect path.

    Uses the autouse conftest env seed (`_env`), overriding only the data_dir
    so the DiskStore write is scoped under tmp_path.
    """
    monkeypatch.setenv("GCM_DATA_DIR", str(tmp_path))
    settings = Settings.from_env()
    provider = server.build_auth(settings)
    assert isinstance(provider, GoogleProvider)


def test_main_builds_app_and_invokes_http_run(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """main() wires Settings → build_auth → build_app → app.run(transport=http).

    Patches ``FastMCP.run`` so the call doesn't actually bind a port. Asserts
    that the transport kwarg is ``http`` — the one behavior HTTPS composition
    guarantees over the stdio entry point.
    """
    monkeypatch.setenv("GCM_DATA_DIR", str(tmp_path))
    with patch("fastmcp.FastMCP.run") as mock_run:
        server.main()
    assert mock_run.called
    kwargs = mock_run.call_args.kwargs
    assert kwargs["transport"] == "http"
    assert kwargs["port"] == 8000
