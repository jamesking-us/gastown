package witness

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func witnessRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// witnessSandbox builds <root>/<rig>/polecats/<name>/ and returns it.
func witnessSandbox(t *testing.T, rig, name string) string {
	t.Helper()
	sandbox := filepath.Join(t.TempDir(), rig, "polecats", name)
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	return sandbox
}

// witnessCheckout creates a checkout with an origin remote and a pushed main.
func witnessCheckout(t *testing.T, sandbox, name string) string {
	t.Helper()
	remote := filepath.Join(sandbox, name+"-remote.git")
	repo := filepath.Join(sandbox, name)
	witnessRunGit(t, sandbox, "init", "--bare", remote)
	witnessRunGit(t, sandbox, "init", repo)
	witnessRunGit(t, repo, "config", "user.email", "test@example.com")
	witnessRunGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	witnessRunGit(t, repo, "add", "README.md")
	witnessRunGit(t, repo, "commit", "-m", "base")
	witnessRunGit(t, repo, "branch", "-M", "main")
	witnessRunGit(t, repo, "remote", "add", "origin", remote)
	witnessRunGit(t, repo, "push", "-u", "origin", "main")
	return repo
}

// TestSiblingCheckoutRiskSeesDirtySibling: the witness seat holds kill authority
// over the same polecats as the mayor seat, so the two-repo blindness has to be
// closed there too (cl-hwl, lesson 318).
func TestSiblingCheckoutRiskSeesDirtySibling(t *testing.T) {
	sandbox := witnessSandbox(t, "ccm", "brotherhood")
	clone := witnessCheckout(t, sandbox, "ccm")
	scratch := witnessCheckout(t, sandbox, "gastown-fork")

	if risk := siblingCheckoutRisk(clone); risk.Dirty || risk.CheckFailed {
		t.Fatalf("clean sandbox reported risk: %+v", risk)
	}

	if err := os.WriteFile(filepath.Join(scratch, "wip.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	risk := siblingCheckoutRisk(clone)
	if !risk.Dirty {
		t.Fatal("uncommitted work in a sibling checkout was not reported")
	}
	if risk.DirtyReason == "" {
		t.Fatal("dirty sibling produced no reason naming the checkout")
	}
}

// TestSiblingCheckoutRiskUnmeasurableIsNotZero: a sibling with no ref to
// compare HEAD against is the one whose commits exist nowhere else. It must
// fail closed, not report an absence of work.
func TestSiblingCheckoutRiskUnmeasurableIsNotZero(t *testing.T) {
	sandbox := witnessSandbox(t, "ccm", "mirelurk")
	clone := witnessCheckout(t, sandbox, "ccm")

	scratch := filepath.Join(sandbox, "scratch")
	witnessRunGit(t, sandbox, "init", scratch)
	witnessRunGit(t, scratch, "config", "user.email", "test@example.com")
	witnessRunGit(t, scratch, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(scratch, "only-copy.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	witnessRunGit(t, scratch, "add", "only-copy.go")
	witnessRunGit(t, scratch, "commit", "-m", "the only copy of this work")

	risk := siblingCheckoutRisk(clone)
	if !risk.CheckFailed {
		t.Fatalf("unmeasurable sibling did not fail closed: %+v", risk)
	}
	if risk.CheckFailedReason == "" {
		t.Fatal("unmeasurable sibling produced no reason")
	}
}

// TestCheckoutGitSafeJudgesEachCheckout guards the per-checkout predicate that
// activeMRGitSafe now runs over every checkout in a sandbox, not just the rig
// clone, before discarding a recorded dirty cleanup_status.
func TestCheckoutGitSafeJudgesEachCheckout(t *testing.T) {
	sandbox := witnessSandbox(t, "ccm", "brotherhood")
	scratch := witnessCheckout(t, sandbox, "gastown-fork")

	if !checkoutGitSafe(scratch) {
		t.Fatal("fully pushed sibling checkout reported unsafe")
	}
	if err := os.WriteFile(filepath.Join(scratch, "wip.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if checkoutGitSafe(scratch) {
		t.Fatal("dirty sibling checkout reported git-safe")
	}
}
