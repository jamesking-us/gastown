package cmd

import (
	"fmt"
)

// rebaseGit is the subset of *git.Git that autoRebaseOnTarget needs. Defined as
// an interface so tests can drive the decision logic without standing up a full
// git repo for every gating case.
type rebaseGit interface {
	Rebase(onto string) error
	AbortRebase() error
}

// autoRebaseOnTarget rebases the current branch onto base when the branch is
// behind the target. It is a no-op when there is nothing to rebase, when the
// polecat ran the formula's pre-verify step (rebasing again would invalidate
// the gate results that --pre-verified attests to), when a prior push
// checkpoint exists (rebasing after pushing would require a force-push), or
// when the caller supplies a skipBecause reason (gt-4y9: --no-rebase, or the
// branch is already published on its remote — pushed and possibly reviewed
// commits must never be rewritten).
//
// Returns:
//   - rebased: true if a rebase actually ran successfully.
//   - skipReason: non-empty when behind > 0 but the rebase was intentionally
//     skipped. Empty when behind == 0 (no rebase needed) or when rebased == true.
//   - err: rebase failure, after AbortRebase has been attempted to clean up.
//
// gh#3400.
func autoRebaseOnTarget(g rebaseGit, base string, behind int, preVerified, alreadyPushed bool, skipBecause string) (rebased bool, skipReason string, err error) {
	if behind <= 0 {
		return false, "", nil
	}
	switch {
	case preVerified:
		return false, "--pre-verified is set", nil
	case alreadyPushed:
		return false, "prior push checkpoint exists", nil
	case skipBecause != "":
		return false, skipBecause, nil
	}

	fmt.Printf("→ Auto-rebasing onto %s (%d commits behind)\n", base, behind)
	if rebaseErr := g.Rebase(base); rebaseErr != nil {
		_ = g.AbortRebase()
		return false, "", fmt.Errorf("auto-rebase onto %s failed: %w\n"+
			"Resolve conflicts manually (git fetch origin && git rebase %s), commit the resolution, then rerun gt done.",
			base, rebaseErr, base)
	}
	return true, "", nil
}

// remoteBranchChecker is the subset of *git.Git used to decide whether a branch
// is already published.
type remoteBranchChecker interface {
	RemoteBranchExists(remote, branch string) (bool, error)
}

// doneRebaseSkipReason returns why gt done must not auto-rebase, or "" if it may.
//
// gt-4y9: polecats push after every commit (and the refinery/compliance review
// the pushed tip), so a branch that already exists on origin carries published,
// possibly reviewed commits. Rebasing it rewrites those SHAs: it broke compliance
// fix-round exact-ancestor chains and forced manual mayor recovery many times.
// If we cannot tell whether the branch is published, we do not rebase either.
// The refinery merges the branch onto the target, so skipping is always safe.
func doneRebaseSkipReason(g remoteBranchChecker, branch string) string {
	if doneNoRebase {
		return "--no-rebase is set"
	}
	if branch == "" {
		return ""
	}
	published, err := g.RemoteBranchExists("origin", branch)
	if err != nil {
		return fmt.Sprintf("could not check whether %s is published on origin (%v); not rewriting it", branch, err)
	}
	if published {
		return fmt.Sprintf("%s is already published on origin (rebasing would rewrite pushed commits)", branch)
	}
	return ""
}
