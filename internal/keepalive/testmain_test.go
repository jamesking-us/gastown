package keepalive

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// This package resolves a town root with workspace.FindFromCwd to find the
// seats it keeps alive.
//
// The confinement in workspace.Find keys on the sentinel this sets, so without
// a TestMain this package is unguarded however good the guard is: its tests
// walk up from their own directory and, on a Gas Town host, arrive at the
// operator's live town (cl-st8u, the fourth escape in the cl-69h family).
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestProcessEnvIsIsolated is the standing positive control: deleting the
// TestMain above fails a test instead of quietly resuming writes to the real
// HOME, the production Dolt server and the operator's town.
func TestProcessEnvIsIsolated(t *testing.T) {
	testenv.AssertProcessEnvIsolated(t)
}
