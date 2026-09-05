//go:build windows

package nudge

// pollerProcessMatches has no Windows implementation: there is no zombie
// state to confuse OpenProcess, and reading another process's command line
// needs a WMI or NtQueryInformationProcess round-trip that the Unix path
// gets from a single `ps`. Fails open, matching the Unix behaviour when `ps`
// cannot answer — the caller keeps the liveness result.
func pollerProcessMatches(_ int, _ string) bool {
	return true
}
