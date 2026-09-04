package witness

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/gastown/internal/mail"
)

// RestartBlockedError indicates the reassigned-work interlock (cl-3df) refused
// to restart a polecat. Callers should report this distinctly from a
// technical restart failure — it is the witness correctly declining a
// hazardous restart, not a malfunction.
type RestartBlockedError struct {
	Reason string // "lifecycle-hold" or "assignee-mismatch"
	Detail string
}

func (e *RestartBlockedError) Error() string {
	return fmt.Sprintf("restart blocked (%s): %s", e.Reason, e.Detail)
}

// restartInterlockDecision explains whether a restart may proceed.
type restartInterlockDecision struct {
	Blocked bool
	Reason  string
	Detail  string
}

// checkRestartInterlock is the single choke point for the reassigned-work
// interlock (cl-3df). Background: automated zombie recovery restarts a
// polecat's session to preserve its worktree and branch (gt-dsgp,
// restart-first policy) whenever the session looks dead or hung. That policy
// has no knowledge of two situations where restarting is actively hazardous:
//
//  1. A LIFECYCLE:Shutdown request for this polecat is still sitting
//     unprocessed in the rig witness's inbox — meaning a human or the mayor
//     has not yet resolved whether this polecat should come back at all
//     (e.g. because its work was just reassigned to someone else and nuking
//     its branch would destroy the only durable copy of reviewed work).
//     Restarting races that open decision.
//  2. The polecat's hooked bead has already been reassigned to a different
//     assignee. The restarted polecat's branch name and worktree still read
//     the old bead; a freshly restarted agent holding that stale context is
//     exactly the one most likely to try to "preserve" work it no longer
//     owns — pushing, rebasing, or amending a branch that is now load-bearing
//     for whoever the bead was reassigned to.
//
// Restart-first policy stays the default in every other case: this refuses
// only the two specific conditions above, it does not suppress recovery
// generally. A blocked restart is reported via RestartBlockedError so callers
// can announce it (patrol output, witness escalation) rather than silently
// dropping the zombie.
//
// A package var (matching verifyBranchAlreadyMerged) so tests can substitute
// a deterministic decision without a live mailbox or beads database.
var checkRestartInterlock = defaultCheckRestartInterlock

func defaultCheckRestartInterlock(bd *BdCli, workDir, rigName, polecatName, hookBead string) restartInterlockDecision {
	if pending, subject := pendingLifecycleShutdown(workDir, rigName, polecatName); pending {
		return restartInterlockDecision{
			Blocked: true,
			Reason:  "lifecycle-hold",
			Detail:  fmt.Sprintf("unprocessed shutdown request for %s/%s (subject: %q)", rigName, polecatName, subject),
		}
	}

	if hookBead == "" {
		return restartInterlockDecision{}
	}

	assignee, ok := getBeadAssignee(bd, workDir, hookBead)
	if !ok || assignee == "" {
		// Assignee couldn't be determined (transient lookup error, or the bead
		// carries no assignee). Fail open — the lifecycle-hold check above is
		// the primary guard, and refusing every restart on an inconclusive
		// assignee read would strand polecats whenever beads is slow.
		return restartInterlockDecision{}
	}

	if !assigneeIsPolecat(assignee, rigName, polecatName) {
		return restartInterlockDecision{
			Blocked: true,
			Reason:  "assignee-mismatch",
			Detail:  fmt.Sprintf("hook bead %s is now assigned to %s, not %s/polecats/%s", hookBead, assignee, rigName, polecatName),
		}
	}

	return restartInterlockDecision{}
}

// pendingLifecycleShutdown reports whether the rig witness's inbox still
// holds an unprocessed LIFECYCLE:Shutdown request naming polecatName, and if
// so, the subject line of that message. Such a message sitting unprocessed
// means a shutdown/reassignment decision for this polecat is still open
// (cl-3df): automated recovery raced exactly this window once already,
// restarting a polecat whose work had already moved to another polecat.
//
// A mailbox read failure is treated as "no hold found" rather than blocking
// the restart — this check must not strand every zombie behind a flaky mail
// read; the assignee check is the second, independent line of defense.
func pendingLifecycleShutdown(workDir, rigName, polecatName string) (bool, string) {
	mailbox := mail.NewMailboxFromAddress(fmt.Sprintf("%s/witness", rigName), workDir)
	messages, err := mailbox.List()
	if err != nil {
		return false, ""
	}
	return lifecycleShutdownPendingFor(messages, polecatName)
}

// lifecycleShutdownPendingFor scans messages for an unprocessed
// LIFECYCLE:Shutdown <polecatName> subject and returns (true, subject) for
// the first match, or (false, "") if none match. Split out from
// pendingLifecycleShutdown so the matching logic is testable against
// constructed messages, without a live mailbox/bd process.
func lifecycleShutdownPendingFor(messages []*mail.Message, polecatName string) (bool, string) {
	for _, msg := range messages {
		matches := PatternLifecycleShutdown.FindStringSubmatch(msg.Subject)
		if len(matches) < 2 {
			continue
		}
		if matches[1] == polecatName {
			return true, msg.Subject
		}
	}
	return false, ""
}

// getBeadAssignee returns the current assignee of beadID, or ("", false) if
// it could not be determined (network error, missing bead, malformed
// response). Mirrors getBeadStatus's contract.
func getBeadAssignee(bd *BdCli, workDir, beadID string) (string, bool) {
	if beadID == "" {
		return "", false
	}
	output, err := bd.Exec(workDir, "show", beadID, "--json")
	if err != nil || output == "" {
		return "", false
	}
	var issues []struct {
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil || len(issues) == 0 {
		return "", false
	}
	return issues[0].Assignee, true
}

// assigneeIsPolecat reports whether assignee identifies this exact polecat,
// using the same "<rig>/polecats/<name>" address form as DetectOrphanedMolecules.
func assigneeIsPolecat(assignee, rigName, polecatName string) bool {
	return assignee == fmt.Sprintf("%s/polecats/%s", rigName, polecatName)
}
