package convoy

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestMain(m *testing.M) {
	// Isolate HOME and the Dolt endpoint before anything else, so a test that
	// reaches for bd or Dolt without opting in cannot touch the developer's
	// real HOME or the production server on the default port (cl-69h/cl-qaj3).
	// The container setup below overrides the endpoint with its own, which is
	// why this must come first.
	cleanup := testenv.IsolateProcessEnv()

	// Start an ephemeral Dolt container for this package's tests.
	// setupTestStore sets BEADS_TEST_MODE=1, which causes the beads SDK
	// to create testdb_<hash> databases. By routing those to an isolated
	// container (via BEADS_DOLT_PORT), the databases are destroyed when the
	// container is terminated at cleanup — preventing orphan
	// accumulation in the shared production Dolt data dir.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "convoy TestMain: skipping — %v\n", err)
		cleanup()
		os.Exit(0)
	}

	code := m.Run()

	testutil.TerminateDoltContainer()
	cleanup()
	os.Exit(code)
}
