package witness

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

// installFakeGtRestart puts a fake "gt" binary on PATH that records each
// invocation's args (one line per call) to markerPath. Used to prove
// RestartPolecatSession did or did not actually attempt a session restart.
func installFakeGtRestart(t *testing.T, markerPath string) {
	t.Helper()

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "gt")
	script := "#!/bin/sh\necho \"$@\" >> \"" + markerPath + "\"\nexit 0\n"
	if runtime.GOOS == "windows" {
		scriptPath += ".bat"
		script = "@echo off\r\necho %* >> \"" + markerPath + "\"\r\n"
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// --- lifecycleShutdownPendingFor: pure matching logic, no live mail/bd needed ---

func TestLifecycleShutdownPendingFor_MatchesExactPolecatName(t *testing.T) {
	t.Parallel()
	messages := []*mail.Message{
		{Subject: "LIFECYCLE:Shutdown foundation"},
	}
	found, subject := lifecycleShutdownPendingFor(messages, "foundation")
	if !found {
		t.Fatal("expected a match for foundation")
	}
	if subject != "LIFECYCLE:Shutdown foundation" {
		t.Errorf("subject = %q, want the matched subject line", subject)
	}
}

func TestLifecycleShutdownPendingFor_NoMatchForDifferentPolecat(t *testing.T) {
	t.Parallel()
	messages := []*mail.Message{
		{Subject: "LIFECYCLE:Shutdown foundation"},
	}
	found, _ := lifecycleShutdownPendingFor(messages, "refuge")
	if found {
		t.Error("must not match a shutdown request addressed to a different polecat")
	}
}

func TestLifecycleShutdownPendingFor_IgnoresUnrelatedSubjects(t *testing.T) {
	t.Parallel()
	messages := []*mail.Message{
		{Subject: "POLECAT_DONE foundation"},
		{Subject: "MERGED foundation"},
		{Subject: "HELP: something"},
	}
	found, _ := lifecycleShutdownPendingFor(messages, "foundation")
	if found {
		t.Error("must not match on unrelated protocol subjects")
	}
}

func TestLifecycleShutdownPendingFor_EmptyInbox(t *testing.T) {
	t.Parallel()
	found, subject := lifecycleShutdownPendingFor(nil, "foundation")
	if found || subject != "" {
		t.Errorf("found=%v subject=%q, want (false, \"\") for an empty inbox", found, subject)
	}
}

// --- defaultCheckRestartInterlock: assignee-mismatch path (mockable via bd) ---

func TestDefaultCheckRestartInterlock_AssigneeMismatchBlocks(t *testing.T) {
	bd, _ := mockBd(
		func(args []string) (string, error) {
			return `[{"assignee":"testrig/polecats/refuge"}]`, nil
		},
		func(args []string) error { return nil },
	)

	decision := defaultCheckRestartInterlock(bd, t.TempDir(), "testrig", "foundation", "cl-39w.6")
	if !decision.Blocked {
		t.Fatal("expected the restart to be blocked when the hooked bead's assignee is a different polecat")
	}
	if decision.Reason != "assignee-mismatch" {
		t.Errorf("Reason = %q, want %q", decision.Reason, "assignee-mismatch")
	}
	if !strings.Contains(decision.Detail, "refuge") || !strings.Contains(decision.Detail, "cl-39w.6") {
		t.Errorf("Detail = %q, want it to name the bead and the actual assignee", decision.Detail)
	}
}

func TestDefaultCheckRestartInterlock_AssigneeMatchAllowsRestart(t *testing.T) {
	bd, _ := mockBd(
		func(args []string) (string, error) {
			return `[{"assignee":"testrig/polecats/foundation"}]`, nil
		},
		func(args []string) error { return nil },
	)

	decision := defaultCheckRestartInterlock(bd, t.TempDir(), "testrig", "foundation", "cl-39w.6")
	if decision.Blocked {
		t.Errorf("must not block when the polecat is still the bead's assignee, got Reason=%q Detail=%q", decision.Reason, decision.Detail)
	}
}

func TestDefaultCheckRestartInterlock_NoHookBeadSkipsAssigneeCheck(t *testing.T) {
	bd, calls := mockBd(
		func(args []string) (string, error) {
			t.Fatal("bd show must not be called when there is no hook bead")
			return "", nil
		},
		func(args []string) error { return nil },
	)

	decision := defaultCheckRestartInterlock(bd, t.TempDir(), "testrig", "foundation", "")
	if decision.Blocked {
		t.Errorf("must not block a polecat with no hooked bead, got Reason=%q Detail=%q", decision.Reason, decision.Detail)
	}
	if len(calls.calls) != 0 {
		t.Errorf("expected no bd calls, got %v", calls.calls)
	}
}

func TestDefaultCheckRestartInterlock_LookupFailureFailsOpen(t *testing.T) {
	bd, _ := mockBd(
		func(args []string) (string, error) {
			return "", errors.New("dolt: connection refused")
		},
		func(args []string) error { return nil },
	)

	decision := defaultCheckRestartInterlock(bd, t.TempDir(), "testrig", "foundation", "cl-39w.6")
	if decision.Blocked {
		t.Errorf("a transient assignee-lookup failure must fail OPEN (not strand a legitimate restart), got Reason=%q", decision.Reason)
	}
}

// --- RestartPolecatSession: the interlock is a hard gate on the restart call ---

func TestRestartPolecatSession_BlockedInterlockPreventsSessionRestart(t *testing.T) {
	oldCheck := checkRestartInterlock
	checkRestartInterlock = func(bd *BdCli, workDir, rigName, polecatName, hookBead string) restartInterlockDecision {
		return restartInterlockDecision{Blocked: true, Reason: "lifecycle-hold", Detail: "unprocessed shutdown request"}
	}
	t.Cleanup(func() { checkRestartInterlock = oldCheck })

	marker := filepath.Join(t.TempDir(), "gt-calls.log")
	installFakeGtRestart(t, marker)

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	err := RestartPolecatSession(bd, t.TempDir(), "testrig", "foundation", "cl-39w.6")
	if err == nil {
		t.Fatal("expected the blocked interlock to produce an error")
	}
	var blocked *RestartBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("err = %v, want a *RestartBlockedError", err)
	}
	if blocked.Reason != "lifecycle-hold" {
		t.Errorf("Reason = %q, want %q", blocked.Reason, "lifecycle-hold")
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("gt session restart must NOT have been invoked when the interlock blocks the restart")
	}
}

