#!/usr/bin/env bash
# Prints one version's section of CHANGELOG.md, for the release notes.
#
# The entry is the release note. Generating notes from commit subjects
# instead would publish "fix(go): a deleted message reads back as a
# tombstone" to people deciding whether to upgrade, which is not who
# that sentence was written for.
#
#   scripts/extract-release-notes.sh 1.0.0 [CHANGELOG.md]
set -euo pipefail
VERSION=${1:?usage: extract-release-notes.sh VERSION [CHANGELOG]}
VERSION=${VERSION#v}
FILE=${2:-CHANGELOG.md}

# The link footer at the end of the file is not part of any section, but
# it follows the oldest one with no heading in between — so stop on it
# too, or the oldest entry's notes end with a block of compare links.
notes=$(awk -v want="## [$VERSION]" '
  index($0, want) == 1 { inside = 1; next }
  inside && /^## / { exit }
  inside && /^\[[^]]+\]: / { exit }
  # Skip the blank lines under the heading; $(...) strips the trailing
  # ones already.
  inside && !body && NF == 0 { next }
  inside { body = 1; print }
' "$FILE")

if [ -z "$notes" ]; then
  echo "no CHANGELOG section for $VERSION in $FILE" >&2
  exit 1
fi

printf '%s\n' "$notes"
