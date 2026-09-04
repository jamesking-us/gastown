// Package procutil provides process-existence checks that avoid two
// false-positive failure modes that plain PID probing exhibits (cl-d77p):
//
//   - Zombie self-report as alive. A defunct (zombie) child still answers a
//     bare Signal(0)/kill(pid, 0) probe successfully — the kernel keeps its
//     PID allocated until the parent reaps it — so a probe built on Signal(0)
//     alone reports a process as running for a window after its real work
//     already finished. Every liveness check in this package additionally
//     confirms via `ps` that the candidate's state is not zombie.
//   - Self-matching pattern search. `pgrep -f PATTERN` matches against the
//     full command line of every process on the system, including whichever
//     process's own command line happens to contain PATTERN. A wait loop of
//     the shape `until ! pgrep -f "$PATTERN"; do sleep N; done` embeds
//     PATTERN in its own `bash -c` invocation, so it matches itself and can
//     never observe "no matches" — the loop never terminates. Five such
//     loops did exactly this to polecat chrome (2026-09-03, 40 minutes lost)
//     after minuteman hit the same class capped by an iteration bound
//     (cl-69h). FindByPattern excludes the caller's own PID and its parent's.
package procutil

import "os"

// IsAlive reports whether pid identifies a live, non-zombie process.
func IsAlive(pid int) bool {
	return isAlive(pid)
}

// IsProcessAlive is IsAlive for a caller that already holds an *os.Process
// (e.g. from os/exec or os.FindProcess). A nil process is never alive.
func IsProcessAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return IsAlive(p.Pid)
}
