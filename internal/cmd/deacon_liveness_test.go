package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/worktreewrite"
)

// The health check's secondary liveness signal (cl-2sp).
//
// It used to be a delta on tmux session_activity, described in the source as
// "a reliable liveness signal". That field is pinned to session creation and
// never moves, so the delta could never fire and the branch was dead — failing
// toward "did not respond", which escalates a healthy agent.

func TestWorktreeWriteAdvanced(t *testing.T) {
	base := time.Now()
	found := func(at time.Time) worktreewrite.Result {
		return worktreewrite.Result{Found: true, LastWrite: at}
	}

	tests := []struct {
		name              string
		baseline, current worktreewrite.Result
		want              bool
	}{
		{"newer write", found(base), found(base.Add(time.Second)), true},
		{"same write", found(base), found(base), false},
		{"older write", found(base), found(base.Add(-time.Second)), false},
		{
			// "Nothing, then something" is not movement by this agent: the
			// first file any process creates in a previously-empty sandbox
			// would fire it, and a health check that reports a response nobody
			// made is worse than one that misses a response that was made.
			"nothing then something",
			worktreewrite.Result{Found: false},
			found(base),
			false,
		},
		{
			"baseline unreadable",
			worktreewrite.Result{Err: errors.New("permission denied")},
			found(base),
			false,
		},
		{
			"current unreadable",
			found(base),
			worktreewrite.Result{Err: errors.New("permission denied")},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := worktreeWriteAdvanced(tt.baseline, tt.current); got != tt.want {
				t.Errorf("worktreeWriteAdvanced = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAgentSandboxDir_ScopeIsTheSandbox pins the scope. A polecat sandbox holds
// the rig worktree plus any scratch clones, and work done in a sibling clone is
// still work (cl-hwl). Resolving to one checkout would answer about a fraction
// of what the agent is doing — the wrong-object error this whole bead is about.
func TestAgentSandboxDir_ScopeIsTheSandbox(t *testing.T) {
	town := t.TempDir()
	sandbox := filepath.Join(town, "myrig", "polecats", "dust")
	if err := os.MkdirAll(filepath.Join(sandbox, "myrig", "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sandbox, "gastown-fork"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := agentSandboxDir(town, "myrig/polecats/dust")
	if got != sandbox {
		t.Fatalf("agentSandboxDir = %q, want the sandbox root %q", got, sandbox)
	}

	// A write in the scratch clone must be visible from the resolved root.
	if err := os.WriteFile(filepath.Join(sandbox, "gastown-fork", "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res := scanAgentSandbox(got)
	if !res.Found {
		t.Error("scanAgentSandbox found nothing; sibling-checkout work is invisible")
	}
}

func TestAgentSandboxDir_RejectsNonAddresses(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "myrig", "witness"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tests := []struct {
		name, town, address string
	}{
		{"empty town", "", "myrig/witness"},
		{"empty address", town, ""},
		{"absolute path", town, "/etc"},
		{"traversal", town, "myrig/../../etc"},
		{"missing directory", town, "myrig/polecats/ghost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentSandboxDir(tt.town, tt.address); got != "" {
				t.Errorf("agentSandboxDir = %q, want empty", got)
			}
		})
	}
}

func TestSinceLastSandboxScan_Throttles(t *testing.T) {
	last := time.Now()
	if sinceLastSandboxScan(&last, time.Hour) {
		t.Error("scanned again immediately; the throttle does nothing")
	}
	last = time.Now().Add(-2 * time.Hour)
	if !sinceLastSandboxScan(&last, time.Hour) {
		t.Error("did not scan after the interval elapsed")
	}
	if time.Since(last) > time.Minute {
		t.Error("timestamp was not advanced; every tick would scan")
	}
}

func TestScanAgentSandbox_EmptyDirIsZeroResult(t *testing.T) {
	res := scanAgentSandbox("")
	if res.Found || res.Err != nil {
		t.Errorf("scanAgentSandbox(\"\") = %+v, want the zero Result", res)
	}
}
