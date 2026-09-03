#!/usr/bin/env bash
# wisp-deletion-record-guard.sh — static control for hq-6ewp.
#
# The wisps table family is in dolt_ignore, so no wisp table is ever committed
# and `AS OF` answers table-not-found on it. A deleted wisp leaves no trace in
# Dolt at all: no history, no snapshot, no undo. The 1449 closed hq wisps
# destroyed on 2026-09-01 were never recovered, and the investigation into who
# destroyed them could name its actor only by reading source code.
#
# So every path that deletes from that family writes to one durable log first,
# through internal/wispaudit. The failure mode this guards is not the paths that
# exist today — those are covered and tested — but the NEXT one: a new deleter
# added in a year, by someone who never read the bead, which is unrecorded by
# default and silent about it.
#
#   scripts/guards/wisp-deletion-record-guard.sh
#
# It greps rather than parses, and it is deliberately crude: every file that can
# delete a wisp must also mention internal/wispaudit. A file that legitimately
# cannot (a pure query helper that happens to match) goes in EXEMPT with its
# reason. Compiles nothing and runs no tests.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

# Broad on purpose. A narrow pattern set only catches the deleters that exist
# today, and today's deleters are already covered — the risk is the next one.
# Anything that can issue a delete or purge verb, or a raw DELETE, is a
# candidate and has to be accounted for one way or the other.
PATTERNS=(
  '"delete",'
  '"purge",'
  'DELETE FROM'
)

# Candidates that cannot destroy an unrecoverable row. Every entry names why,
# and every entry must still match a pattern — a dead exemption is a lie about
# what the guard checked.
#
# The recurring reason: the `issues` table is NOT in dolt_ignore. Its rows are
# committed and readable back with AS OF, so deleting from it is recoverable and
# needs no side record. That is the whole distinction this guard turns on.
EXEMPT=(
  # deleteBead, used by the rig/queue/channel/group helpers — all `issues` rows.
  "internal/beads/beads.go"
  # `gt crew remove --purge` deletes an agent bead, an `issues` row.
  "internal/cmd/crew_lifecycle.go"
  # Declares the --purge flag for the above. Deletes nothing itself.
  "internal/cmd/crew.go"
  # `gt mail delete` — mail lives in `issues`.
  "internal/daemon/lifecycle.go"
  "internal/doctor/lifecycle_check.go"
  # Migrates misplaced ephemeral beads INTO the wisps table, then deletes the
  # `issues` rows it copied from. Nothing is destroyed.
  "internal/doctor/misclassified_wisp_check.go"
  # DELETE FROM dolt_branch_control — server plumbing, not beads at all.
  "internal/doltserver/doltserver.go"
  # Orchestrators: both call reaper.Purge, which writes the record. The match
  # here is a molecule step named "purge" and a cobra command named "purge".
  "internal/daemon/wisp_reaper.go"
  "internal/cmd/reaper.go"
)

is_exempt() {
  local f="$1" e
  for e in "${EXEMPT[@]}"; do
    [[ "$f" == "$e" ]] && return 0
  done
  return 1
}

echo "== finding files that can delete from the wisps family"

candidates=()
for pattern in "${PATTERNS[@]}"; do
  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    [[ "$f" == *_test.go ]] && continue
    [[ "$f" == internal/wispaudit/* ]] && continue
    candidates+=("$f")
  done < <(grep -rlE --include='*.go' -- "$pattern" internal cmd 2>/dev/null)
done

if [[ ${#candidates[@]} -eq 0 ]]; then
  echo "FAIL: matched no files at all — the patterns are broken, not the tree"
  exit 2
fi

# De-duplicate.
mapfile -t candidates < <(printf '%s\n' "${candidates[@]}" | sort -u)

missing=()
recorded=0
exempted=0
for f in "${candidates[@]}"; do
  if is_exempt "$f"; then
    exempted=$((exempted + 1))
    continue
  fi
  if grep -qs 'internal/wispaudit' "$f"; then
    recorded=$((recorded + 1))
  else
    missing+=("$f")
  fi
done

echo "== ${#candidates[@]} files can issue a delete; $recorded record it, $exempted exempt"

# A stale exemption hides a file that no longer exists or no longer matches, and
# makes the count above a fiction.
dead=()
for e in "${EXEMPT[@]}"; do
  found=0
  for f in "${candidates[@]}"; do
    [[ "$f" == "$e" ]] && found=1 && break
  done
  [[ $found -eq 0 ]] && dead+=("$e")
done
if [[ ${#dead[@]} -gt 0 ]]; then
  echo "FAIL: EXEMPT names files that no longer match any pattern — remove them:"
  printf '       %s\n' "${dead[@]}"
  exit 1
fi

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "FAIL: these delete wisps without writing the deletion record first:"
  printf '       %s\n' "${missing[@]}"
  echo
  echo "       A wisp deletion is unrecoverable: the wisps family is in dolt_ignore,"
  echo "       so nothing it removes is in any Dolt commit and no AS OF reads it back."
  echo "       Before the delete, call:"
  echo "           if err := wispaudit.Plan(actor, wispaudit.Path..., scope, db, wisps, nil); err != nil {"
  echo "               return err  // do NOT delete — an unrecordable deletion is the defect"
  echo "           }"
  echo "       and add a Path constant naming the new deleter (internal/wispaudit)."
  echo "       If the delete addresses the issues table, which HAS history, add the"
  echo "       file to EXEMPT here with that reason."
  exit 1
fi

echo "OK: every wisp deleter writes its deletion record first"
