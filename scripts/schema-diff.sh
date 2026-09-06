#!/usr/bin/env bash
# Compares the built binary's tool surface with the released baseline in
# testdata/schemas-baseline.json.
#
# This script is the plumbing; internal/devcheck does the comparing and
# holds the reasoning.
set -euo pipefail
BIN=${1:-./google-chat-mcp}
BASELINE=${2:-testdata/schemas-baseline.json}

"$BIN" --dump-schemas > schemas.json

if [ ! -f "$BASELINE" ]; then
  echo "no baseline at $BASELINE; wrote schemas.json"
  exit 0
fi

exec go run ./internal/devcheck schema-diff "$BASELINE" schemas.json
