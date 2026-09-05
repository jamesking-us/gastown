//go:build !windows

package nudge

import (
	"os/exec"
	"strconv"
	"strings"
)

// pollerProcessMatches reports whether pid is actually this session's
// nudge-poller, by reading the process's own command line.
//
// Liveness alone verifies the PID, not the process. PIDs are recycled, and
// this town recycles them in bulk: a container restart replays the low PID
// range while the pidfiles under .runtime/nudge_poller/ still name PIDs from
// the previous boot. A stale pidfile whose PID has been reused by an
// unrelated process passes every liveness check, so pollerAlive reports
// "already running" forever and the seat never gets a poller again — deaf,
// with a reassuring pidfile. Checking the command line is what makes the
// answer about the process rather than about the number.
//
// Fails OPEN: if `ps` cannot report the command line, the caller keeps the
// liveness answer. A false "not ours" costs a duplicate poller; treating a
// transient ps failure as proof of staleness would cause one on every hiccup.
func pollerProcessMatches(pid int, session string) bool {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true // cannot tell — defer to the liveness check
	}
	return commandLineIsPoller(string(out), session)
}

// commandLineIsPoller is the pure matching core of pollerProcessMatches,
// split out so the recycled-PID logic is testable against fabricated `ps`
// output without spawning processes.
//
// An empty command line (ps printed nothing usable) fails open for the same
// reason ps errors do.
func commandLineIsPoller(cmdline, session string) bool {
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return true
	}
	return strings.Contains(cmdline, "nudge-poller") && strings.Contains(cmdline, session)
}
