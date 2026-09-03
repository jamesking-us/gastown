package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
)

// newSandbox builds a <root>/polecats/<name>/ sandbox and returns the sandbox
// directory. Callers add checkouts to it; the rig clone is named after the rig
// so clonePath resolution matches production layout.
func newSandbox(t *testing.T, name string) string {
	t.Helper()
	sandbox := filepath.Join(t.TempDir(), "polecats", name)
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	return sandbox
}

// newSandboxCheckout creates a checkout inside a sandbox with an origin remote
// and one pushed commit on main.
func newSandboxCheckout(t *testing.T, sandbox, name string) string {
	t.Helper()
	remote := filepath.Join(sandbox, name+"-remote.git")
	repo := filepath.Join(sandbox, name)
	runCmd(t, sandbox, "git", "init", "--bare", remote)
	runCmd(t, sandbox, "git", "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(repo, "README.md"), "base")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", "main")
	return repo
}

// TestSandboxCheckoutsFindsSiblingCheckouts pins the enumeration: a nuke
// destroys every checkout in the sandbox, so every checkout must be inspected.
func TestSandboxCheckoutsFindsSiblingCheckouts(t *testing.T) {
	sandbox := newSandbox(t, "mirelurk")
	rigClone := newSandboxCheckout(t, sandbox, "ccm")
	scratch := newSandboxCheckout(t, sandbox, "gastown-fork")
	// A plain directory in the sandbox is not a checkout and must not be one.
	if err := os.MkdirAll(filepath.Join(sandbox, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	root, checkouts := polecatSandboxCheckouts(rigClone)
	if root != sandbox {
		t.Fatalf("sandbox root = %q, want %q", root, sandbox)
	}
	if len(checkouts) != 2 {
		t.Fatalf("checkouts = %v, want the rig clone and the scratch clone", checkouts)
	}
	if checkouts[0] != rigClone {
		t.Fatalf("first checkout = %q, want the rig clone %q", checkouts[0], rigClone)
	}
	if checkouts[1] != scratch {
		t.Fatalf("second checkout = %q, want %q", checkouts[1], scratch)
	}
}

// TestSandboxCheckoutsKeepsSingleCheckoutOutsidePolecatLayout guards the
// layout assumption: outside <...>/polecats/<name>/<clone>, siblings are not
// this polecat's and must not be scanned.
func TestSandboxCheckoutsKeepsSingleCheckoutOutsidePolecatLayout(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	root, checkouts := polecatSandboxCheckouts(repo)
	if root != repo {
		t.Fatalf("root = %q, want the clone itself %q", root, repo)
	}
	if len(checkouts) != 1 || checkouts[0] != repo {
		t.Fatalf("checkouts = %v, want only %q", checkouts, repo)
	}
}

// TestGitStateSeesDirtySiblingCheckout is the cl-hwl two-repo blindness: work
// living in the second checkout of a cross-repo sandbox was invisible, so the
// rig clone alone reported CLEAN while a nuke would have destroyed it.
func TestGitStateSeesDirtySiblingCheckout(t *testing.T) {
	sandbox := newSandbox(t, "brotherhood")
	rigClone := newSandboxCheckout(t, sandbox, "ccm")
	scratch := newSandboxCheckout(t, sandbox, "gastown-fork")
	writeRecoveryFile(t, filepath.Join(scratch, "JurisdictionNotice.cs"), "uncommitted work")

	state, err := getGitState(rigClone)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.Clean {
		t.Fatalf("sandbox reported clean while %s holds uncommitted work: %+v", scratch, state)
	}
	if len(state.UncommittedFiles) != 1 || !strings.Contains(state.UncommittedFiles[0], "gastown-fork/JurisdictionNotice.cs") {
		t.Fatalf("uncommitted files = %v, want the sibling checkout's file qualified by its checkout", state.UncommittedFiles)
	}
	if len(state.Checkouts) != 2 {
		t.Fatalf("checkouts reported = %d, want 2 so the verdict states its own scope", len(state.Checkouts))
	}
}

// TestGitStateSeesUnpushedCommitsInSiblingCheckout covers the committed-but-
// unpushed half of the same blindness.
func TestGitStateSeesUnpushedCommitsInSiblingCheckout(t *testing.T) {
	sandbox := newSandbox(t, "brotherhood")
	rigClone := newSandboxCheckout(t, sandbox, "ccm")
	scratch := newSandboxCheckout(t, sandbox, "gastown-fork")
	runGit(t, scratch, "switch", "-c", "polecat/brotherhood/cl-69h")
	writeRecoveryFile(t, filepath.Join(scratch, "fix.go"), "package main")
	runGit(t, scratch, "add", "fix.go")
	runGit(t, scratch, "commit", "-m", "work that never reached the remote")

	state, err := getGitState(rigClone)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.Clean {
		t.Fatal("sandbox reported clean while the sibling checkout holds an unpushed commit")
	}
	if state.UnpushedCommits != 1 {
		t.Fatalf("unpushed commits = %d, want 1 from the sibling checkout", state.UnpushedCommits)
	}
}

// TestGitStateUnmeasurableCommitsAreNotReportedAsZero is the anti-correlation
// itself: a checkout with no ref to compare HEAD against is the checkout whose
// commits exist nowhere else, and it used to report zero work at risk.
func TestGitStateUnmeasurableCommitsAreNotReportedAsZero(t *testing.T) {
	sandbox := newSandbox(t, "mirelurk")
	rigClone := newSandboxCheckout(t, sandbox, "ccm")

	// A scratch clone with commits and no remote at all.
	scratch := filepath.Join(sandbox, "scratch")
	runCmd(t, sandbox, "git", "init", scratch)
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(scratch, "only-copy.go"), "package main")
	runGit(t, scratch, "add", "only-copy.go")
	runGit(t, scratch, "commit", "-m", "the only copy of this work")

	state, err := getGitState(rigClone)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.Clean {
		t.Fatal("checkout whose commits exist nowhere but the sandbox reported clean")
	}
	if len(state.Unmeasured) == 0 {
		t.Fatal("unmeasurable checkout produced no Unmeasured entry; the omission stayed silent")
	}
	if !strings.Contains(strings.Join(state.Unmeasured, " "), "scratch") {
		t.Fatalf("Unmeasured = %v, want it to name the checkout", state.Unmeasured)
	}
}

// TestUnmeasuredCheckoutBlocksCleanup pins the verdict side: unmeasured must
// reach the classifier as a check failure, never as an absence of work.
func TestUnmeasuredCheckoutBlocksCleanup(t *testing.T) {
	input := polecat.WorkstateInput{State: polecat.StateIdle, CleanupStatus: polecat.CleanupClean}
	gitState := &GitState{
		Clean:      false,
		Unmeasured: []string{"scratch: no remote, upstream, or target ref to compare HEAD against"},
		Checkouts:  []CheckoutGitState{{Path: "ccm", Clean: true, Primary: true}, {Path: "scratch", Unmeasured: "no comparison refs"}},
	}
	applyGitStateToWorkstateInput(&input, "/tmp/sandbox/ccm", gitState, nil)
	if !input.GitCheckFailed {
		t.Fatal("unmeasured checkout did not set GitCheckFailed")
	}
	d := polecat.DecideWorkstate(input)
	if d.SafeToNuke || d.Verdict != polecat.WorkstateVerdictNeedsRecovery {
		t.Fatalf("verdict = %s (safe_to_nuke=%v), want NEEDS_RECOVERY for an unmeasured checkout", d.Verdict, d.SafeToNuke)
	}
}

// TestCleanupStatusFromGitStatePrefersUnknownOverClean guards the fallback
// path: with no agent bead, an unreadable checkout must not settle to a status
// that reads as safe.
func TestCleanupStatusFromGitStatePrefersUnknownOverClean(t *testing.T) {
	if got := cleanupStatusFromGitState(nil); got != polecat.CleanupUnknown {
		t.Fatalf("nil git state = %q, want unknown", got)
	}
	unmeasured := &GitState{Clean: false, Unmeasured: []string{"scratch: no comparison refs"}, UnpushedCommits: 0}
	if got := cleanupStatusFromGitState(unmeasured); got != polecat.CleanupUnknown {
		t.Fatalf("unmeasured git state = %q, want unknown", got)
	}
	if got := cleanupStatusFromGitState(&GitState{Clean: true}); got != polecat.CleanupClean {
		t.Fatalf("clean git state = %q, want clean", got)
	}
}

// TestGitStateScopeSentenceNamesCheckouts keeps the claim's limits attached to
// the claim: a SAFE_TO_NUKE line must be able to say what it examined.
func TestGitStateScopeSentenceNamesCheckouts(t *testing.T) {
	sentence := gitStateScopeSentence(&GitState{Checkouts: []CheckoutGitState{{Path: "ccm"}, {Path: "gastown-fork"}}})
	for _, want := range []string{"ccm", "gastown-fork", "2 checkout"} {
		if !strings.Contains(sentence, want) {
			t.Fatalf("scope sentence %q missing %q", sentence, want)
		}
	}
	if got := gitStateScopeSentence(nil); !strings.Contains(got, "unknown") {
		t.Fatalf("nil scope sentence = %q, want it to admit it inspected nothing", got)
	}
}

// TestActiveMRGitSafeCoversWholeSandbox: the gate that discards a recorded
// dirty cleanup_status must not be satisfied by the rig clone alone.
func TestActiveMRGitSafeCoversWholeSandbox(t *testing.T) {
	sandbox := newSandbox(t, "brotherhood")
	rigClone := newSandboxCheckout(t, sandbox, "ccm")
	scratch := newSandboxCheckout(t, sandbox, "gastown-fork")

	if !activeMRGitSafeForWorktree(rigClone) {
		t.Fatal("fully pushed sandbox reported unsafe")
	}

	writeRecoveryFile(t, filepath.Join(scratch, "wip.go"), "package main")
	if activeMRGitSafeForWorktree(rigClone) {
		t.Fatal("sandbox with a dirty sibling checkout reported git-safe")
	}
}
