#!/bin/sh
# Picks the Linux binary for this machine's architecture.
#
# A bundle manifest names a command per platform and has no key for the
# architecture, so a Linux entry would otherwise have to be one binary
# and be wrong for everyone else. Claude Desktop for Linux ships x64 and
# arm64 both, so "everyone else" is a real set of people. macOS solves
# this with a universal binary and Windows by running amd64 under
# emulation; Linux has neither, so the choice is made here instead.
#
# exec, not a call: the server talks MCP over this process's stdio, and
# a shell left in the middle would own the pipes.
set -eu

dir=$(dirname "$0")

case $(uname -m) in
  x86_64 | amd64)
    bin=$dir/google-chat-mcp-amd64
    ;;
  aarch64 | arm64)
    bin=$dir/google-chat-mcp-arm64
    ;;
  *)
    # stderr, because stdout is the JSON-RPC channel and a line of
    # English on it corrupts the session before the client's first
    # request completes.
    echo "google-chat-mcp: no binary in this bundle for $(uname -m)." \
         "Install with: go install github.com/mmedum/google-chat-mcp/cmd/google-chat-mcp@latest" >&2
    exit 1
    ;;
esac

if [ ! -x "$bin" ]; then
  echo "google-chat-mcp: $bin is missing from the bundle or is not executable." >&2
  exit 1
fi

exec "$bin" "$@"
