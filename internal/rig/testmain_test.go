package rig

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs. Tests here
// scaffold rigs and shell out to bd; without isolation they wrote real
// databases to the production Dolt server on the default port and reached the
// developer's real HOME (cl-69h).
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
