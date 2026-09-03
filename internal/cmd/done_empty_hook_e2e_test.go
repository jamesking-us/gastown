package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestRunDoneRefusesSubmitFromEmptyHook is the acceptance test for cl-lqj, run
// against the harder of the two live variants.
//
// The seat IS the bead's assignee and the bead IS open — mayor reopened and
// reassigned it — so the assignee/open-bead check that suggests itself would
// wave this submission through, and the branch carries commits. Nothing is on
// the seat's hook, so gt done refuses: no push, no MR bead, no close.
//
// The explicit --issue flag is set, because that is what a restarted session
// has to hand. It must not lift the refusal.
func TestRunDoneRefusesSubmitFromEmptyHook(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	branch := setupRoutedSubmitGitRepo(t, workDir, false)
	logPath := installEmptyHookBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	townRoot := routedSourceTestTownRoot(workDir)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneCleanupStatus = "unpushed"
	doneSkipVerify = true
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID string) error { return nil }

	err := runDone(nil, nil)
	if err == nil {
		t.Fatal("runDone submitted from an empty hook; want refusal")
	}
	if !strings.Contains(err.Error(), "REFUSING TO SUBMIT") || !strings.Contains(err.Error(), "cl-lqj") {
		t.Fatalf("runDone error = %v, want the empty-hook refusal", err)
	}

	// Nothing reached the merge queue: no MR bead was created and no comment
	// or close was written back to the source. (Agent-bead housekeeping is not
	// part of the submission and is expected in the log.)
	log := readSubmitSourceBDLog(t, logPath)
	for _, forbidden := range []string{"gt:merge-request", "comments add bd-source", "close bd-source"} {
		if strings.Contains(log, forbidden) {
			t.Errorf("empty-hook refusal still ran %q:\n%s", forbidden, log)
		}
	}

	// Nothing reached the remote either — the branch was never pushed.
	remote := gitOutputForEmptyHookTest(t, workDir, "config", "--get", "remote.origin.url")
	refs := gitOutputForEmptyHookTest(t, workDir, "ls-remote", "--heads", remote)
	if strings.Contains(refs, branch) {
		t.Errorf("branch %s was pushed despite the refusal:\n%s", branch, refs)
	}

	// The commits are intact: a refusal must never cost the polecat its work.
	head := gitOutputForEmptyHookTest(t, workDir, "log", "--oneline", "-1")
	if !strings.Contains(head, "feature") {
		t.Errorf("branch tip = %q, want the feature commit left in place", head)
	}
}

// TestRunDoneRefusalLeavesNoDoneIntent verifies the refusal happens before the
// done-intent label is written. A lingering done-intent tells the witness to
// treat the seat as a zombie mid-exit and nuke it; a refused submit is not an
// exit, and must not look like one.
func TestRunDoneRefusalLeavesNoDoneIntent(t *testing.T) {
	workDir, currentBeadsDir, ownerBeadsDir := setupRoutedSourceTestTown(t)
	setupRoutedSubmitCommandTown(t, workDir)
	setupRoutedSubmitGitRepo(t, workDir, false)
	logPath := installEmptyHookBDRecorder(t, currentBeadsDir, ownerBeadsDir)
	resetDoneFlagsForTest(t)
	townRoot := routedSourceTestTownRoot(workDir)
	t.Setenv("GT_TEST_NUDGE_LOG", filepath.Join(t.TempDir(), "nudge.log"))
	t.Setenv("GT_TOWN_ROOT", townRoot)
	t.Setenv("GT_ROOT", townRoot)
	t.Setenv("GT_ROLE", "gastown/polecats/refuge")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "refuge")
	t.Setenv("BD_ACTOR", "gastown/polecats/refuge")
	t.Chdir(workDir)

	doneIssue = "bd-source"
	doneCleanupStatus = "unpushed"
	doneSkipVerify = true
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID string) error { return nil }

	if err := runDone(nil, nil); err == nil {
		t.Fatal("runDone submitted from an empty hook; want refusal")
	}

	log := readSubmitSourceBDLog(t, logPath)
	if strings.Contains(log, "done-intent") {
		t.Errorf("refused submit wrote a done-intent label:\n%s", log)
	}
}

// installEmptyHookBDRecorder is installSubmitSourceBDRecorder with the seat's
// hook empty: bd-source exists, is open, and is assigned to this very seat,
// but no bead is hooked or in progress for it.
func installEmptyHookBDRecorder(t *testing.T, currentBeadsDir, ownerBeadsDir string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	sourceJSON := `[{"id":"bd-source","title":"owner source","status":"open","assignee":"gastown/polecats/refuge","priority":1,"issue_type":"task"}]`
	script := `#!/bin/sh
if [ "$1" = "--allow-stale" ]; then
  shift
fi
if [ "$1" = "version" ]; then
  echo "bd stub"
  exit 0
fi
printf '%s\t%s\n' "$BEADS_DIR" "$*" >> ` + shellQuoteForEmptyHookTest(logPath) + `
if [ "$1" = "show" ] && [ "$2" = "bd-source" ]; then
  if [ "$BEADS_DIR" = ` + shellQuoteForEmptyHookTest(currentBeadsDir) + ` ]; then
    echo '` + sourceJSON + `'
    exit 0
  fi
  if [ "$BEADS_DIR" = ` + shellQuoteForEmptyHookTest(ownerBeadsDir) + ` ]; then
    echo '` + sourceJSON + `'
    exit 0
  fi
  echo "Issue not found in $BEADS_DIR" >&2
  exit 1
fi
if [ "$1" = "list" ]; then
  echo '[]'
  exit 0
fi
if [ "$1" = "sql" ]; then
  echo '[]'
  exit 0
fi
echo "unexpected bd command: $*" >&2
exit 1
`
	path := filepath.Join(binDir, "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write bd recorder: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	return logPath
}

func shellQuoteForEmptyHookTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func gitOutputForEmptyHookTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
