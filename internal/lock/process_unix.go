//go:build !windows

package lock

import (
	"os/exec"
	"syscall"

	"github.com/steveyegge/gastown/internal/procutil"
)

// setProcessGroup detaches the child into its own process group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processExists checks if a process with the given PID exists and is alive
// (and not a zombie awaiting reap — see internal/procutil for why a bare
// signal-0 check is not enough).
func processExists(pid int) bool {
	return procutil.IsAlive(pid)
}
