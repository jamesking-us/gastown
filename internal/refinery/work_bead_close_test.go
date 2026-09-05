package refinery

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeWorkBeadStore struct {
	issues          map[string]*beads.Issue
	children        map[string][]*beads.Issue
	childrenErr     error
	comments        map[string][]beads.Comment
	commentsErr     error
	closeCalls      []string
	lastCloseReason string
	closeErr        error
	closeErrCloses  bool
}

func newFakeWorkBeadStore() *fakeWorkBeadStore {
	return &fakeWorkBeadStore{issues: map[string]*beads.Issue{}, children: map[string][]*beads.Issue{}, comments: map[string][]beads.Comment{}}
}

func (f *fakeWorkBeadStore) addComment(id, text string) {
	f.comments[id] = append(f.comments[id], beads.Comment{ID: id + "-c", IssueID: id, Text: text})
}

func (f *fakeWorkBeadStore) Comments(id string) ([]beads.Comment, error) {
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.comments[id], nil
}

func (f *fakeWorkBeadStore) addChild(parent string, child *beads.Issue) {
	f.issues[child.ID] = child
	f.children[parent] = append(f.children[parent], child)
}

func (f *fakeWorkBeadStore) Children(id string) ([]*beads.Issue, error) {
	if f.childrenErr != nil {
		return nil, f.childrenErr
	}
	return f.children[id], nil
}

func (f *fakeWorkBeadStore) add(issue *beads.Issue) {
	f.issues[issue.ID] = issue
}

func (f *fakeWorkBeadStore) Show(id string) (*beads.Issue, error) {
	issue, ok := f.issues[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func (f *fakeWorkBeadStore) ForceCloseWithReason(reason string, ids ...string) error {
	f.lastCloseReason = reason
	f.closeCalls = append(f.closeCalls, ids...)
	if f.closeErr != nil {
		if f.closeErrCloses {
			for _, id := range ids {
				if issue, ok := f.issues[id]; ok {
					issue.Status = string(beads.StatusClosed)
				}
			}
		}
		return f.closeErr
	}
	for _, id := range ids {
		if issue, ok := f.issues[id]; ok {
			issue.Status = string(beads.StatusClosed)
		}
	}
	return nil
}

func workIssue(id string, status string) *beads.Issue {
	return &beads.Issue{ID: id, Title: id, Type: "bug", Status: status}
}

func agentIssue(id string, desc string) *beads.Issue {
	return &beads.Issue{ID: id, Title: id, Type: "agent", Labels: []string{"gt:agent"}, Status: string(beads.StatusOpen), Description: desc}
}

func TestCloseMergedWorkBead_SourceIssueWinsOverAgentFallback(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.add(workIssue("gt-agent-hint", string(beads.StatusOpen)))
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "active_mr: gt-mr\nlast_source_issue: gt-agent-hint\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Branch:      "polecat/atom/gt-source+abc123",
		Target:      "main",
		SourceIssue: "gt-source",
		AgentBead:   "gt-agent",
		MergeCommit: "abc123",
	})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want closed gt-source", result)
	}
	if len(work.closeCalls) != 1 || work.closeCalls[0] != "gt-source" {
		t.Fatalf("close calls = %v, want [gt-source]", work.closeCalls)
	}
	if !strings.Contains(work.lastCloseReason, "Merged in gt-mr") || !strings.Contains(work.lastCloseReason, "commit_sha: abc123") {
		t.Fatalf("close reason missing merge metadata: %q", work.lastCloseReason)
	}
}

func TestCloseMergedWorkBead_FallsBackToVerifiedAgentSource(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-source\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
		MRID:      "gt-mr",
		Branch:    "polecat/atom/gt-source+abc123",
		Target:    "main",
		AgentBead: "gt-agent",
	})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want fallback close gt-source", result)
	}
	if len(work.closeCalls) != 1 || work.closeCalls[0] != "gt-source" {
		t.Fatalf("close calls = %v, want [gt-source]", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_FallsBackToCompletionMRID(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "mr_id: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-source\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
		MRID:      "gt-mr",
		Branch:    "polecat/atom/gt-source+abc123",
		AgentBead: "gt-agent",
	})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want completion-metadata fallback close", result)
	}
}

