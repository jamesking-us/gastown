package testutil

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// This package's helpers spawn bd and gt and stand up Dolt containers, and its
// own tests exercise them. Without isolation such a test inherits the
// developer's real HOME — which is how a stray ~/.beads-planning embedded Dolt
// store gets manufactured on a live Gas Town host — and the default Dolt
// endpoint, which on such a host is the production server (cl-69h; extended
// past cmd/rig/proxy/plugin by cl-qaj3).
//
// The isolation helper itself lives in internal/testenv, which imports nothing
// but the standard library, so packages like this one can use it without an
// import cycle.
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	TerminateDoltContainer()
	cleanup()
	os.Exit(code)
}
