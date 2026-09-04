//go:build !windows

package procutil

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// isAlive reports whether pid is a live, non-zombie process on Unix.
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return !isZombie(pid)
}

// isZombie reports whether pid's process state, as reported by `ps`, is
// zombie/defunct. Signal(0) succeeds against a zombie (its PID is still
// allocated until the parent reaps it), so this is the check that actually
// distinguishes "still doing work" from "exited, not yet reaped".
func isZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		// ps couldn't read the state (process gone between the Signal(0)
		// check and here, or ps itself failed). Signal(0) already
		// established liveness; fail open rather than turn a transient ps
		// error into a false "dead" reading.
		return false
	}
	return parseZombieStat(out)
}

// parseZombieStat is the pure parsing core of isZombie, split out for
// testing without spawning real processes. `ps -o stat=` reports a leading
// "Z" for a zombie (macOS/BSD may show "Z+" in a foreground group).
func parseZombieStat(out []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// FindByPattern runs `pgrep -f pattern` and returns the PIDs of live,
// non-zombie matches, excluding the calling process and its parent.
//
// This is the fix for the self-matching pattern search: a caller that polls
// with a pattern also present in its own (or its shell wrapper's) command
// line would otherwise match itself and never see an empty result. Excluding
// self and parent PID here means the same mistake in a caller's PATTERN
// cannot manufacture a permanent match; it does not protect against a
// pattern that separately matches an unrelated third process.
func FindByPattern(pattern string) ([]int, error) {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// pgrep exits 1 for "no processes matched" - not an error.
			return nil, nil
		}
		return nil, err
	}
	return filterPgrepOutput(out, os.Getpid(), os.Getppid(), isAlive), nil
}

// filterPgrepOutput is the pure filtering core of FindByPattern, split out
// so the self/parent exclusion and liveness re-verification are testable
// against fabricated pgrep output without spawning real processes.
func filterPgrepOutput(out []byte, self, parent int, alive func(int) bool) []int {
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		if pid == self || pid == parent {
			continue
		}
		if !alive(pid) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}
