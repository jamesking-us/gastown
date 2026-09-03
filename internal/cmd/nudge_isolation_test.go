//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
	"github.com/steveyegge/gastown/internal/testsink"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/townlog"
)

// The regression guards for gt-8f3.
//
// On 2026-09-03 `go test ./internal/cmd/` delivered real nudges to the live
// town: hq-mayor, deacon, cl-witness, cl-refinery and gastown/alpha each
// received the literal string "test", and one of the deacon's dogs received
// "hello dog" — twice, five minutes apart, from an ordinary polecat running the
// suite for its own change. The payloads came from the tests below's
// neighbours in nudge_test.go.
//
// What these guards protect is specifically the DEFAULT. Individual tests could
// already opt out of delivery by setting a sink path with t.Setenv, and the
// ones that did were not the problem; the guarantee has to hold for a test that
// has never heard of the sink, because that is the test that will be written
// next.

// TestNudgeDeliveryIsSunkWithoutOptIn is the headline guard: a package whose
// TestMain isolates the process gets nudge interception with no further
// cooperation from the test.
//
// It deliberately does NOT call t.Setenv on the sink variable. Doing so would
// make it pass on the code that shipped the escape.
func TestNudgeDeliveryIsSunkWithoutOptIn(t *testing.T) {
	sink := os.Getenv(testenv.NudgeSinkEnv)
	if sink == "" {
		t.Fatalf("this test process has no nudge sink: %s must be set by testenv.IsolateProcessEnv (gt-8f3)", testenv.NudgeSinkEnv)
	}
	if !testsink.Active() {
		t.Fatalf("%s is not set: the transport guards are disarmed and this package's tests can reach live seats", testenv.IsolatedEnv)
	}

	before := sinkLines(t, sink)

	// hq-mayor is the acute target, not an arbitrary one: it is the town's most
	// protected pane, and delivery into a pane holding staged unsubmitted text
	// appends and submits it (cl-jkr). If any guard in the chain is missing on
	// a Gas Town host, this line is a real nudge to the mayor.
	if err := deliverNudge(tmux.NewTmux(), "hq-mayor", "test", "cloudcontentmanager/polecats/chrome"); err != nil {
		t.Fatalf("deliverNudge: %v", err)
	}

	added := added(before, sinkLines(t, sink))
	if len(added) != 1 {
		t.Fatalf("sink gained %d lines, want 1: %v", len(added), added)
	}
	// Asserting the CONTENT, not just the count, is what keeps the guard from
	// hollowing out the thing it guards: a deliverNudge that silently dropped
	// every nudge would satisfy "nothing was delivered" perfectly.
	if want := "nudge:hq-mayor:cloudcontentmanager/polecats/chrome:test"; added[0] != want {
		t.Errorf("sink line = %q, want %q", added[0], want)
	}
}

// TestNudgeDeliveryIsSunkForEveryMode covers the three delivery modes
// separately. They are three different transports — a tmux pane, the on-disk
// queue, and wait-idle, which polls the pane and then does one or the other —
// so an interception placed on only one of them still leaks through the rest.
func TestNudgeDeliveryIsSunkForEveryMode(t *testing.T) {
	origMode, origPriority := nudgeModeFlag, nudgePriorityFlag
	t.Cleanup(func() { nudgeModeFlag, nudgePriorityFlag = origMode, origPriority })
	nudgePriorityFlag = "normal"

	sink := os.Getenv(testenv.NudgeSinkEnv)
	if sink == "" {
		t.Fatalf("%s unset; see TestNudgeDeliveryIsSunkWithoutOptIn", testenv.NudgeSinkEnv)
	}

	for _, mode := range []string{NudgeModeImmediate, NudgeModeQueue, NudgeModeWaitIdle} {
		t.Run(mode, func(t *testing.T) {
			nudgeModeFlag = mode
			before := sinkLines(t, sink)

			if err := deliverNudge(tmux.NewTmux(), "cl-refinery", "test", "chrome"); err != nil {
				t.Fatalf("deliverNudge(%s): %v", mode, err)
			}

			added := added(before, sinkLines(t, sink))
			if len(added) != 1 || !strings.HasPrefix(added[0], "nudge:cl-refinery:") {
				t.Fatalf("mode %s: sink gained %v, want one cl-refinery line", mode, added)
			}
		})
	}
}

// TestNudgeLeavesNoTraceInATownItDoesNotOwn covers the two records a delivery
// writes besides the message itself. Both escaped isolation even when delivery
// did not, because they resolve the town by walking UP from the working
// directory — which, for a test, is its own package inside a checkout that sits
// inside the operator's real town. The deacon's evidence for gt-8f3 was exactly
// these lines in /gt/logs/town.log, and two readers spent real effort on what
// looked like a nudge storm and was a test writing to the shared log.
func TestNudgeLeavesNoTraceInATownItDoesNotOwn(t *testing.T) {
	town := fakeTown(t)
	t.Chdir(town)

	recordNudge("cloudcontentmanager", "hq-mayor", "chrome", "test")

	townLog := filepath.Join(town, "logs", "town.log")
	if data, err := os.ReadFile(townLog); err == nil && len(data) > 0 {
		t.Errorf("a test-context nudge wrote to the town log:\n%s", data)
	}
	if data, err := os.ReadFile(filepath.Join(town, ".events.jsonl")); err == nil && len(data) > 0 {
		t.Errorf("a test-context nudge wrote to the events feed:\n%s", data)
	}
}

// TestNudgeRecordsAreWrittenWhenNotUnderTest is the positive control for the
// guard above. Suppression that suppressed unconditionally would pass that test
// and quietly delete the town log's nudge history in production.
func TestNudgeRecordsAreWrittenWhenNotUnderTest(t *testing.T) {
	town := fakeTown(t)
	t.Chdir(town)
	t.Setenv(testenv.IsolatedEnv, "")

	recordNudge("cloudcontentmanager", "hq-mayor", "chrome", "a real nudge")

	data, err := os.ReadFile(filepath.Join(town, "logs", "town.log"))
	if err != nil {
		t.Fatalf("no town log written outside a test process: %v", err)
	}
	if !strings.Contains(string(data), "hq-mayor") {
		t.Errorf("town log does not name the target:\n%s", data)
	}
	if !strings.Contains(string(data), string(townlog.EventNudge)) {
		t.Errorf("town log does not record a nudge event:\n%s", data)
	}
}

// fakeTown builds the smallest directory the workspace lookup accepts as a town
// root: mayor/town.json. It stands in for /gt, and lives under t.TempDir so the
// test owns it — writes here are the ones that are allowed to land.
func fakeTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatalf("creating fake town: %v", err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{"name":"faketown"}`), 0o644); err != nil {
		t.Fatalf("writing town.json: %v", err)
	}
	return town
}

func sinkLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading nudge sink: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// added returns the lines the sink gained. The sink is process-wide and append
// only, and this package's tests run in one process, so a test must compare
// against what was there rather than truncating and racing its neighbours.
func added(before, after []string) []string {
	if len(after) <= len(before) {
		return nil
	}
	return after[len(before):]
}
