# Nudge delivery: two transports, one queue

A nudge is written to a per-session queue directory and drained later. There
are two drainers, and confusing them costs real messages. This page records
which seats use which, why every seat currently uses both, and the three
delivery defects fixed under gt-sve (the gastown half of hq-z5eb).

## The two transports

| | **Hook drain** | **Injection drain** |
|---|---|---|
| Who | The agent's own `UserPromptSubmit` hook, via `gt mail check --inject` | The background `gt nudge-poller <session>` process, and the `--mode=wait-idle` idle watcher |
| Mechanism | Prints the formatted batch on stdout; the harness folds it into the agent's turn | Types the formatted batch into the agent's tmux pane with `send-keys`, then presses Enter |
| Touches tmux | No | Yes |
| Can strand text in a composer | No | Yes — this is the entire risk surface |
| Fires when | The agent submits a prompt | Every poll interval (default 10s) |

Both call `nudge.Drain`, which claims each file by rename, so they cannot
double-deliver a given entry.

## Why every seat has both

The hook alone deadlocks. It fires only when the agent submits a prompt, so an
agent sitting idle at its prompt waiting for work never drains: the agent waits
for a nudge and the nudge waits for the agent (gt-dgf). The poller exists to
break that cycle, and for that reason `crew`, `witness`, `refinery` and
`deacon` start one for **every** agent, not only for runtimes that lack a hook.

The consequence is that **no seat is hook-only**. Measured on this town
(2026-09-05, 14 live sessions): every session had a poller pidfile, and every
configured agent (`worker`, `dog-worker`, `fable-max`, `sonnet-vertex`) is a
Claude wrapper that also drains via the hook. Blast radius for any injection
defect is therefore the whole town, and "which seats are unaffected" has the
answer *none* until the poller's role is narrowed.

Narrowing it is not as simple as skipping the poller for Claude seats: that
reintroduces the gt-dgf deadlock for idle agents. The redundancy is load-bearing
in one direction only — the hook cannot cover idleness, and the poller cannot
cover safety.

### A second measured consequence

The poller gates injection on idleness only for runtimes with prompt detection:

```go
hasPromptDetection = preset.ReadyPromptPrefix != ""
// ...
if shouldSkipDrainUntilIdle(hasPromptDetection, waitErr) { continue }
```

None of this town's agent presets sets `ready_prompt_prefix`, so
`hasPromptDetection` is false on every seat and a failed `WaitForIdle` does not
stop the drain. Injection therefore happens on the poll interval whether or not
the agent is idle. Setting `ready_prompt_prefix` on the town's presets is the
cheapest way to buy the idle gate back.

## The three defects (gt-sve)

### 1. Literal text parsed as flags

`tmux` parses a subcommand's arguments with `getopt(3)`, which keeps scanning
for flags after `-l`. Without a `--` terminator, literal text beginning with
`-` is read as flags and the command fails outright:

```
$ tmux send-keys -t pane -l '-u hello'
command send-keys: unknown flag -u
$ tmux send-keys -t pane -l -- '-u hello'      # delivers the text
```

Nudge text reaches `send-keys` as fixed-size 512-byte chunks, so *which* byte
lands first in a chunk is an accident of message length. A batch whose boundary
falls before `-urgent` reports `unknown flag -u`; one that splits
`<system-reminder>` reports `-r`. Because a retried batch is byte-identical,
the same boundary reproduces the same error on every attempt — which is how one
bad split filled a poller log with 39 identical errors out of 41 lines.

The searched-for "`-u` in tmux subcommand position" was never a hardcoded
argument. `BuildCommandContext` and `Tmux.commandContext` build the global `-u`
correctly and were rightly exonerated. The bug was the *absence* of `--`.

Fixed by routing every send that carries caller-supplied text through
`Tmux.sendKeysText`, which adds the terminator once so no future call site can
omit it. `sendKeysLiteral` is the `-l` form of that helper.

