//go:build !integration

package cmd

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

// TestMain isolates the package's test process before any test runs.
//
// This package's tests shell out to bd and gt. Without isolation they inherit
// the developer's HOME — which is how a stray ~/.beads-planning embedded Dolt
// store gets manufactured on a live Gas Town host — and the default Dolt
// endpoint, which on such a host is the production server (cl-69h).
//
// The integration build of this package has its own TestMain in
// integration_testmain_test.go; keep the two in sync.
func TestMain(m *testing.M) {
	cleanup := testutil.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
