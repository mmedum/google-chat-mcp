#!/usr/bin/env bash
# Enforces a statement-coverage floor per core package. The profile comes
# from -coverpkg=./internal/... so cross-package coverage counts; blocks
# then appear once per test binary and are de-duplicated here.
set -euo pipefail
PROFILE=${1:-cov.out}
MIN=${2:-80}
MODULE=github.com/mmedum/google-chat-mcp
GO_LIST=${GO:-go}
fail=0
# Derived, not listed: a package added under cmd/ or internal/ is under
# the floor by default. A hand-kept list lets a new package escape it
# silently, which is the opposite of what a floor is for.
# internal/version holds build stamps and has nothing worth covering.
#
# cmd/ is in the list because it was missing from it: the floor read
# ./internal/... only, so the command package sat at 32% against an 80%
# floor every other package cleared, and nothing could report it because
# it was outside -coverpkg as well. google-sheets-mcp found the identical
# hole in its own tree. The rule this cost us: a derived list is only as
# trustworthy as the root it derives from, and this one derived from one
# of the two trees that ship.
#
# One exception is not in this list and cannot be, so it is written down
# instead: a package whose files are all behind a build tag has no files
# in a default build, so `go list` does not report it and the floor never
# sees it. internal/evals is that package. Saying "everything under
# internal/" while a whole package is invisible would be a gate claiming
# more than it does.
EXEMPT="internal/version"

# A floor of its own, not an exemption, and the difference is the point:
# an exempt package is invisible, and invisible is how cmd/ sat at 32%
# without anyone knowing. This one is printed on every run.
#
# What is left uncovered there is the loopback OAuth flow, the serve loop,
# the live doctor walk and the userinfo lookup — each needing a network or
# a process seam the shipped code does not have. They are covered, but by
# scripts/stdio-smoke.sh and by live runs rather than by unit tests. The
# number is set just under what the package holds today so it ratchets:
# it may go up and may not go down. Raising it means adding those seams,
# which is a change to shipped code and wants its own review.
FLOOR_cmd_google_chat_mcp=55

for pkg in $($GO_LIST list -f '{{.ImportPath}}' ./cmd/... ./internal/... | sed "s|^$MODULE/||"); do
  case " $EXEMPT " in *" $pkg "*) continue ;; esac
  # Per-package override, named after the path with the separators
  # flattened. Absent means the ordinary floor.
  override_var="FLOOR_$(printf '%s' "$pkg" | tr -c 'A-Za-z0-9' '_')"
  floor=${!override_var:-$MIN}
  # The directory exactly, not a prefix. Matching on "$pkg/" scores a
  # package on its subpackages too, so a parent's printed number
  # describes neither package — google-sheets-mcp had a package reported
  # at 68.7% whose own coverage was 90.1%, once a subpackage grew. No
  # package here has a child today, which is exactly why this would have
  # gone unnoticed until one did.
  pct=$(awk -v want="$MODULE/$pkg" 'NR>1 {
      file = $1; sub(/:.*/, "", file);
      dir = file; sub(/\/[^\/]*$/, "", dir);
      if (dir != want) next;
      if (!($1 in stmts)) stmts[$1]=$2;
      if ($3>0) hit[$1]=1 }
    END { for (k in stmts) { total+=stmts[k]; if (k in hit) cov+=stmts[k] }
          if (total) printf "%.1f", 100*cov/total; else print "0" }' "$PROFILE")
  if [ "$floor" != "$MIN" ]; then
    printf '%-22s %6s%%  (floor %s%%)\n' "$pkg" "$pct" "$floor"
  else
    printf '%-22s %6s%%\n' "$pkg" "$pct"
  fi
  if awk -v a="$pct" -v b="$floor" 'BEGIN {exit !(a < b)}'; then fail=1; fi
done
[ $fail = 0 ] || { echo "coverage below its floor in at least one core package; each package is printed above with the floor it has to clear"; exit 1; }
