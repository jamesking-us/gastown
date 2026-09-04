//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/steveyegge/gastown/internal/procutil"
)

// setSysProcAttr sets platform-specific process attributes.
// On Windows, detach the child into a new process group and suppress
// console-window creation so background subprocesses don't flash a
// visible window (the daemon itself runs with CREATE_NO_WINDOW).
func setSysProcAttr(cmd *exec.Cmd) {
	const CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW,
	}
}

// isProcessAlive checks if a process is still running.
// On Windows, Signal(0) is not supported; procutil opens the process handle
// with minimal access to verify it exists.
func isProcessAlive(p *os.Process) bool {
	return procutil.IsProcessAlive(p)
}

// sendTermSignal sends a termination signal.
// On Windows, there's no SIGTERM - we use Kill() directly.
func sendTermSignal(p *os.Process) error {
	return p.Kill()
}

// sendKillSignal sends a kill signal.
// On Windows, Kill() is the only option.
func sendKillSignal(p *os.Process) error {
	return p.Kill()
}
