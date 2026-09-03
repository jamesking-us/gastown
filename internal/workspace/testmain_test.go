package workspace

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// This package IS the town-root lookup, so its tests are the last place that
// may be allowed to answer the question against the operator's live town. A
// test binary's working directory is its own package inside the gastown
// checkout, and on a Gas Town host that checkout sits inside /gt — so without
// isolation the walk in Find climbs straight out of the test and into the real
// town root (cl-st8u, the fourth escape in the cl-69h family).
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
