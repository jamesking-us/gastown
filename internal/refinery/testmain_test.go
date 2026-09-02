package refinery

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
	"github.com/steveyegge/gastown/internal/testutil"
)

// TestMain isolates the package's test process before any test runs. Tests
// here reach the beads SDK and shell out to bd; without isolation they inherit
// the developer's real HOME and the default Dolt endpoint, which on a Gas Town
// host is the production server (cl-69h/cl-qaj3). Tests that need a real
// server ask for one with testutil.RequireDoltContainer, which overrides the
// endpoint afterwards.
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	testutil.TerminateDoltContainer()
	cleanup()
	os.Exit(code)
}
