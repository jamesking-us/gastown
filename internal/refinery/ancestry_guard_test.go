package refinery

import (
	"context"
	"testing"
)

// These tests cover the pre-merge ancestry/staleness guard added for cl-046:
// a stale branch whose changed files don't overlap what landed on target
// since it forked merges cleanly via a plain merge-tree check, with no
// signal that the branch was stale. The guard measures behind-count and, only
// when the branch is behind, checks whether its content already reached
// target under a different SHA before letting doMerge attempt a redundant
// merge.

// TestDoMerge_AlreadyLandedByPatchIDIsSkipped covers the hazard directly: a
// branch is behind target, but its change already landed on target under a
// different commit (e.g. rebased/squashed in by a different route). Without
// the guard, doMerge would still attempt (and likely succeed at) a clean,
// silent re-merge. With the guard, the MR is rejected as already-landed and
// target is left untouched.
func TestDoMerge_AlreadyLandedByPatchIDIsSkipped(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	// Feature branch introduces content X, forked from the initial commit.
	createFeatureBranch(t, workDir, "feature-x", "x.txt", "content-x\n")

	// The same change lands on main via a different route (e.g. a squash
	// merge elsewhere) — same diff, different commit identity.
	writeFile(t, workDir, "x.txt", "content-x\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: add x.txt (landed via a different branch)")

	// main also picks up an unrelated commit, so the feature branch is behind
	// by more than just the equivalent-content commit.
	writeFile(t, workDir, "unrelated.txt", "unrelated\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "chore: unrelated main commit")
	run(t, workDir, "git", "push", "origin", "main")

	before := run(t, workDir, "git", "rev-parse", "origin/main")

	e := newTestEngineer(t, workDir, g)
	e.beads = nil // rejection path closes the MR bead; no beads DB in this test

	mr := makeMR("mr-x", "feature-x", "main")
	result := e.doMerge(context.Background(), mr)

	if result.Success || !result.NoMerge {
		t.Fatalf("expected already-landed MR to be rejected without merging, got: %+v", result)
	}

	assertOriginMainUnchangedAndReset(t, workDir, before)
}

// TestDoMerge_StaleButNotYetLandedStillMerges confirms the guard does not
// block a legitimate merge: a branch behind target whose content has not
// landed yet must still merge normally, since git's own merge correctly
// integrates divergent history in that case.
func TestDoMerge_StaleButNotYetLandedStillMerges(t *testing.T) {
	workDir, g, cleanup := testGitRepo(t)
	defer cleanup()

	createFeatureBranch(t, workDir, "feature-y", "y.txt", "content-y\n")

	// main advances with unrelated content the feature branch never saw.
	writeFile(t, workDir, "unrelated.txt", "unrelated\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "chore: unrelated main commit")
	run(t, workDir, "git", "push", "origin", "main")

	e := newTestEngineer(t, workDir, g)

	mr := makeMR("mr-y", "feature-y", "main")
	result := e.doMerge(context.Background(), mr)

	if !result.Success {
		t.Fatalf("expected stale-but-unlanded MR to merge normally, got: %+v", result)
	}

	merged := run(t, workDir, "git", "show", "origin/main:y.txt")
	if merged != "content-y" {
		t.Fatalf("expected y.txt content on origin/main, got %q", merged)
	}
}
