package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mergeProofGit runs a git command in dir and fails the test if it errors.
func mergeProofGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeCommit writes a file and commits it, returning the new commit sha.
func writeCommit(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mergeProofGit(t, dir, "add", name)
	mergeProofGit(t, dir, "commit", "-m", message)
	return mergeProofGit(t, dir, "rev-parse", "HEAD")
}

// mergeProofRepo builds the shape the refinery actually produces: a branch cut
// from the target, a target that then moves on (other MRs landing), and the
// branch rebased onto the moved target before it is merged.
type mergeProofRepo struct {
	dir       string
	target    string // target branch name
	submitted string // branch tip as submitted to the queue
}

func newMergeProofRepo(t *testing.T) *mergeProofRepo {
	t.Helper()
	dir := initTestRepo(t)
	target := mergeProofGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	// The branch as the worker submitted it: two commits off the target.
	mergeProofGit(t, dir, "checkout", "-b", "polecat/test/work")
	writeCommit(t, dir, "feature.txt", "one\n", "feature: first")
	submitted := writeCommit(t, dir, "feature.txt", "one\ntwo\n", "feature: second")

	// Meanwhile the target moves — another MR lands while this one waits at the
	// compliance gate. This is what guarantees the rebase.
	mergeProofGit(t, dir, "checkout", target)
	writeCommit(t, dir, "other.txt", "unrelated\n", "other: landed first")

	return &mergeProofRepo{dir: dir, target: target, submitted: submitted}
}

func (r *mergeProofRepo) rebaseOntoTarget(t *testing.T) {
	t.Helper()
	mergeProofGit(t, r.dir, "checkout", "polecat/test/work")
	mergeProofGit(t, r.dir, "rebase", r.target)
}

func TestProveLandedByPatchID_RebasedMergeIsProven(t *testing.T) {
	repo := newMergeProofRepo(t)
	repo.rebaseOntoTarget(t)
	mergeProofGit(t, repo.dir, "checkout", repo.target)
	mergeProofGit(t, repo.dir, "merge", "--no-ff", "-m", "Merge work", "polecat/test/work")

	g := NewGit(repo.dir)

	// The premise of the bug: the submitted sha is NOT on the target, because
	// rebasing rewrote it. That must not be read as a failed merge.
	if ok, err := g.IsAncestor(repo.submitted, repo.target); err != nil || ok {
		t.Fatalf("IsAncestor(submitted, target) = %v, %v; want false (rebase rewrote the sha)", ok, err)
	}

	proof, err := g.ProveLandedByPatchID(repo.submitted, repo.target)
	if err != nil {
		t.Fatalf("ProveLandedByPatchID on a rebased merge: %v", err)
	}
	if proof.Method != LandedProofPatchID || proof.PatchID == "" {
		t.Fatalf("proof = %+v, want a patch-id proof", proof)
	}
	if proof.LandedHead == "" || proof.LandedBase == "" {
		t.Fatalf("proof = %+v, want the landed range recorded", proof)
	}
}

func TestProveLandedByPatchID_RebasedAndSquashedMergeIsProven(t *testing.T) {
	repo := newMergeProofRepo(t)
	repo.rebaseOntoTarget(t)

	// Squashed: no landed commit corresponds to any submitted commit, so every
	// per-commit patch-id differs by construction. Only the combined diff works.
	mergeProofGit(t, repo.dir, "checkout", repo.target)
	mergeProofGit(t, repo.dir, "merge", "--squash", "polecat/test/work")
	mergeProofGit(t, repo.dir, "commit", "-m", "Merge work (squashed)")

	g := NewGit(repo.dir)
	proof, err := g.ProveLandedByPatchID(repo.submitted, repo.target)
	if err != nil {
		t.Fatalf("ProveLandedByPatchID on a squashed merge: %v", err)
	}
	if proof.Method != LandedProofPatchID {
		t.Fatalf("proof = %+v, want a patch-id proof", proof)
	}
}