func TestCloseMergedWorkBead_RejectsUnverifiedAgentFallbacks(t *testing.T) {
	tests := []struct {
		name          string
		agentDesc     string
		agentType     string
		agentLabs     []string
		requestBranch string
	}{
		{name: "wrong active mr", agentDesc: "active_mr: gt-other\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "wrong completion mr", agentDesc: "mr_id: gt-other\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "branch mismatch", agentDesc: "active_mr: gt-mr\nbranch: polecat/other/gt-source+abc123\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "missing agent branch", agentDesc: "active_mr: gt-mr\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "missing request branch", agentDesc: "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-source\n", agentType: "agent", agentLabs: []string{"gt:agent"}},
		{name: "missing source", agentDesc: "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\n", agentType: "agent", agentLabs: []string{"gt:agent"}, requestBranch: "polecat/atom/gt-source+abc123"},
		{name: "not agent bead", agentDesc: "active_mr: gt-mr\nlast_source_issue: gt-source\n", agentType: "task", agentLabs: nil, requestBranch: "polecat/atom/gt-source+abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := newFakeWorkBeadStore()
			work.add(workIssue("gt-source", string(beads.StatusOpen)))
			agent := newFakeWorkBeadStore()
			agent.add(&beads.Issue{ID: "gt-agent", Title: "agent", Type: tt.agentType, Labels: tt.agentLabs, Status: string(beads.StatusOpen), Description: tt.agentDesc})

			result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{
				MRID:      "gt-mr",
				Branch:    tt.requestBranch,
				AgentBead: "gt-agent",
			})

			if result.Closed || !result.NotFound || result.WorkBeadID != "" {
				t.Fatalf("result = %+v, want unresolved fallback", result)
			}
			if len(work.closeCalls) != 0 {
				t.Fatalf("close calls = %v, want none", work.closeCalls)
			}
		})
	}
}

func TestCloseMergedWorkBead_RejectsNonConcreteTarget(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(&beads.Issue{ID: "gt-mr-target", Title: "MR target", Type: "merge-request", Labels: []string{"gt:merge-request"}, Status: string(beads.StatusOpen)})
	agent := newFakeWorkBeadStore()
	agent.add(agentIssue("gt-agent", "active_mr: gt-mr\nbranch: polecat/atom/gt-source+abc123\nlast_source_issue: gt-mr-target\n"))

	result := closeMergedWorkBead(work, agent, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", Branch: "polecat/atom/gt-source+abc123", AgentBead: "gt-agent"})

	if result.Closed || !result.NotFound || result.WorkBeadID != "gt-mr-target" {
		t.Fatalf("result = %+v, want rejected non-concrete target", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_RejectsNonMergeableTargets(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "no_merge", description: "no_merge: true\n"},
		{name: "review_only", description: "review_only: true\n"},
		{name: "local merge strategy", description: "merge_strategy: local\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := newFakeWorkBeadStore()
			issue := workIssue("gt-source", string(beads.StatusOpen))
			issue.Description = tt.description
			work.add(issue)

			result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

			if result.Closed || !result.NotFound || result.WorkBeadID != "gt-source" {
				t.Fatalf("result = %+v, want rejected target", result)
			}
			if len(work.closeCalls) != 0 {
				t.Fatalf("close calls = %v, want none", work.closeCalls)
			}
		})
	}
}

func TestCloseMergedWorkBead_AlreadyTerminalConcreteTargetIsNoop(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusClosed)))

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if !result.Closed || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want terminal no-op success", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_CloseErrorLeavesWorkOpen(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.closeErr = errors.New("dolt unavailable")

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if result.Closed || !result.NotFound || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want failed close", result)
	}
	if got := work.issues["gt-source"].Status; got != string(beads.StatusOpen) {
		t.Fatalf("source status = %q, want open", got)
	}
}

func TestCloseMergedWorkBead_CloseErrorThenTerminalRaceSucceeds(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.closeErr = errors.New("lost close race")
	work.closeErrCloses = true

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if !result.Closed || result.NotFound || result.WorkBeadID != "gt-source" {
		t.Fatalf("result = %+v, want terminal race success", result)
	}
}

func TestManagerIssueToMRIncludesAgentBead(t *testing.T) {
	mgr, _ := setupTestManager(t)
	issue := &beads.Issue{
		ID:          "gt-mr",
		Title:       "MR",
		Status:      string(beads.StatusOpen),
		Description: "branch: polecat/atom/gt-source+abc123\nsource_issue: gt-source\nagent_bead: gt-agent\ntarget: main",
	}

	mr := mgr.issueToMR(issue)
	if mr.AgentBead != "gt-agent" {
		t.Fatalf("AgentBead = %q, want gt-agent", mr.AgentBead)
	}
}

