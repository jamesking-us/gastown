//go:build !windows

package mayor

import "github.com/steveyegge/gastown/internal/procutil"

// acpProcessAlive checks if a process is running (and not a zombie awaiting
// reap — see internal/procutil for why a bare signal-0 check is not enough).
func acpProcessAlive(pid int) bool {
	return procutil.IsAlive(pid)
}
