package witness

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The stall detector's suppressor (cl-2sp).
//
// These tests pin the ASYMMETRY rather than the mechanism. worktreeShowsRecentWork
// may only ever suppress a detection; nothing it returns may cause one. Two
// polecats confirmed to have been working normally were recorded as
// "startup-stall -> auto-dismissed" before this existed, because the gates it
// sits in front of both read session age and neither reads activity.

func writeFile(t *testing.T, dir, rel string, age time.Duration) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestWorktreeShowsRecentWork_SuppressesOnRecentWrite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/handler.go", 10*time.Second)

	if !worktreeShowsRecentWork(dir, WorktreeWriteWorkingWindow) {
		t.Error("worktreeShowsRecentWork = false for a worktree written 10s ago")
	}
}

// TestWorktreeShowsRecentWork_QuietDoesNotAccuse is the important one. Every
// case here is a HEALTHY polecat that writes nothing — an agent reading source
// outside the worktree, one analysing test output, one blocked on a build lock.
// All must return false, and false must mean nothing more than "not suppressed".
func TestWorktreeShowsRecentWork_QuietDoesNotAccuse(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"old writes only", func(t *testing.T) string {
			dir := t.TempDir()
			writeFile(t, dir, "src/handler.go", 2*time.Hour)
			return dir
		}},
		{"empty worktree", func(t *testing.T) string { return t.TempDir() }},
		{"missing worktree", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "nope")
		}},
		{"empty path", func(t *testing.T) string { return "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if worktreeShowsRecentWork(tt.setup(t), WorktreeWriteWorkingWindow) {
				t.Error("worktreeShowsRecentWork = true, want false")
			}
		})
	}
}

// TestWorktreeShowsRecentWork_IgnoresGitAndBeads guards the false positive that
// would make the suppressor useless: another seat's push writes .git, and the
// beads daemon writes .beads, in every worktree in town. If those counted,
// every polecat would look permanently busy and the suppressor would suppress
// everything.
func TestWorktreeShowsRecentWork_IgnoresGitAndBeads(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/handler.go", 3*time.Hour)
	writeFile(t, dir, ".git/refs/heads/main", time.Second)
	writeFile(t, dir, ".beads/issues.jsonl", time.Second)

	if worktreeShowsRecentWork(dir, WorktreeWriteWorkingWindow) {
		t.Error("worktreeShowsRecentWork = true from .git/.beads writes")
	}
}

// TestWorktreeWriteWorkingWindow_IsGenerous pins the sizing argument. The action
// this window guards is blind keystrokes into a live pane; the cost of firing it
// on a working agent is an interrupted turn, and where staged text sits in the
// input box, a command nobody decided to run. A short window trades that
// against one extra patrol tick, which is not a trade worth making.
func TestWorktreeWriteWorkingWindow_IsGenerous(t *testing.T) {
	if WorktreeWriteWorkingWindow < 2*time.Minute {
		t.Errorf("WorktreeWriteWorkingWindow = %v, too short to protect a working agent",
			WorktreeWriteWorkingWindow)
	}
}
