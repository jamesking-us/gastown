# Dolt Health Guide

This guide covers evidence capture for Dolt outages and Gas Town behavior
mismatches that look like Dolt trouble.

## When To Use This

Use this checklist when any of these happen:

- `bd` commands hang, time out, or return unexpected empty results.
- `gt dolt status` reports unhealthy server state, high latency, stale PIDs, or
  orphan test databases.
- A Gas Town command behaves differently from its documented or expected behavior
  and Dolt is part of the control path.

Do not restart Dolt before collecting diagnostics. A blind restart can destroy the
state needed to explain the incident.

## Immediate Diagnostics

Capture non-fatal diagnostics first:

```bash
gt dolt dump 2>&1 | tee /tmp/dolt-hang-$(date +%s).log
gt dolt status 2>&1 | tee /tmp/dolt-status-$(date +%s).log
```

Then escalate with the evidence path:

```bash
gt escalate -s HIGH "Dolt: <symptom>" -m "Evidence: /tmp/dolt-status-..."
```

## RCA Capture Checklist

Attach this checklist to the escalation body, the follow-up bead, or the war-room
entry. Use `N/A` only when a field truly does not apply to a non-Dolt behavior
mismatch.

```markdown
### RCA Capture

- Trigger command:
- Concurrent GT processes:
- Dolt pid/status:
- Stale pid status:
- Orphan test server status:
- Suspected GT code path:
- Expected behavior:
- Observed behavior:
- Evidence source:
- Likely root cause:
- Smallest fix direction:
```

## Field Notes

- **Trigger command**: the exact command or agent action that exposed the issue.
- **Concurrent GT processes**: active mayor, witness, refinery, polecat, dog, or
  test processes that may share Dolt.
- **Dolt pid/status**: server PID, health, latency, and port state from
  `gt dolt status` or `gt dolt dump`.
- **Stale pid status**: whether pid files point at missing or unrelated processes.
- **Orphan test server status**: orphan database or test-server count, especially
  `testdb_*`, `beads_t*`, `beads_pt*`, or `doctest_*`.
- **Suspected GT code path**: command, package, plugin, or template that most
  likely drove the behavior.
- **Expected behavior**: what the command or workflow should have done.
- **Observed behavior**: what actually happened, including errors and timings.
- **Evidence source**: log files, command output, bead IDs, session IDs, or branch names.
- **Likely root cause**: current best explanation, clearly marked if uncertain.
- **Smallest fix direction**: the least invasive code, docs, or operations change
  that would prevent repeat incidents.

## Wisps Have No Dolt History — Read the Deletion Log Instead

Do not reach for `AS OF` when investigating a missing wisp. It will not work,
and the reason is not a bug you can wait out.

The whole wisps table family — `wisps`, `wisp_comments`, `wisp_labels`,
`wisp_events`, `wisp_dependencies`, `wisp_child_counters` — is in `dolt_ignore`.
No wisp table is ever committed, so:

```sql
SELECT count(*) FROM wisps AS OF 'HEAD';   -- Error 1146: table not found
SELECT count(*) FROM issues AS OF 'HEAD';  -- works: AS OF is not broken
```

There is no undo, no snapshot, and no `dolt_log` entry behind any wisp deletion.
`compaction_snapshots` and `issue_snapshots` hold no wisp rows; `wisp_events`
cannot record a deletion because a deleted wisp's events cascade away with it,
so its silence is not evidence. This was checked, not assumed (hq-6ewp), and it
is why the 1449 closed hq wisps destroyed on 2026-09-01 were never recovered.

The ignore is deliberate and is staying: wisp mutation volume exceeds the town
database's entire commit volume, so un-ignoring the family would multiply Dolt
commit traffic several-fold against a store that has already needed its history
flattened. The record lives beside the ignore instead.

**So the deletion log is the evidence.** Every path that deletes a wisp — the
`gt done` molecule purge, `gt polecat nuke`, `gt compact`'s TTL delete,
`gt dolt sync --gc`, `gt maintain`, the reaper purge (daemon patrol and
`gt reaper purge`), and `gt patrol`'s digest cleanup — writes to
`<town>/.events.jsonl` **before** deleting, and does not delete at all if the
record cannot be written.

```bash
# What happened to a particular wisp:
grep hq-wisp-b8pbe ~/gt/.events.jsonl | jq .

# Everything a deleter removed, most recent first:
jq -c 'select(.type=="wisp_purge")
       | {ts, actor, path: .payload.path, phase: .payload.phase,
          db: .payload.db, count: .payload.count}' ~/gt/.events.jsonl | tail -20

# Names and titles of what one record removed:
jq -r 'select(.type=="wisp_purge" and .payload.phase=="planned")
       | .payload.wisps[] | "\(.id)\t\(.title // "")"' ~/gt/.events.jsonl
```

Records come in pairs. `phase: "planned"` is written before the delete and is
the one that survives a crash mid-purge; `phase: "completed"` says what actually
went. `path` names which deleter acted — the question a wisp-loss investigation
has to answer first, and the one that previously could only be answered by
reading source code. `predicted: true` marks the paths that go through
`bd purge`, which reports a count and never the ids, so their set is enumerated
beforehand rather than observed.

Adding a new wisp deleter without a record is a CI failure, not a review catch:
`scripts/guards/wisp-deletion-record-guard.sh`.

## Simulated Incident Smoke Check

For documentation-only RCA work, use this smoke check to verify the checklist is
available and wired into the escalation path:

```bash
test -f docs/dolt-health-guide.md
grep -n "Trigger command" docs/dolt-health-guide.md
grep -n "RCA capture checklist" internal/templates/townroot/claude.md docs/design/escalation.md
```
