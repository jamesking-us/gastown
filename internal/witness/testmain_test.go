package witness

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// Tests here drive witness patrols through the rig manager, which reaches the beads SDK and the Dolt client. Without isolation such a test inherits the
// developer's real HOME — which is how a stray ~/.beads-planning embedded Dolt
// store gets manufactured on a live Gas Town host — and the default Dolt
// endpoint, which on such a host is the production server (cl-69h; extended
// past cmd/rig/proxy/plugin by cl-qaj3).
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
