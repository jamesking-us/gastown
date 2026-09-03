package reaper

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestMain isolates the package's test process before any test runs.
//
// The purge path writes a deletion record into the town's events log, and the
// town root is resolved from the working directory with GT_TOWN_ROOT/GT_ROOT as
// the fallback. An unisolated run therefore appends test records to the
// operator's real log — and this package's tests are about a deleter, so the
// records they write are exactly the ones a later investigation would trust.
func TestMain(m *testing.M) {
	cleanup := testenv.IsolateProcessEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
