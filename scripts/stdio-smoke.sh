#!/usr/bin/env bash
# Drives the binary over stdio without credentials: initialize, list tools,
# and call a tool (which must answer with an [auth] tool error, not crash).
set -euo pipefail
BIN=${1:-./google-chat-mcp}
TMP=$(mktemp -d)
trap 'rm -r -f "$TMP"' EXIT
# A temp dir sits outside the home directory, so the smoke run opts past
# the guard BaseDir applies to a real override.
export GCM_CONFIG_DIR="$TMP/cfg" GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME=1 GCM_LOG_LEVEL=error
{
  echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
  echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  echo '{"jsonrpc":"2.0","id":4,"method":"resources/templates/list"}'
  echo '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_spaces","arguments":{}}}'
  sleep 1
} | timeout 20 "$BIN" > "$TMP/out.jsonl"
grep -q '"id":1' "$TMP/out.jsonl" || { echo "no initialize response"; cat "$TMP/out.jsonl"; exit 1; }
grep -q '"name":"list_spaces"' "$TMP/out.jsonl" || { echo "list_spaces missing from tools/list"; exit 1; }
grep -q '"name":"whoami"' "$TMP/out.jsonl" || { echo "whoami missing from tools/list"; exit 1; }
grep '"id":4' "$TMP/out.jsonl" | grep -q 'gchat://spaces/{space_id}' || { echo "resource templates missing"; exit 1; }
grep '"id":3' "$TMP/out.jsonl" | grep -q '"isError":true' || { echo "tool call without credentials should be a tool error"; cat "$TMP/out.jsonl"; exit 1; }
grep '"id":3' "$TMP/out.jsonl" | grep -q '\[auth\]' || { echo "tool error should carry the [auth] class"; exit 1; }
while read -r line; do
  case "$line" in
    '{"jsonrpc":'*) ;;
    "") ;;
    *) echo "non-JSON-RPC line on stdout: $line"; exit 1 ;;
  esac
done < "$TMP/out.jsonl"
echo "stdio smoke ok"