func TestRestartPolecatSession_AssigneeMismatchBlocksSessionRestart(t *testing.T) {
	oldCheck := checkRestartInterlock
	checkRestartInterlock = func(bd *BdCli, workDir, rigName, polecatName, hookBead string) restartInterlockDecision {
		return restartInterlockDecision{Blocked: true, Reason: "assignee-mismatch", Detail: "bead reassigned"}
	}
	t.Cleanup(func() { checkRestartInterlock = oldCheck })

	marker := filepath.Join(t.TempDir(), "gt-calls.log")
	installFakeGtRestart(t, marker)

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	err := RestartPolecatSession(bd, t.TempDir(), "testrig", "refuge", "cl-39w.6")
	var blocked *RestartBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("err = %v, want a *RestartBlockedError", err)
	}
	if blocked.Reason != "assignee-mismatch" {
		t.Errorf("Reason = %q, want %q", blocked.Reason, "assignee-mismatch")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("gt session restart must NOT have been invoked when the interlock blocks the restart")
	}
}

func TestRestartPolecatSession_UnblockedInterlockProceedsToSessionRestart(t *testing.T) {
	oldCheck := checkRestartInterlock
	checkRestartInterlock = func(bd *BdCli, workDir, rigName, polecatName, hookBead string) restartInterlockDecision {
		return restartInterlockDecision{}
	}
	t.Cleanup(func() { checkRestartInterlock = oldCheck })

	marker := filepath.Join(t.TempDir(), "gt-calls.log")
	installFakeGtRestart(t, marker)

	bd, _ := mockBd(
		func(args []string) (string, error) { return "[]", nil },
		func(args []string) error { return nil },
	)

	if err := RestartPolecatSession(bd, t.TempDir(), "testrig", "foundation", "cl-39w.6"); err != nil {
		t.Fatalf("unexpected error from an unblocked restart: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("expected gt session restart to have been invoked: %v", err)
	}
	if !strings.Contains(string(data), "session restart testrig/foundation") {
		t.Errorf("gt invocation = %q, want it to restart testrig/foundation", string(data))
	}
}

// --- applyRestartResult: blocked vs. technical failure must read differently ---

func TestApplyRestartResult_BlockedErrorGetsDistinctAction(t *testing.T) {
	t.Parallel()
	zombie := &ZombieResult{Action: "restarted"}
	err := &RestartBlockedError{Reason: "lifecycle-hold", Detail: "unprocessed shutdown request for testrig/foundation"}

	applyRestartResult(zombie, err, "restart-failed")

	if !strings.HasPrefix(zombie.Action, "restart-blocked-lifecycle-hold") {
		t.Errorf("Action = %q, want it prefixed with restart-blocked-lifecycle-hold", zombie.Action)
	}
	if zombie.Error == nil {
		t.Error("zombie.Error must be set so the block is visible in diagnostics")
	}
}

func TestApplyRestartResult_TechnicalFailureUsesFailedAction(t *testing.T) {
	t.Parallel()
	zombie := &ZombieResult{Action: "restarted"}
	err := errors.New("session restart failed: exit status 1")

	applyRestartResult(zombie, err, "restart-agent-dead-session-failed")

	if zombie.Action != "restart-agent-dead-session-failed: session restart failed: exit status 1" {
		t.Errorf("Action = %q, want the failedAction prefix preserved for a non-blocked error", zombie.Action)
	}
}

func TestApplyRestartResult_NilErrorLeavesZombieUnchanged(t *testing.T) {
	t.Parallel()
	zombie := &ZombieResult{Action: "restarted"}
	applyRestartResult(zombie, nil, "restart-failed")

	if zombie.Action != "restarted" || zombie.Error != nil {
		t.Errorf("nil error must not modify zombie, got Action=%q Error=%v", zombie.Action, zombie.Error)
	}
}
