package krc

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// This package never types workspace.Find itself: it reaches the town-root
// lookup through internal/events, which appends the town's events feed under a
// resolved root.
//
// That indirection is the whole point of the TestMain. Confinement
// (testsink.ConfineTownRoot, called from workspace.Find) keys on a sentinel
// that only IsolateProcessEnv sets, so an unisolated test binary that LINKS the
// lookup is unguarded however good the guard is — its tests walk up from their
// own directory and, on a Gas Town host where the checkout sits inside /gt,
// arrive at the operator's live town. Reaching the lookup through a dependency
// arrives there exactly as surely as calling it directly (cl-az4x, the
// transitive closure of the cl-st8u fix).
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
