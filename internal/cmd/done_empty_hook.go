package cmd

import (
	"fmt"
	"strings"
)

// The empty-hook submit invariant (cl-lqj).
//
// An empty hook must never lead to a submitted merge request. This is the
// general fix for a cascade observed live on 2026-09-01, where every
// individual step was correct:
//
//	patrol-scan restarted a session-dead polecat (restart-first preserves work)
//	-> the restarted session came up with an EMPTY hook
//	-> the empty-hook startup protocol told it to run gt done, and it complied
//	-> gt done decoded the issue id from the BRANCH NAME and submitted
//	-> an unreviewed MR (three "WIP: checkpoint (auto)" commits, 4,262
//	   insertions across 21 files) reached the front of the merge queue with no
//	   human or agent having chosen to submit it.
//
// The obvious guard — "is this seat still the bead's assignee, and is the bead
// open" — is deliberately NOT the fix, and must not be shipped as one. It was
// measured against the second polecat armed on the same rig at the same time:
// that seat WAS the assignee, its bead WAS open (reopened by the mayor), and it
// would have submitted anyway, carrying the only reviewed copy of the work.
// Entitlement checks test permission; the defect is about intent, and no
// permission check can detect a correctly-permissioned action nobody decided to
// take.
//
// What separates the two cases is not ownership and not bead state: it is
// whether the seat is holding work at all. Active work means a bead that is
// hooked or in_progress for this seat — the same authoritative lookup gt hook
// and gt prime use (activeWorkStatuses). A bead sitting at "open" with this
// seat as assignee is not work on a hook; it is an assignment nobody slung.
//
// Deliberately NOT used as a discriminator: any liveness or age timestamp.
// gt polecat status --json last_activity is a static copy of created_at and
// never moves (cl-2sp); tmux session_activity never updates either; and the
// branch-name base36 suffix records when the BRANCH was cut, not when the
// submitting session began — run against the cascade case it returns a
// reassuring ~61 minutes. A restart respawns the pane inside the existing tmux
// session, so session_created does not move on restart either. Each of those
// looks like the check you want and silently answers a different question.

// emptyHookSubmit is everything the refusal needs to know about a gt done
// invocation. Kept as plain data so the decision is unit-testable without a
// worktree, a beads database, or a live session.
type emptyHookSubmit struct {
	// ExitType is the validated exit status (COMPLETED / ESCALATED / DEFERRED).
	ExitType string
	// PolecatSeat reports whether this invocation is running as a polecat.
	PolecatSeat bool
	// Seat is the agent identity (e.g. rig/polecats/name), "" when unresolved.
	Seat string
	// Branch is the current branch.
	Branch string
	// ActiveHook holds the ids of beads hooked or in_progress for this seat.
	ActiveHook []string
	// CommitsAhead is the commit count this branch would submit.
	CommitsAhead int
	// BranchPushedWithWork reports that the feature branch is already on the
	// remote with no unpushed commits — the "MR already submitted" fallback
	// path, which submits just as surely as a branch with local commits.
	BranchPushedWithWork bool
}

// hasActiveHook reports whether any non-empty bead id is on a seat's hook.
func hasActiveHook(ids []string) bool {
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			return true
		}
	}
	return false
}

// carriesWork reports whether this invocation would put commits into the merge
// queue. Callers that cannot determine the commit count must pass the same
// fail-closed value gt done already uses for that case (assume work exists).
func (in emptyHookSubmit) carriesWork() bool {
	return in.CommitsAhead > 0 || in.BranchPushedWithWork
}

// refuseEmptyHookSubmit enforces the invariant. It returns a terminal error
// when a polecat seat with nothing on its hook is about to submit work, and
// nil in every other case.
//
// Scope is deliberate and exact. The refusal covers the submitting path only:
//
//   - ESCALATED and DEFERRED create no merge request, so a seat that was handed
//     no work keeps a way to exit. Refusing those would strand hookless polecats
//     as zombies, which is how the restart loop started in the first place.
//   - A COMPLETED exit with no commits creates no merge request either. It is
//     allowed, but emptyHookBlocksSourceClose stops it from closing a bead it
//     was never given.
//
// The check does not consult the --issue flag, the branch-derived id, the
// bead's assignee, or the bead's open/closed state. None of those can tell a
// chosen completion from a startup protocol's, and the last two are the guard
// this bug exists to reject.
func refuseEmptyHookSubmit(in emptyHookSubmit) error {
	if in.ExitType != ExitCompleted || !in.PolecatSeat {
		return nil
	}
	if hasActiveHook(in.ActiveHook) || !in.carriesWork() {
		return nil
	}

	seat := strings.TrimSpace(in.Seat)
	if seat == "" {
		seat = "(identity unresolved)"
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "(unknown)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "REFUSING TO SUBMIT: nothing is on this seat's hook (cl-lqj)\n\n")
	fmt.Fprintf(&b, "  Seat:   %s\n", seat)
	fmt.Fprintf(&b, "  Branch: %s\n", branch)
	if in.CommitsAhead > 0 {
		fmt.Fprintf(&b, "  Commits that would have been submitted: %d\n", in.CommitsAhead)
	} else {
		fmt.Fprintf(&b, "  Branch is already pushed — a merge request would be created for it\n")
	}
	b.WriteString("\nNo bead is hooked or in progress for this seat, so no work was slung here\n")
	b.WriteString("and there is no completion to declare. A session that was handed no work\n")
	b.WriteString("cannot decide that work is finished — restarted sessions have reached the\n")
	b.WriteString("front of the merge queue this way, carrying thousands of unreviewed lines\n")
	b.WriteString("nobody chose to submit.\n")
	b.WriteString("\nNothing was pushed and no merge request was created. Every commit on this\n")
	b.WriteString("branch is untouched.\n")
	b.WriteString("\nIf this work is yours, the hook has to be restored by whoever dispatches\n")
	b.WriteString("work here — ask the witness to sling the bead back to this seat, then run\n")
	b.WriteString("gt done again. Do not re-point the branch, re-close the bead, or reach for\n")
	b.WriteString("a flag: the refusal is the whole point, and none of them lift it.\n")
	b.WriteString("\nIf you have nothing to work on, exit without submitting:\n")
	b.WriteString("  gt done --status DEFERRED\n")

	return fmt.Errorf("%s", b.String())
}

// emptyHookBlocksSourceClose reports whether the no-merge-request close path
// must be skipped. gt done force-closes the source bead when a COMPLETED exit
// has no commits, on the reasoning that nothing else ever will. A seat with an
// empty hook has no such standing: the bead it would close is one it was not
// given, decoded from a branch name that survives reassignment. Closing it
// hides work the mayor may have just reopened for somebody else.
func emptyHookBlocksSourceClose(polecatSeat bool, activeHook []string) bool {
	if !polecatSeat {
		return false
	}
	return !hasActiveHook(activeHook)
}

// doneSeatIsPolecat reports whether gt done is running as a polecat, from the
// actor identity and the GT_POLECAT environment marker. Either is enough: an
// unresolved identity inside a polecat session must still be treated as a
// polecat seat, so an identity lookup that fails cannot become a way around
// the invariant.
func doneSeatIsPolecat(actor, polecatEnv string) bool {
	return isPolecatActor(actor) || strings.TrimSpace(polecatEnv) != ""
}
