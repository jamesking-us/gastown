#!/usr/bin/env bash
# test-isolation-guard.sh — positive control for cl-69h.
#
# The failure this guards against is silent: a test that escapes isolation
# writes a stray embedded Dolt store under the developer's real
# $HOME/.beads-planning, and creates real databases on whatever Dolt server the
# shell points at — on a Gas Town host, the production server. Nothing fails,
# nothing is logged; the debris is only found later, and `gt dolt cleanup` does
# not match it (the reaper's test-pollution prefixes are testdb_/beads_t/
# beads_pt/doctest_, and rig fixtures are named forkrig/testrig/testrip).
#
# So the check has to be external: snapshot both surfaces, run the tests, and
# assert neither moved.
#
#   scripts/guards/test-isolation-guard.sh [go-package ...]
#
# Exits non-zero if $HOME/.beads-planning appeared or changed, or if the set of
# databases on the Dolt server changed. Read-only with respect to the server —
# it never creates or drops anything.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

PACKAGES=("$@")
if [[ ${#PACKAGES[@]} -eq 0 ]]; then
  PACKAGES=(./internal/cmd/ ./internal/rig/ ./internal/proxy/ ./internal/plugin/)
fi

DOLT_HOST="${GT_DOLT_HOST:-127.0.0.1}"
DOLT_PORT="${GT_DOLT_PORT:-3307}"
BEADS_PLANNING="$HOME/.beads-planning"

# Serialize with every other heavy build on the box (Gas Town standing order).
BUILD_LOCK="${GT_BUILD_LOCK:-/tmp/.dotnet-build.lock}"

snapshot_home() {
  if [[ -e "$BEADS_PLANNING" ]]; then
    # Deep mtime, not just the top directory: bd writes inside .beads/.
    find "$BEADS_PLANNING" -printf '%T@ %p\n' 2>/dev/null | sort
  else
    echo "ABSENT"
  fi
}

snapshot_databases() {
  if ! command -v dolt >/dev/null 2>&1; then
    echo "UNAVAILABLE: no dolt client"
    return
  fi
  local out
  out=$(timeout 60 dolt --host "$DOLT_HOST" --port "$DOLT_PORT" \
    --user "${GT_DOLT_USER:-root}" --password "${GT_DOLT_PASSWORD:-}" --no-tls \
    sql -q "show databases" 2>/dev/null)
  if [[ -z "$out" ]]; then
    echo "UNAVAILABLE: no server on $DOLT_HOST:$DOLT_PORT"
    return
  fi
  printf '%s\n' "$out" | sed -n 's/^| \(.*[^ ]\) *|$/\1/p' | grep -v '^Database$' | sort
}

home_before=$(snapshot_home)
db_before=$(snapshot_databases)

echo "== baseline"
echo "   $BEADS_PLANNING: $(if [[ "$home_before" == "ABSENT" ]]; then echo absent; else echo "$(wc -l <<<"$home_before") entries"; fi)"
echo "   databases on $DOLT_HOST:$DOLT_PORT: $(tr '\n' ' ' <<<"$db_before")"

# internal/cmd is a big package and this box is shared; the default 10m
# per-package limit is a contention artifact more often than a real hang.
GO_TEST_TIMEOUT="${GO_TEST_TIMEOUT:-25m}"

echo "== running: go test -short -p 2 -timeout $GO_TEST_TIMEOUT ${PACKAGES[*]}"
flock -w 1800 "$BUILD_LOCK" go test -short -p 2 -timeout "$GO_TEST_TIMEOUT" "${PACKAGES[@]}"
test_status=$?

home_after=$(snapshot_home)
db_after=$(snapshot_databases)

status=0

if [[ "$home_before" != "$home_after" ]]; then
  echo "FAIL: $BEADS_PLANNING changed during the test run — a test escaped into the real HOME"
  diff <(printf '%s\n' "$home_before") <(printf '%s\n' "$home_after") | sed 's/^/       /'
  status=1
else
  echo "OK: $BEADS_PLANNING unchanged"
fi

if [[ "$db_before" == UNAVAILABLE:* ]]; then
  echo "SKIP: database check (${db_before#UNAVAILABLE: })"
elif [[ "$db_before" != "$db_after" ]]; then
  echo "FAIL: databases on $DOLT_HOST:$DOLT_PORT changed — a test reached a real Dolt server"
  diff <(printf '%s\n' "$db_before") <(printf '%s\n' "$db_after") | sed 's/^/       /'
  status=1
else
  echo "OK: databases on $DOLT_HOST:$DOLT_PORT unchanged"
fi

if [[ $test_status -ne 0 ]]; then
  echo "NOTE: the test run itself failed (exit $test_status); isolation verdict above is still valid"
  status=$test_status
fi

exit $status
