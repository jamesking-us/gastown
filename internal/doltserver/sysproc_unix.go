//go:build !windows

package doltserver

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/steveyegge/gastown/internal/procutil"
)

// setProcessGroup puts the command in its own process group so that signals
// sent to the parent process group (e.g. SIGHUP when the caller calls
// syscall.Exec to become tmux) don't reach the spawned process.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processIsAlive checks whether a process with the given PID is still
// running (and not a zombie awaiting reap — see internal/procutil for why a
// bare signal-0 check is not enough).
func processIsAlive(pid int) bool {
	return procutil.IsAlive(pid)
}

// gracefulTerminate sends SIGTERM for graceful shutdown on Unix.
func gracefulTerminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
