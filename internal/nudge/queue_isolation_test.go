package nudge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestEnqueueRefusesATownTheTestDoesNotOwn is the queue half of gt-8f3, whose
// acceptance criterion is zero new entries under <townRoot>/.runtime/nudge_queue
// during a test run.
//
// The queue looked like scratch state and is not: an entry written here is
// picked up by the target agent's UserPromptSubmit hook and delivered to a real
// pane, so writing one into the live town is a nudge with a delay. During the
// incident cl-refinery's queue was already saturated at 50/50 under a separate
// active incident, and the test suite was pushing at it on every run.
func TestEnqueueRefusesATownTheTestDoesNotOwn(t *testing.T) {
	sink := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(testenv.NudgeSinkEnv, sink)

	// A path outside anything this process created. It does not exist, so if the
	// guard is missing the failure is a created directory rather than a nudge to
	// a live seat — the same reason the tmux guard's test aims at a dead socket.
	foreign := filepath.Join(string(filepath.Separator), "gt-not-our-town")

	if err := Enqueue(foreign, "cl-refinery", QueuedNudge{Sender: "chrome", Message: "test"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if _, err := os.Stat(foreign); !os.IsNotExist(err) {
		t.Errorf("Enqueue created %q in a town the test does not own: %v", foreign, err)
	}
	data, err := os.ReadFile(sink)
	if err != nil {
		t.Fatalf("the refused nudge was not recorded: %v", err)
	}
	if got := string(data); !strings.Contains(got, "nudge:cl-refinery:chrome:test") {
		t.Errorf("sink = %q, want the refused nudge recorded", got)
	}
}

// TestEnqueueStillWritesToATownTheTestOwns is the positive control. A guard
// that refused every enqueue under test would pass the test above and leave the
// queue — TTLs, depth limits, FIFO ordering, claim files — with nothing
// exercising it.
func TestEnqueueStillWritesToATownTheTestOwns(t *testing.T) {
	town := t.TempDir()

	if err := Enqueue(town, "gt-alpha", QueuedNudge{Sender: "chrome", Message: "queued for real"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if n, err := Pending(town, "gt-alpha"); err != nil || n != 1 {
		t.Fatalf("Pending = %d, %v; want 1 entry in the test's own queue", n, err)
	}
}