The literal sends are where the failure was measured, but `--` is not about
`-l`: `send-keys` parses its whole argument list with getopt, so the key-name
position has the same defect whenever the argument can vary at runtime. This
is how "`SendKeysDebounced` uses only valid flags (`-t`, `-l`, then a separate
Enter)" came to be a true statement that cleared the wrong thing. **The flags
were never the problem; the data was.** Enumerate call sites by pattern, not by
reading, and audit the argument position rather than the flag list.

Routed through the helper (all carry runtime text):

| Site | Path |
|---|---|
| `sendMessageToTarget` chunk loop | poller / idle watcher / `gt nudge` |
| `sendKeysLiteralWithRetry` | first chunk and short messages |
| `SendKeysDebounced` | most agent prompts |
| `SendKeysRaw` | exported; key-name position |
| `NewSessionWithCommand` / `NewSessionWithCommandAndEnv` (Windows psmux) | session startup command |
| `RespawnPane` / `RespawnPaneWithWorkDir` (Windows psmux) | hot reload command |

> **Unverified on Windows.** The four psmux sites are included because the
> rule is "every text-carrying site, one helper" and a carve-out is how the
> next one hides. But psmux is a separate tmux-compatible shim with no source
> in this tree, so its handling of `--` was not tested — only real tmux 3.4
> was. Nearly every argument parser honours `--`; if psmux does not, it would
> type a stray `--` ahead of the startup command. Worth a Windows check before
> anyone relies on it.

Audited and deliberately **not** routed: the seventeen remaining
`t.run("send-keys", …)` calls pass only compile-time key-name constants
(`Enter`, `Escape`, `C-u`, `C-j`, `Down`, `-X cancel`). They cannot vary at
runtime, so they cannot carry a leading `-`. Re-run the audit with:

```
grep -rn 't.run("send-keys"' --include='*.go' internal/ | grep -v _test.go
```

Any hit whose trailing argument is a variable belongs in the helper.

### 2. Retry-restaging into panes

A chunked send that fails partway does not fail cleanly: every chunk before the
rejected one is already sitting unsubmitted in the agent's composer. The old
poller called `nudge.Requeue` on injection failure, so the same batch came back
on the next poll and typed another fragment on top of the last — roughly every
12 seconds, bounded only by the 30-minute TTL, so about 150 attempts. That is
where piles of staged text came from (hq-r77q, gt-pdf, hq-0p1l), and why one
witness seat took a single 8-9 message batch as ~25 stacked copies the moment
something pressed Enter (hq-8nll, gt-h9d).

A fragment is not inert. The next Enter from **any** source — a later nudge,
the agent, a person glancing at the pane — submits the truncated text as if the
sender had written it (cl-jkr).

The rule is now **deliver-once-or-fail**:

- A failed batch goes back to the queue via `nudge.RequeueHookOnly`, which sets
  `injection_failed` on each entry.
- `nudge.DrainInjectable` (used by every tmux injector) leaves marked entries
  in the queue; `nudge.Drain` (the hook path) still returns them, because
  printing into a turn cannot strand anything.
- `nudge.PendingInjectable` excludes them too, so the poller does not spin on
  work it will never take.
- A partial send is reported as `errPartialStage`, and the nudge path clears
  the composer before returning, so the failure cannot arm a later Enter.

Marking rather than discarding matters as much as the rest. An undelivered
nudge is not noise: the ACK that sat undelivered for 23 minutes under hq-z5eb
was a polecat correcting an error in its supervisor's instruction, and it was
right.

### 3. Poller respawn trusting the pidfile

A zombie answers a bare `kill(pid, 0)` probe successfully — the kernel keeps
its PID allocated until the parent reaps it. `pollerAlive` used exactly that
bare probe, so a pidfile naming a corpse read as "poller running" and
`StartPoller`'s already-running fast path returned it instead of respawning.
The seat then had no poller at all until someone noticed by hand.

Measured three times: `gastown/witness` at PID 3507 (`STAT=Z`, dead 55 minutes
while `.runtime/nudge_poller/gt-witness.pid` still named it), a second witness
seat, and `cl-witness` at PID 3512 during this fix. `pollerAlive` and
`StopPoller` now use `procutil.IsAlive`, which confirms the state is not zombie
(cl-d77p).

