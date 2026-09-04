//go:build windows

package procutil

import "syscall"

// isAlive reports whether pid is a live process on Windows.
//
// Windows has no zombie/defunct process state in the POSIX sense: once a
// process terminates, OpenProcess fails for its PID (modulo PID reuse), so
// no separate zombie check is needed here.
func isAlive(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(h)
	return true
}
