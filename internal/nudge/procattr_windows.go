//go:build windows

package nudge

import (
	"os"
	"syscall"

	"github.com/steveyegge/gastown/internal/procutil"
)

// detachedProcAttr returns SysProcAttr for Windows.
// CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW detaches the child from the
// parent's console group without flashing a visible console window.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: 0x00000200 | 0x08000000, // CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW
	}
}

// isProcessAlive checks if a process is running on Windows.
func isProcessAlive(proc *os.Process) bool {
	return procutil.IsProcessAlive(proc)
}

// terminateProcess kills the process on Windows (no graceful SIGTERM).
func terminateProcess(proc *os.Process) error {
	return proc.Kill()
}
