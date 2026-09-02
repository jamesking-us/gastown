#!/usr/bin/env bash
# isolation-coverage-guard.sh — static half of the cl-69h / cl-qaj3 control.
#
# test-isolation-guard.sh is the dynamic half: it runs a named set of packages
# and proves nothing escaped. But it can only watch the packages it is handed,
# and cl-69h shipped with four of them because those were the four anyone had
# thought to name. The list going stale is the actual failure mode here — a new
# package that reaches bd or Dolt is unisolated by default, and an unisolated
# test fails silently by writing to the operator's real HOME and production
# Dolt server.
#
# So this half never takes a list. It re-derives it: every package whose TEST
# BINARY transitively imports internal/beads, internal/doltserver or
# internal/rig must have a TestMain that calls testenv.IsolateProcessEnv().
#
#   scripts/guards/isolation-coverage-guard.sh
#
# Exits non-zero, naming the packages, if any of them does not. Compiles
# nothing and runs no tests; `go list` only loads the package graph.
#
# This is a floor, not a ceiling. A package can need isolation without
# importing any of those three — internal/tmux spawns tmux, and the shells and
# gt/bd commands tmux runs inherit this process's environment wholesale — so
# packages isolate voluntarily too, and that is not an error here. The import
# graph is only what can be derived mechanically.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

MODULE="github.com/steveyegge/gastown"

# A test binary that can reach any of these can reach bd, the beads SDK, or a
# Dolt server, which is the whole exposure.
SENSITIVE="$MODULE/internal/beads $MODULE/internal/doltserver $MODULE/internal/rig"

# Packages that must NOT be isolated by a TestMain, with the reason. Keep this
# list short and justified — every entry is a package whose tests can still
# reach the real HOME and the real Dolt server.
#
#   internal/testenv — it IS the isolation helper; its own test drives the
#   un-isolated -> isolated transition and a TestMain would erase the "before".
EXEMPT="$MODULE/internal/testenv"

echo "== deriving the package list (go list, no compilation)"
graph=$(go list -e -f '{{.ImportPath}}|{{len .TestGoFiles}}|{{len .XTestGoFiles}}|{{.Dir}}|{{join .Deps " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}' ./... 2>/dev/null)
if [[ -z "$graph" ]]; then
  echo "FAIL: go list produced nothing"
  exit 2
fi

needs=$(awk -F'|' -v sensitive="$SENSITIVE" -v exempt="$EXEMPT" '
  { path[NR]=$1; ntest[NR]=$2; nxtest[NR]=$3; dir[NR]=$4; deps[$1]=$5; timp[NR]=$6; ximp[NR]=$7; n=NR }
  END {
    split(sensitive, s, " ")
    split(exempt, e, " "); for (i in e) isExempt[e[i]]=1
    for (r = 1; r <= n; r++) {
      if (ntest[r] == 0 && nxtest[r] == 0) continue
      if (isExempt[path[r]]) continue
      # Closure of the test binary: the package, its test-only imports, and
      # everything each of those pulls in.
      closure = " " path[r] " " deps[path[r]] " " timp[r] " " ximp[r] " "
      split(timp[r] " " ximp[r], ti, " ")
      for (i in ti) if (ti[i] != "" && ti[i] in deps) closure = closure deps[ti[i]] " "
      for (i in s) if (index(closure, " " s[i] " ")) { print path[r] "|" dir[r]; break }
    }
  }' <<<"$graph")

if [[ -z "$needs" ]]; then
  echo "FAIL: derived an empty package list — the query is broken, not the tree"
  exit 2
fi

status=0
isolated=0
missing=()

while IFS='|' read -r pkg dir; do
  [[ -z "$pkg" ]] && continue
  if grep -qs -- 'testenv\.IsolateProcessEnv()' "$dir"/*_test.go; then
    isolated=$((isolated + 1))
  else
    missing+=("$pkg")
  fi
done <<<"$needs"

total=$(( isolated + ${#missing[@]} ))
echo "== $total packages reach bd/beads/Dolt from their tests; $isolated isolated"

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "FAIL: no TestMain calling testenv.IsolateProcessEnv() in:"
  printf '       %s\n' "${missing[@]}"
  echo
  echo "       Add, as the FIRST statement of the package's TestMain:"
  echo "           cleanup := testenv.IsolateProcessEnv()"
  echo "           code := m.Run()"
  echo "           cleanup()"
  echo "       and an isolation_guard_test.go calling testenv.AssertProcessEnvIsolated(t)"
  echo "       (AssertProcessEnvIsolatedWithDoltServer if the package starts a Dolt container)."
  echo "       If the package genuinely must not be isolated, add it to EXEMPT here with a reason."
  status=1
else
  echo "OK: every package that can reach bd or Dolt from its tests isolates its process"
fi

exit $status
