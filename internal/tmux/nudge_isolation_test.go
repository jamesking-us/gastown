package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// liveTownSocket stands in for the socket the town's agents actually run on.
//
// It is deliberately NOT the socket this package's TestMain registered: the
// point of the guard is that a test process may deliver to a server it created
// and to nothing else. Nothing listens on this name, so if the guard is missing
// the test fails on a dead server rather than reaching anyone — which is the
// only honest way to write this assertion, since the real failure mode is
// delivery to a pane belonging to the operator.
const liveTownSocket = "gt-town-not-ours"

// TestNudgeSessionCannotReachAForeignSocketUnderTest guards the funnel every
// tmux nudge in the codebase passes through.
//
// gt-8f3 was not a bug in one caller. It was the absence of any property at the
// transport, so whether a test delivered to a real agent depended on which of a
// dozen call paths it took and on whether whoever wrote it knew to set an
// environment variable. The session names a test resolves are the live town's —
// "hq-mayor" here is the session the operator's mayor runs in — and send-keys
// into a pane holding staged unsubmitted text appends and submits it (cl-jkr).
func TestNudgeSessionCannotReachAForeignSocketUnderTest(t *testing.T) {
	sink := os.Getenv(testenv.NudgeSinkEnv)
	if sink == "" {
		t.Fatalf("this test process has no nudge sink: %s must be set by testenv.IsolateProcessEnv (gt-8f3)", testenv.NudgeSinkEnv)
	}
	before, _ := os.ReadFile(sink)

	if err := NewTmuxWithSocket(liveTownSocket).NudgeSession("hq-mayor", "test"); err != nil {
		t.Fatalf("NudgeSession: %v", err)
	}

	after, err := os.ReadFile(sink)
	if err != nil {
		t.Fatalf("reading nudge sink: %v", err)
	}
	// Asserting the recorded content, not merely that no delivery happened,
	// is what stops the guard from hollowing out what it guards: a transport
	// that dropped every nudge would satisfy "nothing escaped" perfectly.
	gained := strings.TrimPrefix(string(after), string(before))
	if !strings.Contains(gained, "nudge:hq-mayor:") || !strings.Contains(gained, "test") {
		t.Errorf("sink gained %q, want a recorded hq-mayor nudge — the delivery either escaped or vanished", gained)
	}
}

// TestNudgeSessionInterceptionPrecedesTheQueueLock covers a consequence of
// where the guard sits. Interception happens BEFORE the flock at
// <townRoot>/.runtime/nudge_queue/<session>/.lock, so an intercepted nudge
// cannot create that directory either — which is the other half of gt-8f3's
// acceptance criterion, zero new entries under .runtime/nudge_queue.
func TestNudgeSessionInterceptionPrecedesTheQueueLock(t *testing.T) {
	town := t.TempDir()

	if err := NewTmuxWithSocket(liveTownSocket).NudgeSessionWithOpts("hq-mayor", "test", NudgeOpts{TownRoot: town}); err != nil {
		t.Fatalf("NudgeSessionWithOpts: %v", err)
	}

	if _, err := os.Stat(filepath.Join(town, ".runtime", "nudge_queue")); !os.IsNotExist(err) {
		t.Errorf("intercepted nudge still touched the town's queue directory: %v", err)
	}
}

// TestNudgeSessionStillDeliversToATestOwnedSocket is the positive control for
// the exemption, and the reason the guard is scoped to sockets at all: this
// package's delivery tests must keep delivering.
func TestNudgeSessionStillDeliversToATestOwnedSocket(t *testing.T) {
	socket := GetDefaultSocket()
	if socket == "" {
		t.Skip("no test socket registered by TestMain")
	}
	if !testsinkAllows(socket) {
		t.Fatalf("TestMain's socket %q is not registered in %s: this package's own nudge tests are being intercepted", socket, testenv.TmuxSocketsEnv)
	}
	if testsinkAllows(liveTownSocket) {
		t.Fatalf("%q is registered as test-owned; the guard above proves nothing", liveTownSocket)
	}
}

func testsinkAllows(socket string) bool {
	for _, allowed := range strings.Split(os.Getenv(testenv.TmuxSocketsEnv), ",") {
		if allowed == socket {
			return true
		}
	}
	return false
}
