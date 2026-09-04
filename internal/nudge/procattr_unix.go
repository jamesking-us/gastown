//go:build !windows

package nudge

import (
	"os"
	"syscall"

	"github.com/steveyegge/gastown/internal/procutil"
)

// detachedProcAttr returns SysProcAttr that detaches the child from
// the parent's process group so it survives the caller's exit.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// isProcessAlive checks if a process is running (and not a zombie awaiting
// reap — see internal/procutil for why a bare signal-0 check is not enough).
func isProcessAlive(proc *os.Process) bool {
	return procutil.IsProcessAlive(proc)
}

// terminateProcess sends SIGTERM for graceful shutdown.
func terminateProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}
