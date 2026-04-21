"""End-to-end harness for the stdio entry point.

Spawns ``python -m src.stdio serve`` as an MCP subprocess under the
FastMCP stdio client, with:

- ``GCM_TEST_AUTH_STUB=1`` — ``cmd_serve`` substitutes a fixed AuthInfo
  for the real loopback-refresh resolver. No tokens.json, no Fernet key,
  no Google token endpoint hit.
- ``GCM_CHAT_API_BASE=http://127.0.0.1:<port>`` — ``ChatClient``'s base
  URL, pointed at a local stdlib HTTP server that serves the handful of
  Chat-API responses the test exercises.
- Isolated ``GCM_CONFIG_DIR`` + ``HOME`` — keeps audit pepper + data dir
  under ``tmp_path``, so nothing bleeds into ``~/.config``.

The whole point of this test is the stdout-hygiene regression guard: if
any part of the stdio serve path leaks a ``print()`` or misdirects a
structlog line to stdout, the fastmcp JSON-RPC handshake fails first,
which surfaces as a connect timeout here. The tool-call assertions are
the happy-path proof that ``cmd_serve`` wires ``build_app`` correctly
with the stub resolver + base-URL overrides.
"""

from __future__ import annotations

import http.server
import json
import socketserver
import sys
import threading
from collections.abc import Iterator
from pathlib import Path
from typing import ClassVar

import pytest
from fastmcp import Client
from fastmcp.client.transports import StdioTransport


class _StubChatAPIHandler(http.server.BaseHTTPRequestHandler):
    """Minimal Google Chat API stand-in.

    Supports the narrow surface the integration test calls:
    - ``GET /spaces`` — returns one SPACE and one DIRECT_MESSAGE entry,
      matching Google's ``spaces.list`` response shape (items_key=``spaces``,
      no ``nextPageToken``).

    Anything else → 404 JSON body. Requests are logged to ``_received`` on
    the class so the test can assert what was actually called.
    """

    _received: ClassVar[list[dict[str, str]]] = []

    # Silence the default stderr access-log — keeps test output clean.
    def log_message(self, format: str, *args: object) -> None:
        return

    def do_GET(self) -> None:
        self._received.append({"method": "GET", "path": self.path})
        if self.path.startswith("/spaces"):
            body = json.dumps(
                {
                    "spaces": [
                        {
                            "name": "spaces/AAA",
                            "type": "SPACE",
                            "displayName": "eng-stub",
                        },
                        {
                            "name": "spaces/BBB",
                            "type": "DIRECT_MESSAGE",
                        },
                    ]
                }
            ).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self._send_json(
            404, {"error": {"code": 404, "message": "not stubbed", "status": "NOT_FOUND"}}
        )

    def _send_json(self, status: int, payload: dict[str, object]) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class _ThreadingHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    """One-request-per-thread HTTP server so pagination never blocks itself."""

    daemon_threads = True
    allow_reuse_address = True


@pytest.fixture
def stub_chat_api() -> Iterator[str]:
    """Start the stub Chat API on a random loopback port; yield its base URL."""
    _StubChatAPIHandler._received.clear()
    server = _ThreadingHTTPServer(("127.0.0.1", 0), _StubChatAPIHandler)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, name="stub-chat-api", daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{port}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2.0)


async def test_stdio_happy_path_tools_list_and_call(tmp_path: Path, stub_chat_api: str) -> None:
    """Drive serve end-to-end: MCP initialize → tools/list → tools/call list_spaces.

    The FastMCP stdio client handles the initialize + notifications/initialized
    handshake internally. Any structlog line or print() on stdout would cause
    the JSON-RPC parser to choke before ``list_tools`` returns — that failure
    mode IS the stdout-hygiene regression assertion.
    """
    config_dir = tmp_path / "gcm"
    stderr_log = tmp_path / "server.stderr"
    env = {
        "PATH": "/usr/bin:/bin:/usr/local/bin",
        "HOME": str(tmp_path / "home"),
        "GCM_CONFIG_DIR": str(config_dir),
        "GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME": "1",
        "GCM_TEST_AUTH_STUB": "1",
        # GCM_DEV_MODE=1 is required when GCM_CHAT_API_BASE / GCM_PEOPLE_API_BASE
        # point at anything other than *.googleapis.com. The validator rejects
        # non-Google URLs in production to close the token-exfil vector.
        "GCM_DEV_MODE": "1",
        "GCM_CHAT_API_BASE": stub_chat_api,
        "GCM_PEOPLE_API_BASE": stub_chat_api,
        "GCM_LOG_LEVEL": "INFO",
    }
    transport = StdioTransport(
        command=sys.executable,
        args=["-m", "src.stdio", "serve"],
        env=env,
        keep_alive=False,
        log_file=stderr_log,
    )
    async with Client(transport) as client:
        tools = await client.list_tools()
        tool_names = {t.name for t in tools}
        assert len(tools) == 21
        assert "list_spaces" in tool_names
        assert "whoami" in tool_names

        result = await client.call_tool("list_spaces", {"limit": 2})

    assert result.structured_content is not None
    spaces = result.structured_content["result"]
    assert isinstance(spaces, list)
    assert len(spaces) == 2
    assert spaces[0]["space_id"] == "spaces/AAA"
    assert spaces[0]["display_name"] == "eng-stub"
    # The stub Chat server saw exactly our list_spaces call.
    paths = [r["path"] for r in _StubChatAPIHandler._received]
    assert any(p.startswith("/spaces") for p in paths), paths