// The trees are not the check: a rebased branch lands on a target that also
// carries everything else merged in between, so the trees legitimately differ.
func TestProveLandedByPatchID_LandedTreeDiffersFromSubmittedTree(t *testing.T) {
	repo := newMergeProofRepo(t)
	repo.rebaseOntoTarget(t)
	mergeProofGit(t, repo.dir, "checkout", repo.target)
	mergeProofGit(t, repo.dir, "merge", "--no-ff", "-m", "Merge work", "polecat/test/work")

	g := NewGit(repo.dir)
	submittedTree := mergeProofGit(t, repo.dir, "rev-parse", repo.submitted+"^{tree}")
	landedTree := mergeProofGit(t, repo.dir, "rev-parse", repo.target+"^{tree}")
	if submittedTree == landedTree {
		t.Fatal("test setup is wrong: the trees must differ for this to be the interesting case")
	}
	if _, err := g.ProveLandedByPatchID(repo.submitted, repo.target); err != nil {
		t.Fatalf("ProveLandedByPatchID: %v", err)
	}
}

func TestProveLandedByPatchID_UnmergedBranchIsNotProven(t *testing.T) {
	repo := newMergeProofRepo(t)
	repo.rebaseOntoTarget(t)
	mergeProofGit(t, repo.dir, "checkout", repo.target)

	g := NewGit(repo.dir)
	_, err := g.ProveLandedByPatchID(repo.submitted, repo.target)
	if err == nil {
		t.Fatal("ProveLandedByPatchID proved a branch that was never merged")
	}
	if !strings.Contains(err.Error(), "patch_id_landing_not_found") {
		t.Fatalf("error = %v, want a completed search that found nothing", err)
	}
}

// An unanswerable question is reported as inconclusive, never as a pass and
// never as the same thing as a completed search.
func TestProveLandedByPatchID_MissingCommitIsInconclusive(t *testing.T) {
	repo := newMergeProofRepo(t)
	g := NewGit(repo.dir)

	_, err := g.ProveLandedByPatchID("0123456789012345678901234567890123456789", repo.target)
	if err == nil || !strings.Contains(err.Error(), "patch_id_proof_inconclusive") {
		t.Fatalf("error = %v, want an inconclusive result for an absent commit", err)
	}
}

func TestCombinedDiffPatchID_InvariantUnderRebaseAndSquash(t *testing.T) {
	repo := newMergeProofRepo(t)
	g := NewGit(repo.dir)

	base := mergeProofGit(t, repo.dir, "merge-base", repo.submitted, repo.target)
	reviewed, err := g.CombinedDiffPatchID(base, repo.submitted)
	if err != nil {
		t.Fatalf("CombinedDiffPatchID(reviewed): %v", err)
	}
	if reviewed == "" {
		t.Fatal("reviewed patch-id is empty")
	}

	repo.rebaseOntoTarget(t)
	rebasedTip := mergeProofGit(t, repo.dir, "rev-parse", "HEAD")
	rebasedBase := mergeProofGit(t, repo.dir, "merge-base", rebasedTip, repo.target)
	rebased, err := g.CombinedDiffPatchID(rebasedBase, rebasedTip)
	if err != nil {
		t.Fatalf("CombinedDiffPatchID(rebased): %v", err)
	}
	if rebased != reviewed {
		t.Fatalf("patch-id changed across rebase: %s != %s", rebased, reviewed)
	}

	// And across a squash of the same content.
	mergeProofGit(t, repo.dir, "checkout", repo.target)
	mergeProofGit(t, repo.dir, "merge", "--squash", "polecat/test/work")
	mergeProofGit(t, repo.dir, "commit", "-m", "squashed")
	squashed, err := g.CombinedDiffPatchID(repo.target+"~1", repo.target)
	if err != nil {
		t.Fatalf("CombinedDiffPatchID(squashed): %v", err)
	}
	if squashed != reviewed {
		t.Fatalf("patch-id changed across squash: %s != %s", squashed, reviewed)
	}
}

func TestCombinedDiffPatchID_EmptyRangeHasNoPatchID(t *testing.T) {
	repo := newMergeProofRepo(t)
	g := NewGit(repo.dir)

	id, err := g.CombinedDiffPatchID(repo.target, repo.target)
	if err != nil {
		t.Fatalf("CombinedDiffPatchID: %v", err)
	}
	if id != "" {
		t.Fatalf("patch-id = %q for an empty range, want empty", id)
	}
}
