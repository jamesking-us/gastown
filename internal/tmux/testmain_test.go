package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain sets up a dedicated tmux server for the package's integration tests.
// All tests that call newTestTmux() share this isolated server, which is torn
// down after all tests complete. This prevents test sessions from appearing on
// the user's interactive tmux and avoids socket conflicts with other packages.
func TestMain(m *testing.M) {
	// Isolate HOME and the Dolt endpoint first. This package does not import
	// the beads SDK, but its tests spawn tmux — and the shells and gt/bd
	// commands tmux runs inherit this process's environment wholesale, so an
	// unisolated tmux test can reach the developer's real HOME and the
	// production Dolt server just as effectively as a direct caller. tmux.test
	// was one of the binaries observed running during the ~/.beads-planning
	// writes in cl-69h (cl-qaj3).
	cleanup := testenv.IsolateProcessEnv()

	socket := fmt.Sprintf("gt-test-%d", os.Getpid())

	// Set defaultSocket so NewTmux() connects to the test server, not the
	// user's personal server or the sentinel that indicates "no town context".
	SetDefaultSocket(socket)

	// Start a sentinel session to keep the server alive for the entire test run.
	// Without this, tests that kill their last session inadvertently take down
	// the server, leaving a stale socket that prevents subsequent new-session
	// calls from restarting it (tmux sees the socket file but no listener).
	// The sentinel uses a name no individual test touches, so it outlives all
	// per-test sessions. TestMain kills the whole server at the end.
	if _, err := exec.LookPath("tmux"); err == nil {
		_ = exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-s", "gt-test-sentinel").Run()
	}

	code := m.Run()

	// Kill the test tmux server and restore the original socket state.
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	SetDefaultSocket("")

	cleanup()
	os.Exit(code)
}
