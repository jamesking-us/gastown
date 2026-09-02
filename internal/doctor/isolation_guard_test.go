package doctor

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestProcessEnvIsIsolated is the standing positive control for cl-69h: it
// fails if this package's TestMain stops isolating the process, rather than
// letting the tests quietly resume writing to the real HOME and to the
// production Dolt server.
func TestProcessEnvIsIsolated(t *testing.T) {
	testenv.AssertProcessEnvIsolated(t)
}
