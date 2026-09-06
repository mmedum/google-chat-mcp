#!/usr/bin/env bash
# Packs the Claude Desktop bundle from the binaries goreleaser just built.
#
# It runs as the universal binary's post hook, which is the one point in
# the pipeline where every binary exists and the checksum file has not
# been written yet. That is what puts the bundle in checksums.txt with
# the archives, under the same signature.
#
# A manifest picks a binary by platform and has no key for the
# architecture, so every platform it claims has to work on both. macOS
# does through the universal binary and Windows through amd64, which its
# arm64 build runs under emulation. Linux has neither, and Claude Desktop
# for Linux ships x64 and arm64 both, so the bundle carries both Linux
# binaries and a launcher that picks between them at start.
#
# usage: mcpb-pack.sh VERSION [DIST_DIR]
set -euo pipefail

VERSION=${1:?usage: mcpb-pack.sh VERSION [DIST_DIR]}
DIST=${2:-dist}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Pinned, like every other tool the release runs. 2.1.2 was the current
# release on 2026-09-06, checked with `npm view @anthropic-ai/mcpb
# version`; re-check it when the release pipeline is next touched.
MCPB_VERSION=2.1.2

version=${VERSION#v}
out="$DIST/google-chat-mcp_${version}.mcpb"

# One match or nothing: the layout under dist/ carries the build id and
# the amd64 variant, and a glob that quietly matched two would pack
# whichever sorted first.
only() {
  local what=$1
  shift
  if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    echo "mcpb-pack: expected exactly one $what under $DIST, found $#: $*" >&2
    exit 1
  fi
  printf '%s\n' "$1"
}

shopt -s nullglob
darwin=$(only "darwin universal binary" "$DIST"/*darwin_all*/google-chat-mcp)
windows=$(only "windows amd64 binary" "$DIST"/*windows_amd64*/google-chat-mcp.exe)
linux_amd64=$(only "linux amd64 binary" "$DIST"/*linux_amd64*/google-chat-mcp)
linux_arm64=$(only "linux arm64 binary" "$DIST"/*linux_arm64*/google-chat-mcp)

stage=$(mktemp -d)
trap 'rm -r -f "$stage"' EXIT
mkdir -p "$stage/server"

# mcpb pack forces the execute bit on the entry point alone and copies
# the filesystem mode for everything else, so the Windows binary needs
# its mode set here.
install -m 0755 "$darwin" "$stage/server/google-chat-mcp"
install -m 0755 "$windows" "$stage/server/google-chat-mcp.exe"
install -m 0755 "$linux_amd64" "$stage/server/google-chat-mcp-amd64"
install -m 0755 "$linux_arm64" "$stage/server/google-chat-mcp-arm64"
install -m 0755 "$ROOT/packaging/mcpb/linux-launch.sh" "$stage/server/linux-launch.sh"
install -m 0644 "$ROOT/LICENSE" "$ROOT/README.md" "$stage/"

# The manifest is JSON, so the version goes in through a decode and an
# encode rather than a text substitution: scripts/gates owns anything
# that parses what this repository defines, and it refuses a manifest
# that is not carrying the placeholder.
(cd "$ROOT" && go run ./scripts/gates mcpb-manifest "$version" packaging/mcpb/manifest.json) \
  > "$stage/manifest.json"

# pack validates the manifest against the schema before it writes.
npx --yes "@anthropic-ai/mcpb@$MCPB_VERSION" pack "$stage" "$out"

if [ ! -f "$out" ]; then
  echo "mcpb-pack: $out was not written" >&2
  exit 1
fi
echo "mcpb-pack: wrote $out"
