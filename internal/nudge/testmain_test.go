package nudge

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// This is the package that owns the delivery queue, so it is the last one that
// should be able to write into the live town's — and it could: a queued nudge
// is delivered later by the target agent's own hook, which makes the queue a
// transport with a delay rather than a scratch directory (gt-8f3).
//
// Its own tests are unaffected: they build queues under t.TempDir, which the
// destination check in internal/testsink recognises as this process's own.
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// TestProcessEnvIsIsolated is the standing positive control: it fails if the
// TestMain above is deleted, rather than letting the tests quietly resume
// writing to the real HOME and the production Dolt server (cl-69h).
func TestProcessEnvIsIsolated(t *testing.T) {
	testenv.AssertProcessEnvIsolated(t)
}
