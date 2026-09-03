package refinery

import (
	"fmt"
	"io"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type mergedWorkBeadCloseRequest struct {
	MRID        string
	Branch      string
	Target      string
	SourceIssue string
	AgentBead   string
	MergeCommit string
}

type mergedWorkBeadCloseResult struct {
	WorkBeadID string
	Closed     bool
	NotFound   bool

	// Blocked marks a bead this path deliberately refused to close, with the
	// signal that stopped it. NotFound is set alongside it so existing callers
	// keep treating the bead as "not closed here".
	Blocked     bool
	BlockReason string
}

type workBeadCloser interface {
	Show(id string) (*beads.Issue, error)
	ForceCloseWithReason(reason string, ids ...string) error
}

type issueReader interface {
	Show(id string) (*beads.Issue, error)
}

func closeMergedWorkBead(work workBeadCloser, agent issueReader, out io.Writer, req mergedWorkBeadCloseRequest) mergedWorkBeadCloseResult {
	logf := func(format string, args ...interface{}) {
		if out != nil {
			_, _ = fmt.Fprintf(out, format, args...)
		}
	}

	workBeadID := resolveMergedWorkBead(agent, req)
	result := mergedWorkBeadCloseResult{WorkBeadID: workBeadID}
	if workBeadID == "" {
		logf("[Refinery] Note: merged MR %s has no resolvable work bead to close\n", req.MRID)
		result.NotFound = true
		return result
	}
	if work == nil {
		logf("[Refinery] Warning: no beads client available to close work bead %s\n", workBeadID)
		result.NotFound = true
		return result
	}

	issue, err := work.Show(workBeadID)
	if err != nil || issue == nil {
		logf("[Refinery] Warning: failed to fetch work bead %s: %v\n", workBeadID, err)
		result.NotFound = true
		return result
	}
	if reason := beads.ConcreteWorkIssueRejectReason(issue); reason != "" {
		logf("[Refinery] Warning: refusing to close non-concrete work bead %s (%s)\n", workBeadID, reason)
		result.NotFound = true
		result.Blocked = true
		result.BlockReason = reason
		return result
	}
	if beads.IssueStatus(strings.TrimSpace(issue.Status)).IsTerminal() {
		logf("[Refinery] Work bead already closed: %s\n", workBeadID)
		result.Closed = true
		return result
	}
	if reason := refineryMergedWorkBeadCloseBlockReason(issue); reason != "" {
		logf("[Refinery] Warning: refusing to close non-mergeable work bead %s (%s)\n", workBeadID, reason)
		result.NotFound = true
		result.Blocked = true
		result.BlockReason = reason
		return result
	}
	if reason := refinerySplitWorkBeadReason(work, issue); reason != "" {
		logf("[Refinery] Warning: refusing to close partly-merged work bead %s (%s) — %s closed only part of it; close it by hand when the rest lands\n", workBeadID, reason, req.MRID)
		result.NotFound = true
		result.Blocked = true
		result.BlockReason = reason
		return result
	}

	closeReason := fmt.Sprintf("Merged in %s", req.MRID)
	if req.MergeCommit != "" {
		closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, req.Target, req.MergeCommit)
	}

	if err := work.ForceCloseWithReason(closeReason, workBeadID); err != nil {
		if issue, showErr := work.Show(workBeadID); showErr == nil && issue != nil &&
			beads.ConcreteWorkIssueRejectReason(issue) == "" &&
			beads.IssueStatus(strings.TrimSpace(issue.Status)).IsTerminal() {
			logf("[Refinery] Work bead already closed: %s\n", workBeadID)
			result.Closed = true
			return result
		}
		logf("[Refinery] Warning: failed to close work bead %s: %v\n", workBeadID, err)
		result.NotFound = true
		return result
	}

	logf("[Refinery] Closed work bead: %s\n", workBeadID)
	result.Closed = true
	return result
}

