package wispaudit

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// These tests write audit records, and the town root they land in is resolved
// from the working directory with GT_TOWN_ROOT/GT_ROOT as the fallback. An
// unisolated run therefore appends test records to the operator's real
// .events.jsonl — the log this package exists to keep trustworthy.
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