Liveness is only half the question. `procutil.IsAlive` establishes that the
*PID* is running; `pollerProcessMatches` establishes that the *process* behind
it is this session's poller, by reading its command line. The two failures
arrive by the same route: the 21:25Z container restart replayed the low PID
range while the pidfiles still named PIDs from the previous boot, leaving 7 of
14 seats pointing at processes that no longer existed. Any one of those numbers
being reissued to an unrelated process would pass every liveness probe and pin
the seat at "already running" for as long as that process lived. The identity
check fails **open** — if `ps` cannot answer, the liveness result stands, since
a false "not ours" costs a duplicate poller on every hiccup.

**Verify liveness against the process, never against the pidfile.** Pidfiles in
`.runtime/nudge_poller/` are stale by design — a town-wide audit found 47 of
them against 12 live seats — so pidfile count is not a health metric.

### How much item 3 costs, so it is not over-scoped

`gt nudge` starts the poller for its target, so a seat with a dead poller is
not permanently deaf: the next inbound nudge revives the channel. What the
pidfile-vs-process bug actually costs is the backlog sitting in the queue at
the moment of death, plus everything that expires before the next inbound
nudge arrives.

## What is proven, and what is not

The three defects above are all source-verified and independently
reproducible. Their *relative contribution to the 2026-09-05 outage* is not
settled, and this section exists so nobody reads more into the fix than the
evidence carries.

**Proven.** `--` is required and sufficient for `-`-leading text on tmux 3.4
(reproduced both directions). Both text-carrying `send-keys` calls on the
poller's own path — `nudge_poller.go` → `NudgeSessionWithOpts` →
`sendMessageToTarget` → the chunk loop and `sendKeysLiteralWithRetry` — lacked
it. Neither passes `-u` itself, and `commandContext` builds the global `-u`
correctly, so **within the poller process no `send-keys` invocation can emit
`unknown flag -u` except through its text argument.** That is a positive
source result, not an inference from the error string.

**Reconciled.** An objection was raised that the payload always opens with
`<system-reminder>`, so the first token can never be a flag — which is true,
and does not touch the defect: the chunk loop splits at a fixed byte offset, so
a *later* chunk can open with `-u…` or `-r…` even though the whole payload
never does. Both readings are right, and the chunk boundary is what joins them.

**Still not byte-attributable.** That a chunk boundary produced the *specific*
errors logged at 22:20Z. `unknown flag -u` is reproducible from a leading-`-u`
payload *and* from a genuinely misplaced `-u` option, so the error text cannot
discriminate on its own, and the queue contents that produced those lines are
gone. What closes the gap is the call-graph result above rather than the log:
in that process there is no other way for a `-u` to reach a subcommand.

**Measured while writing this (2026-09-05, 13 live queues, 60 chunk
boundaries): zero chunks began with `-`.** Replaying `FormatForInjection` over
the live queue found no hit at that moment, though the same replay earlier in
the evening did find one (session `hq-mayor`, chunk at offset 512 beginning
`-reminder>`). So the mechanism demonstrably fires and is rare — roughly the
frequency of `-` as a byte, about 1%. Rarity is not a defence: a batch that
hits it is byte-identical on every retry, so one unlucky split plus defect 2's
requeue loop is exactly a log of 39 identical errors. **Defect 2 is the
amplifier that turns a 1%-per-boundary accident into an outage**, which is why
it, and not the `--`, is the load-bearing part of this fix.

## Measurement traps

These are paid for; do not re-learn them.

- **A falling queue depth is not evidence of delivery.** One witness queue fell
  18 → 9 seconds after a poller restart, which reads as a drain. It was TTL
  expiry: the nine that vanished were the oldest hitting their 30-minute TTL
  and the nine that remained were all unexpired. Expiry and delivery are
  indistinguishable from depth alone.
- **Working discriminators:** grep the poller log for injection errors, or
  check whether the oldest remaining entry has outlived the TTL.
- **A short sample showing the problem gone is as untrustworthy as one showing
  it present.** Sample across at least one full retry period.
- **To reproduce a staged fragment from a pane,** hash the *rendered* lines
  joined with newlines, including the prompt glyph and two-space indents. No
  substring of the sent message body will match, at any length.
