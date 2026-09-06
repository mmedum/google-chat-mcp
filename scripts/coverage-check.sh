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
# Derived, not listed: a package added under internal/ is under the floor
# by default. A hand-kept list lets a new package escape it silently,
# which is the opposite of what a floor is for. internal/version holds
# build stamps and has nothing worth covering.
#
# One exception is not in this list and cannot be, so it is written down
# instead: a package whose files are all behind a build tag has no files
# in a default build, so `go list` does not report it and the floor never
# sees it. internal/evals is that package. Saying "everything under
# internal/" while a whole package is invisible would be a gate claiming
# more than it does.
EXEMPT="internal/version"
for pkg in $($GO_LIST list -f '{{.ImportPath}}' ./internal/... | sed "s|^$MODULE/||"); do
  case " $EXEMPT " in *" $pkg "*) continue ;; esac
  pct=$(awk -v p="$MODULE/$pkg/" 'NR>1 && index($1, p)==1 {
      if (!($1 in stmts)) stmts[$1]=$2;
      if ($3>0) hit[$1]=1 }
    END { for (k in stmts) { total+=stmts[k]; if (k in hit) cov+=stmts[k] }
          if (total) printf "%.1f", 100*cov/total; else print "0" }' "$PROFILE")
  printf '%-22s %6s%%\n' "$pkg" "$pct"
  if awk -v a="$pct" -v b="$MIN" 'BEGIN {exit !(a < b)}'; then fail=1; fi
done
[ $fail = 0 ] || { echo "coverage below ${MIN}% in at least one core package"; exit 1; }