func resolveMergedWorkBead(agent issueReader, req mergedWorkBeadCloseRequest) string {
	if sourceIssue := cleanWorkBeadID(req.SourceIssue); sourceIssue != "" {
		return sourceIssue
	}
	if agent == nil || cleanWorkBeadID(req.AgentBead) == "" || cleanWorkBeadID(req.MRID) == "" {
		return ""
	}

	agentIssue, err := agent.Show(req.AgentBead)
	if err != nil || !beads.IsAgentBead(agentIssue) {
		return ""
	}
	fields := beads.ParseAgentFields(agentIssue.Description)
	if fields == nil {
		return ""
	}
	if fields.ActiveMR != req.MRID && fields.MRID != req.MRID {
		return ""
	}
	agentBranch := strings.TrimSpace(fields.Branch)
	requestBranch := strings.TrimSpace(req.Branch)
	if agentBranch == "" || strings.EqualFold(agentBranch, "null") {
		return ""
	}
	if requestBranch == "" || strings.EqualFold(requestBranch, "null") || agentBranch != requestBranch {
		return ""
	}
	return cleanWorkBeadID(fields.LastSourceIssue)
}

func cleanWorkBeadID(id string) string {
	id = strings.TrimSpace(id)
	if strings.EqualFold(id, "null") {
		return ""
	}
	return id
}

// workBeadChildLister is implemented by beads clients that can enumerate a
// bead's children. It is optional: a caller that cannot answer the question
// simply does not get the structural half of the split check.
type workBeadChildLister interface {
	Children(id string) ([]*beads.Issue, error)
}

// refinerySplitWorkBeadReason reports why a merged MR must NOT auto-close its
// source issue.
//
// An MR and its source issue are treated as 1:1 by the close path, and they are
// not: a bead can be split across several MRs, and closing it when the first one
// lands marks work done that nobody has done. That false close is silent — the
// bead reads closed, and the remaining facets are simply lost.
//
// Two signals block the close. An explicit 'split: true' or 'partial: true' on
// the bead is the one an agent can set when it knows the work is being split;
// an open concrete child is the structural one. Wisps, molecule steps and other
// internal beads are not concrete work and never block. A child that cannot be
// read blocks too: an unanswerable question is not a pass.
func refinerySplitWorkBeadReason(work workBeadCloser, issue *beads.Issue) string {
	if issue == nil {
		return ""
	}
	if fields := beads.ParseAttachmentFields(issue); fields != nil && fields.SplitWork {
		return "split-declared"
	}

	lister, ok := work.(workBeadChildLister)
	if !ok {
		return ""
	}
	children, err := lister.Children(issue.ID)
	if err != nil {
		return fmt.Sprintf("split-check-inconclusive: cannot read children of %s: %v", issue.ID, err)
	}

	var open, unreadable []string
	for _, child := range children {
		if child == nil || strings.TrimSpace(child.ID) == "" {
			continue
		}
		if beads.ConcreteWorkIssueRejectReason(child) != "" {
			continue // wisp, molecule step, or other internal bead
		}
		status := strings.TrimSpace(child.Status)
		if status == "" {
			unreadable = append(unreadable, child.ID)
			continue
		}
		if !beads.IssueStatus(status).IsTerminal() {
			open = append(open, child.ID)
		}
	}
	if len(open) > 0 {
		return fmt.Sprintf("split: %d open child issue(s): %s", len(open), strings.Join(open, ", "))
	}
	if len(unreadable) > 0 {
		return fmt.Sprintf("split-check-inconclusive: %d child issue(s) have no readable status: %s", len(unreadable), strings.Join(unreadable, ", "))
	}
	return ""
}

func refineryMergedWorkBeadCloseBlockReason(issue *beads.Issue) string {
	if fields := beads.ParseAttachmentFields(issue); fields != nil {
		switch {
		case fields.NoMerge:
			return "no_merge"
		case fields.ReviewOnly:
			return "review_only"
		case strings.EqualFold(strings.TrimSpace(fields.MergeStrategy), "local"):
			return "merge_strategy:local"
		}
	}
	return ""
}
