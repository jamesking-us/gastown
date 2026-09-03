# Heartbeats

Gas Town has **three distinct heartbeat stores**. They have different readers
and thresholds, so Deacon heartbeat commands refresh the Deacon-specific stores
together to avoid false "stuck agent" escalations (see hq-qxl9: a Deacon
refreshed its session heartbeat while the file store aged past threshold).

## The three stores

### 1. Deacon heartbeat file — `<townRoot>/deacon/heartbeat.json`

- **Written by:** `gt deacon heartbeat [action]` and `gt heartbeat` when
  `GT_ROLE=deacon` → `deacon.Touch()` / `deacon.TouchWithAction()`
  (`internal/deacon/heartbeat.go`).
- **Read by:** the stuck-agent-dog plugin (parses the JSON `timestamp`, falling
  back to mtime for malformed legacy files, and cross-checks tmux activity
  before escalating) and the Go daemon (`deacon.ReadHeartbeat`; thresholds 5m
  stale / 20m very-stale → poke).
- **Also touches:** the legacy `deacon/.deacon-heartbeat` mtime file for old
  shell scripts.

### 2. Session heartbeat (per-session state store)

- **Written by:** `gt heartbeat [--state=working|idle|exiting|stuck]` →
  `polecat.TouchSessionHeartbeatWithState()`. Requires `GT_SESSION`.
- **Read by:** the Witness, which reads the self-reported state instead of
  inferring liveness from timers (ZFC: gt-3vr5). This is the store polecats
  refresh.

### 3. Agent-bead label — `heartbeat:<EPOCH>` on the agent bead (e.g. `hq-deacon`)

- **Written by:** `gt mol await-signal` on each timeout/signal wake
  (`updateAgentHeartbeat` in `internal/cmd/molecule_await_signal.go`). A
  label rewrite is used because `bd agent heartbeat` was never shipped
  (steveyegge/beads#2828). Deacon heartbeat commands also sync this label when
  it is older than half of the stale threshold.
- **Read by:** Witness second-order monitoring ("who watches the watchers"):
  Witnesses check the Deacon's bead activity and alert the Mayor if it looks
  unresponsive (>5 minutes per the patrol formula).
- **Gotcha:** a session that never reaches `await-signal` (handoff churn,
  session limits, one very long patrol turn) leaves this label stale for
  hours even though the agent is healthy.

## Reporting contract (hq-huln)

A heartbeat command may skip a store, but it may never report success for a
store that did not write. `gt deacon heartbeat` printed `✓ Heartbeat updated`
off store 1 alone while store 3 was skipped by its throttle or failing against
bd; the Deacon that found it watched its own liveness label sit frozen through
five cheerful invocations.

- **Say which stores are behind the ✓.** `gt deacon heartbeat` prints
  `✓ Heartbeat updated (file, bead label 1788382163)`, or
  `(file, bead label fresh, next refresh in 44s)` when the throttle skipped the
  refresh. A skip is a legitimate outcome — an unreported skip is not.
- **A store that fails is a command that fails.** Any store error exits
  non-zero with `✗ Heartbeat INCOMPLETE`, carrying bd's own stderr. The stores
  fail independently, so the message names each one.
- **Read the label back.** `bd update` exiting 0 is not evidence the label
  landed, and store 3 is the one Witness second-order monitoring reads. The
  bead sync re-reads it and fails loudly unless the stored epoch is at least
  the one just written — a *newer* epoch passes, because `await-signal` may
  legitimately refresh the same label in between.

The throttle on store 3 stays: each refresh is a Dolt commit. It is half the
stale threshold (150s), which is shorter than an ordinary patrol cycle and
longer than a burst of manual probes — so under fast probing the label looks
frozen while nothing is wrong. That is a reason to report the skip, not to
remove the throttle.

## Rules of thumb

- **Deacon sessions:** `gt deacon heartbeat` refreshes the Deacon file and
  throttled bead label. `gt heartbeat` also refreshes the session store and,
  when `GT_ROLE=deacon`, uses the same Deacon file/label sync path.
- **Polecats / Witness / Refinery:** `gt heartbeat` (session store) is the
  one that matters. It reports its own write failure: the same
  success-over-a-failed-write shape lived in store 2 for every role, since
  `TouchSessionHeartbeatWithState` drops errors. Callers that print nothing
  still use that best-effort form; callers that claim the heartbeat was
  updated use `TouchSessionHeartbeatWithStateErr`.
- **Monitoring scripts:** never declare an agent stuck from a single store.
  Cross-check tmux session activity (`tmux display-message -p
  '#{window_activity}'`) before escalating — a live session with a stale
  store is *heartbeat-write divergence*, not a stuck agent. The
  stuck-agent-dog plugin does this since hq-qxl9.
