package testsink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
)

// TestSentinelNamesMatchTestenv pins the two halves of the isolation layer
// together. testenv SETS these variables and testsink READS them, but the two
// packages cannot share a constant — testenv imports "testing", so production
// code cannot import it, which is the whole reason this package exists. A
// rename on one side and not the other would restore gt-8f3 silently: every
// guard would read an unset variable, decide the process is not under test, and
// hand the tests the live transport back.
func TestSentinelNamesMatchTestenv(t *testing.T) {
	if EnvIsolated != testenv.IsolatedEnv {
		t.Errorf("EnvIsolated = %q, testenv.IsolatedEnv = %q — the guards would never fire", EnvIsolated, testenv.IsolatedEnv)
	}
	if EnvNudgeLog != testenv.NudgeSinkEnv {
		t.Errorf("EnvNudgeLog = %q, testenv.NudgeSinkEnv = %q — intercepted nudges would go unrecorded", EnvNudgeLog, testenv.NudgeSinkEnv)
	}
}

func TestActiveFollowsTheSentinel(t *testing.T) {
	t.Setenv(EnvIsolated, "1")
	if !Active() {
		t.Error("Active() = false with the sentinel set")
	}

	// Only "1" counts. A variable that merely exists — exported by a shell
	// profile, say — must not be able to disable a production transport.
	for _, value := range []string{"", "0", "true", "yes"} {
		t.Setenv(EnvIsolated, value)
		if Active() {
			t.Errorf("Active() = true with %s=%q; only \"1\" may count", EnvIsolated, value)
		}
	}
}

func TestInterceptNudgeRecordsWhatItRefusedToDeliver(t *testing.T) {
	sink := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(EnvIsolated, "1")
	t.Setenv(EnvNudgeLog, sink)

	if !InterceptNudge("hq-mayor", "cloudcontentmanager/polecats/chrome", "test") {
		t.Fatal("InterceptNudge() = false in a test process: the delivery would have gone through")
	}

	// The recording is the positive control. An interception that dropped the
	// nudge silently would let the delivery path break without failing a test.
	got := readSink(t, sink)
	if want := "nudge:hq-mayor:cloudcontentmanager/polecats/chrome:test\n"; got != want {
		t.Errorf("sink = %q, want %q", got, want)
	}
}

func TestInterceptNudgeLeavesProductionAlone(t *testing.T) {
	sink := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(EnvIsolated, "")
	t.Setenv(EnvNudgeLog, sink)

	if InterceptNudge("hq-mayor", "deacon", "real") {
		t.Fatal("InterceptNudge() = true outside a test process: gt would stop delivering nudges")
	}
	if _, err := os.Stat(sink); !os.IsNotExist(err) {
		t.Errorf("sink was written outside a test process: %v", err)
	}
}

// TestRecordNudgeKeepsOneDeliveryOnOneLine guards the sink's only structural
// promise. Callers grep it for "nudge:<session>:", and a multi-line message —
// mail bodies and handoff notes are routinely multi-line — would otherwise
// produce continuation lines that read as extra deliveries.
func TestRecordNudgeKeepsOneDeliveryOnOneLine(t *testing.T) {
	sink := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv(EnvNudgeLog, sink)

	RecordNudge("gt-alpha", "mayor", "line one\nline two\r\nline three")

	got := readSink(t, sink)
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("sink = %q, want exactly one newline, got %d", got, n)
	}
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(got, want) {
			t.Errorf("sink = %q, want it to preserve %q", got, want)
		}
	}
}

func TestRecordNudgeWithoutASinkIsHarmless(t *testing.T) {
	t.Setenv(EnvNudgeLog, "")
	RecordNudge("gt-alpha", "mayor", "nowhere to write this")
}

// TestBlocksTownWriteJudgesTheDestination covers the case env-based isolation
// cannot: GT_TOWN_ROOT and GT_ROOT are stripped, but a town root found by
// walking up from the working directory is the operator's real one, and a queued
// nudge written there is delivered to a live agent later (gt-8f3).
func TestBlocksTownWriteJudgesTheDestination(t *testing.T) {
	t.Setenv(EnvIsolated, "1")

	owned := t.TempDir()
	if BlocksTownWrite(owned) {
		t.Errorf("BlocksTownWrite(%q) = true for a town the test made itself; the queue tests need to keep writing", owned)
	}
	if nested := filepath.Join(owned, ".runtime", "nudge_queue"); BlocksTownWrite(nested) {
		t.Errorf("BlocksTownWrite(%q) = true beneath a test-owned town", nested)
	}

	for _, foreign := range []string{"/gt", "/var/lib/gastown", filepath.Join(string(filepath.Separator), "gt", "cloudcontentmanager")} {
		if !BlocksTownWrite(foreign) {
			t.Errorf("BlocksTownWrite(%q) = false: a test would write into a live town", foreign)
		}
	}

	// An empty root means "no town was found", which is not a write at all.
	if BlocksTownWrite("") {
		t.Error(`BlocksTownWrite("") = true; there is no destination to protect`)
	}
}

func TestBlocksTownWriteAllowsTheIsolatedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvIsolated, "1")
	t.Setenv("HOME", home)

	town := filepath.Join(home, "gt")
	if BlocksTownWrite(town) {
		t.Errorf("BlocksTownWrite(%q) = true inside the isolated HOME", town)
	}
}

func TestBlocksTownWriteLeavesProductionAlone(t *testing.T) {
	t.Setenv(EnvIsolated, "")
	if BlocksTownWrite("/gt") {
		t.Error(`BlocksTownWrite("/gt") = true outside a test process: gt would stop queueing nudges`)
	}
}

func readSink(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading sink: %v", err)
	}
	return string(data)
}
