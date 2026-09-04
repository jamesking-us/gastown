# Agent liveness: which instruments answer which question

Deciding whether a working agent is actually working is not a status lookup in
Gas Town. Several fields look like the answer and are not, and acting on them
has repeatedly come within one step of killing healthy work. This page records
which instrument answers which question, and — more usefully — which direction
each one may be read in.

The governing bead is cl-2sp, which collected ten distinct instrument failures.
They share one shape: **every lying instrument was confidently right about the
wrong object, not wrong about the right one.** That is why adding instruments
does not help by itself. Readings that share a wrong referent all agree with
each other and corroborate into a confident falsehood.

## The one rule

> **A recent write proves an agent is working. Silence proves NOTHING.**

Reading, thinking, and waiting on a child process all write nothing. Every
instrument below is trustworthy in one direction only, and the untrustworthy
direction is always the one that authorizes a destructive action.

## Do not use these to judge activity

### `last_activity` (removed)

`gt polecat status --json` emitted `last_activity` until cl-2sp. It was a copy
of the session creation time. Measured on 2026-09-01 across three live polecats
created 36, 31 and 9 minutes apart, `last_activity` equalled `created_at` to the
second in all three, including one that was demonstrably mid-turn. The key is
now `tmux_session_activity`, named for what it actually reports.

### tmux `session_activity`

Also session age. Measured across ten live sessions on the same box: zero had
`session_activity != session_created`. The decisive control was a seat running
the query from *inside its own session while executing commands* and getting a
delta of zero.

Two further traps in the same family:

- **tmux session creation time is not an agent's age.** A tmux session outlives
  the `claude` process inside it, so an agent that restarted thirty seconds ago
  reads as hours old. Use the process start time (`ps -eo pid,lstart,etime,args`
  on the agent process), which also gives you the launcher and launch reason.
- **A delta on a field that never moves can never fire.** The Deacon health
  check used a `session_activity` delta as its secondary response signal and
  described it in the source as reliable. It was dead code, and it failed toward
  "did not respond" — which escalates a healthy agent.

### The pane, read by eye

Direct observation is also an instrument and also needs a control.

- `Press up to edit queued messages` means **mid-turn with input queued**, not
  idle at a prompt. A witness read it as idle and escalated for a restart of an
  agent that was forty minutes into a working turn with uncommitted changes.
- The footer's `N shell` token is a sound positive (something is running, do not
  restart) and an unsound negative: a polecat two hours into a generating turn
  presented the neither-token "idle" footer while its token count climbed
  112.1k → 117.4k.

## Use these

### Worktree writes — `gt polecat activity`

```bash
gt polecat activity <rig>              # survey, quietest first
gt polecat activity --all --json       # for tools
gt polecat status <rig>/<polecat>      # one seat, with the file named
```

Reports when a file was last written under a polecat's worktree, and names the
file. Implemented in `internal/worktreewrite`; consumed by `gt polecat status`,
`gt polecat activity`, the Witness stall detector and the Deacon health check.

- **Positive:** a recent write is reliable evidence of work.
- **Negative:** none. Measured healthy agents reading dependency source in the
  module cache (1074s and later 6061s quiet), mid-analysis of failing tests
  (1h23m quiet), and blocked on the build lock (0-byte output file, stale mtime)
  all read as silent here.
- **Scope:** `.git`, `.beads` and `.runtime` are excluded, because writes there
  come from another seat's push, from the beads daemon, and from gt's own
  session startup respectively. Build outputs (`obj/`, `bin/`, `node_modules`)
  are deliberately **not** excluded — that is where a compiling agent's work
  lands.
- **Blind spot:** work performed outside the worktree is invisible. Reading
  source elsewhere, querying beads, and builds whose outputs land in a shared
  cache all look identical to a dead session.

### The status-line token counter

Sample it twice, seconds apart, via `gt peek`. A **rising** count is generation
in progress and settles the question immediately. A static count while the
elapsed clock runs is the actual hang signature — duration alone cannot
distinguish a working turn from a wedged one, because a wedged session's clock
keeps running too.

### A captured child pid

An agent blocked on a live pid it is waiting for is healthy, and shows no writes
and no token movement. Confirm with `ps -eo etimes,args`. Beware self-matching
poll patterns: a `pgrep` pattern that matches the polling command itself can
never return empty (cl-d77p).

**Prefer a pid you already hold over a pattern search.** `until ! kill -0
$PID; do sleep N; done` cannot self-match — the check names an integer, not a
pattern that can also describe the checker's own command line. `until ! pgrep
-f "PATTERN"; do sleep N; done` embeds PATTERN inside its own `bash -c`
argument, so if PATTERN also describes that wrapper, the loop matches itself
and never terminates. Five monitor loops did exactly this to each other on
2026-09-03 (chrome, cl-st8u): unbounded, mutually self-sustaining, 40 minutes
lost. The same shape produces the opposite failure in a health check rather
than a wait: `pgrep -f "gt nudge-poller hq-deacon"` run to verify that poller
matched the *verifying* shell's own command line and read a dead poller as
healthy (boot, hq-d16k) — a false all-clear instead of a hang, from the same
defect. If a pattern search is unavoidable, exclude the checker's own PID and
its parent's, and don't trust a bare hit: `kill -0`/`Signal(0)` also succeeds
against a zombie (exited, not yet reaped) process, so confirm the candidate's
`ps -o stat=` isn't `Z` before believing it. `internal/procutil` centralizes
both guards (`IsAlive`, `FindByPattern`) for any Go call site that needs one.

## Co-timing dominates all of them

Before diagnosing several quiet agents one at a time, ask whether they stopped
**together**. On 2026-09-03 four agents stopped within 0.6 seconds of each other
on a shared upstream API outage. Every per-agent instrument read "stranded" on
all four and `gt status` called them healthy; a watcher working seat-by-seat
would have nudged four corpses and learned nothing. When several seats stop in
the same second the cause is shared — an outage, a lock, a filesystem — and no
per-agent signal will ever say so.

`gt polecat activity` reports co-timing clusters for this reason. A cluster is a
prompt to ask what those seats share, never a diagnosis: a convoy dispatched
together and finishing together looks the same.

## Deciding whether to intervene

1. `tmux has-session` absent → **dead**.
2. Recent worktree write (`gt polecat activity`) → **working**. Stop here.
3. Footer carries `N shell` → something is running. Do not restart.
4. `gt peek` twice: token count rising → **healthy**; static while the clock
   runs → **hung**.
5. No turn line and messages queued → check for a live child process before
   concluding anything. A polecat waiting on the build lock is the expected
   steady state, presents exactly like a startup wedge on screen, and the
   difference is entirely off-screen.
6. Only after all of the above: **wedged**, and restart only if its hook is
   loaded — an empty-hook restart produces the cl-3df cascade.

A quiet worktree is grounds to **look**, never to act. The cost of being wrong
is asymmetric: a wrongful restart kills an in-progress turn and discards
uncommitted work, while a missed stall costs one more patrol tick.

## Related

- `docs/concepts/heartbeats.md` — the three heartbeat stores, which are
  self-reported and only advance when an agent runs a gt command.
- `docs/concepts/polecat-lifecycle.md` — states and transitions.