// cl-ivk: an MR and its source issue are not 1:1. When a bead's work is split,
// merging one MR must not close the whole bead — the false close is silent and
// the remaining facets are simply lost.
func TestCloseMergedWorkBead_RefusesBeadWithOpenChild(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-split", string(beads.StatusOpen)))
	work.addChild("gt-split", workIssue("gt-part-1", string(beads.StatusClosed)))
	work.addChild("gt-split", workIssue("gt-part-2", string(beads.StatusOpen)))

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Target:      "main",
		SourceIssue: "gt-split",
		MergeCommit: "abc123",
	})

	if result.Closed {
		t.Fatal("closed a bead whose work is only partly merged")
	}
	if !result.Blocked || !strings.Contains(result.BlockReason, "gt-part-2") {
		t.Fatalf("result = %+v, want a block naming the open child", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close attempted: %v", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_ClosedChildrenDoNotBlock(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-whole", string(beads.StatusOpen)))
	work.addChild("gt-whole", workIssue("gt-part-1", string(beads.StatusClosed)))

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Target:      "main",
		SourceIssue: "gt-whole",
		MergeCommit: "abc123",
	})

	if !result.Closed || result.Blocked {
		t.Fatalf("result = %+v, want a normal close when every child is done", result)
	}
}

// Molecule steps and other wisps hang off work beads as a matter of course.
// They are not the bead's remaining work and must not block its close.
func TestCloseMergedWorkBead_WispChildrenDoNotBlock(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-whole", string(beads.StatusOpen)))
	work.addChild("gt-whole", workIssue("gt-wisp-step1", string(beads.StatusOpen)))

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Target:      "main",
		SourceIssue: "gt-whole",
		MergeCommit: "abc123",
	})

	if !result.Closed || result.Blocked {
		t.Fatalf("result = %+v, want wisp children ignored", result)
	}
}

func TestCloseMergedWorkBead_DeclaredSplitBlocksClose(t *testing.T) {
	work := newFakeWorkBeadStore()
	issue := workIssue("gt-declared", string(beads.StatusOpen))
	issue.Description = "split: true\nsomething else\n"
	work.add(issue)

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Target:      "main",
		SourceIssue: "gt-declared",
		MergeCommit: "abc123",
	})

	if result.Closed || !result.Blocked || result.BlockReason != "split-declared" {
		t.Fatalf("result = %+v, want the declared split to block the close", result)
	}
}

// An unanswerable question is not a pass: if the children cannot be read, the
// bead is left open and said so, rather than closed on an assumption.
func TestCloseMergedWorkBead_UnreadableChildrenBlockClose(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-unknown", string(beads.StatusOpen)))
	work.childrenErr = errors.New("dolt unreachable")

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{
		MRID:        "gt-mr",
		Target:      "main",
		SourceIssue: "gt-unknown",
		MergeCommit: "abc123",
	})

	if result.Closed {
		t.Fatal("closed a bead whose children could not be read")
	}
	if !result.Blocked || !strings.Contains(result.BlockReason, "inconclusive") {
		t.Fatalf("result = %+v, want an inconclusive block", result)
	}
}

func TestCloseMergedWorkBead_RefusesBeadStatingItsOwnReleaseCondition(t *testing.T) {
	tests := []struct {
		name       string
		issue      *beads.Issue
		wantReason string
	}{
		{
			name:       "release condition in description",
			issue:      &beads.Issue{ID: "gt-source", Type: "bug", Status: string(beads.StatusOpen), Description: "release_condition: closes on root-cause-found\n"},
			wantReason: "stay-open: release_condition",
		},
		{
			name:       "stay-open label",
			issue:      &beads.Issue{ID: "gt-source", Type: "bug", Status: string(beads.StatusOpen), Labels: []string{"gt:stay-open"}},
			wantReason: "stay-open: label:gt:stay-open",
		},
		{
			name:       "condition in notes",
			issue:      &beads.Issue{ID: "gt-source", Type: "bug", Status: string(beads.StatusOpen), Notes: "**Reopen condition:** any further auto-close"},
			wantReason: "stay-open: reopen_condition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := newFakeWorkBeadStore()
			work.add(tt.issue)

			result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

			if result.Closed || !result.Blocked || result.BlockReason != tt.wantReason {
				t.Fatalf("result = %+v, want blocked with %q", result, tt.wantReason)
			}
			if len(work.closeCalls) != 0 {
				t.Fatalf("close calls = %v, want none", work.closeCalls)
			}
			if got := work.issues["gt-source"].Status; got != string(beads.StatusOpen) {
				t.Fatalf("source status = %q, want open", got)
			}
		})
	}
}

