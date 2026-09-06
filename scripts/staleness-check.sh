#!/usr/bin/env bash
# Fails when the documentation drifts from the code.
#
# This script is the plumbing; scripts/gates answers anything that
# needs to read a tool schema or know what the code defines.
set -euo pipefail
BIN=${1:-./google-chat-mcp}
TMP=$(mktemp -d)
trap 'rm -r -f "$TMP"' EXIT
fail=0

"$BIN" --dump-schemas > "$TMP/schemas.json"

# The README's tool table must list exactly the tools that ship.
shipped=$(go run ./scripts/gates tool-names "$TMP/schemas.json")
readme_tools=$(grep -oE '^\| `[a-z_]+` \|' README.md | tr -d '`| ' | sort || true)
if [ "$shipped" != "$readme_tools" ]; then
  echo "README tool table differs from the registered tools:"
  diff <(echo "$shipped") <(echo "$readme_tools") || true
  fail=1
fi

# docs/configuration.md must name every GCM_ variable the server reads.
for var in $(go run ./scripts/gates config-vars); do
  if ! grep -q "$var" docs/configuration.md; then
    echo "docs/configuration.md does not mention $var"
    fail=1
  fi
done

# docs/gcp-setup.md must list every scope login asks for. A scope missing
# from the consent screen is not granted, and the tool that needs it
# fails with a 403 that names it.
for scope in $(go run ./scripts/gates scopes); do
  if ! grep -q "$scope\$" docs/gcp-setup.md; then
    echo "docs/gcp-setup.md does not list the scope $scope"
    fail=1
  fi
done

# Source that changed since the last tag has to be written down
# somewhere in CHANGELOG.md.
#
# Usually that means content under [Unreleased]. A release commit is the
# exception: it moves that content under a version heading, and CI runs
# on the release pull request before the tag exists. Requiring
# [Unreleased] content unconditionally would fail every release, which
# is how the sibling google-docs-mcp repository discovered this — its
# gate blocked its own release pull request.
last_tag=$(git describe --tags --abbrev=0 2>/dev/null || true)
# Tests are excluded because a change confined to _test.go files cannot
# change what the binary does, and CONTRIBUTING says an internal change
# with no user-visible effect gets no entry. Without the exclusion the
# gate demands a release note for a test, and the only way to satisfy it
# is to write one that lies about what shipped.
if [ -n "$last_tag" ] &&
  ! git diff --quiet "$last_tag" -- cmd internal ':(exclude)*_test.go' 2>/dev/null; then
  # Unreleased is just another section name, so the release-notes
  # extractor answers this too rather than a second awk that has to
  # agree with it about where a section ends.
  unreleased=$(go run ./scripts/gates release-notes Unreleased 2>/dev/null || true)
  # `|| true` because a CHANGELOG with no released version in it yet is a
  # real state — the first release is exactly that — and without it the
  # empty grep takes pipefail and set -e with it, killing this script
  # with no message at all.
  newest=$(grep -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | head -1 |
    sed -E 's/^## \[(.*)\]$/\1/' || true)
  if [ -z "$unreleased" ] && [ "$newest" = "${last_tag#v}" ]; then
    echo "source changed since $last_tag but CHANGELOG.md documents nothing new:"
    echo "  put it under [Unreleased], or under the heading for the release being cut"
    fail=1
  fi
fi

if [ $fail = 0 ]; then
  echo "staleness ok"
fi
exit $fail