func TestCloseMergedWorkBead_RefusesConditionCarriedInComment(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.addComment("gt-source", "mayor ratified: stay_open: true until the RCA lands")

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if result.Closed || !result.Blocked || result.BlockReason != "stay-open: comment:stay_open" {
		t.Fatalf("result = %+v, want blocked by comment-carried condition", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_UnreadableCommentsBlockClose(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.commentsErr = errors.New("dolt unavailable")

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if result.Closed || !result.Blocked || !strings.Contains(result.BlockReason, "stay-open-check-inconclusive") {
		t.Fatalf("result = %+v, want inconclusive comment read to block", result)
	}
	if len(work.closeCalls) != 0 {
		t.Fatalf("close calls = %v, want none", work.closeCalls)
	}
}

func TestCloseMergedWorkBead_OrdinaryBeadStillClosesWithCommentsRead(t *testing.T) {
	work := newFakeWorkBeadStore()
	work.add(workIssue("gt-source", string(beads.StatusOpen)))
	work.addComment("gt-source", "looks good to me")

	result := closeMergedWorkBead(work, nil, nil, mergedWorkBeadCloseRequest{MRID: "gt-mr", SourceIssue: "gt-source"})

	if !result.Closed || result.Blocked {
		t.Fatalf("result = %+v, want ordinary close", result)
	}
	if len(work.closeCalls) != 1 || work.closeCalls[0] != "gt-source" {
		t.Fatalf("close calls = %v, want [gt-source]", work.closeCalls)
	}
}

type fakeAgentHookStore struct {
	hooks      map[string]string
	clearCalls []string
	clearErr   error
}

func (f *fakeAgentHookStore) ClearAgentHookBeadIfMatches(id string, expectedHook string) (bool, error) {
	f.clearCalls = append(f.clearCalls, id+"->"+expectedHook)
	if f.clearErr != nil {
		return false, f.clearErr
	}
	if f.hooks[id] != expectedHook {
		return false, nil
	}
	delete(f.hooks, id)
	return true, nil
}

func TestReleaseMergedWorkHook_ClearsHookNamingTheMergedBead(t *testing.T) {
	hooks := &fakeAgentHookStore{hooks: map[string]string{"gt-agent": "gt-source"}}
	out := &strings.Builder{}

	releaseMergedWorkHook(hooks, out, "gt-agent", "gt-source")

	if _, still := hooks.hooks["gt-agent"]; still {
		t.Fatalf("hook_bead still set: %+v", hooks.hooks)
	}
	if !strings.Contains(out.String(), "Released hook on gt-agent") {
		t.Fatalf("output = %q, want the release reported", out.String())
	}
}

func TestReleaseMergedWorkHook_LeavesAWorkerThatMovedOn(t *testing.T) {
	hooks := &fakeAgentHookStore{hooks: map[string]string{"gt-agent": "gt-next"}}
	out := &strings.Builder{}

	releaseMergedWorkHook(hooks, out, "gt-agent", "gt-source")

	if hooks.hooks["gt-agent"] != "gt-next" {
		t.Fatalf("hook_bead = %q, want gt-next untouched", hooks.hooks["gt-agent"])
	}
	if out.String() != "" {
		t.Fatalf("output = %q, want silence when nothing was cleared", out.String())
	}
}

func TestReleaseMergedWorkHook_IgnoresMissingIDs(t *testing.T) {
	hooks := &fakeAgentHookStore{hooks: map[string]string{"gt-agent": "gt-source"}}

	releaseMergedWorkHook(hooks, nil, "", "gt-source")
	releaseMergedWorkHook(hooks, nil, "gt-agent", "")

	if len(hooks.clearCalls) != 0 {
		t.Fatalf("clear calls = %v, want none", hooks.clearCalls)
	}
}

func TestReleaseMergedWorkHook_ReportsClearFailure(t *testing.T) {
	hooks := &fakeAgentHookStore{hooks: map[string]string{"gt-agent": "gt-source"}, clearErr: errors.New("dolt unavailable")}
	out := &strings.Builder{}

	releaseMergedWorkHook(hooks, out, "gt-agent", "gt-source")

	if !strings.Contains(out.String(), "failed to clear hook_bead") {
		t.Fatalf("output = %q, want the failure reported", out.String())
	}
}
