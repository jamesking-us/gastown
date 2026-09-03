package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/refinery"
)

type fakeMQPostMergeManager struct {
	mr              *refinery.MergeRequest
	findErr         error
	postMergeErr    error
	postMergeCalled bool
	postMergeMR     *refinery.MergeRequest
	postMergeOpts   refinery.PostMergeOptions
}

func (m *fakeMQPostMergeManager) FindMRForPostMerge(string) (*refinery.MergeRequest, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.mr, nil
}

func (m *fakeMQPostMergeManager) PostMergeMR(mr *refinery.MergeRequest, opts refinery.PostMergeOptions) (*refinery.PostMergeResult, error) {
	m.postMergeCalled = true
	m.postMergeMR = mr
	m.postMergeOpts = opts
	if m.postMergeErr != nil {
		return nil, m.postMergeErr
	}
	return &refinery.PostMergeResult{MR: m.mr, MRClosed: true, SourceIssueClosed: true, SourceIssueID: m.mr.IssueID}, nil
}

type fakeMQPostMergeGit struct {
	verifyErr error
	openPR    bool
	deleteErr error
	remoteTip string
	localHead string
	tipErr    error
	fetchErr  error

	// proofs maps a commit to the proof VerifyLandedOnPushTarget returns for it.
	// A commit with no entry falls back to verifyErr, or to an ancestry proof.
	proofs map[string]*git.LandedProof
	// unproven lists commits VerifyLandedOnPushTarget refuses to prove.
	unproven map[string]error

	verifiedCommits []string
	fetchedBranches []string
	deletedBranches []string
	deletedHeads    []string
	localDeleted    []string
}

func (g *fakeMQPostMergeGit) VerifyLandedOnPushTarget(_, _, commit string) (*git.LandedProof, error) {
	g.verifiedCommits = append(g.verifiedCommits, commit)
	if err, ok := g.unproven[commit]; ok {
		return nil, err
	}
	if proof, ok := g.proofs[commit]; ok {
		return proof, nil
	}
	if g.verifyErr != nil {
		return nil, g.verifyErr
	}
	return &git.LandedProof{Method: git.LandedProofAncestry, Submitted: commit}, nil
}

func (g *fakeMQPostMergeGit) FetchBranch(_, branch string) error {
	g.fetchedBranches = append(g.fetchedBranches, branch)
	return g.fetchErr
}

func (g *fakeMQPostMergeGit) HasOpenPullRequest(git.PullRequestRef) bool {
	return g.openPR
}

func (g *fakeMQPostMergeGit) PushRemoteBranchTip(_, _ string) (string, error) {
	return g.remoteTip, g.tipErr
}

func (g *fakeMQPostMergeGit) Rev(string) (string, error) {
	return g.localHead, nil
}

func (g *fakeMQPostMergeGit) DeleteRemoteBranchIfAt(_, branch, expectedHash string) error {
	g.deletedBranches = append(g.deletedBranches, branch)
	g.deletedHeads = append(g.deletedHeads, expectedHash)
	return g.deleteErr
}

func (g *fakeMQPostMergeGit) DeleteBranch(branch string, _ bool) error {
	g.localDeleted = append(g.localDeleted, branch)
	return nil
}

func testMQPostMergeMR() *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID:           "gt-mr-proof",
		Branch:       "polecat/test/gt-proof",
		Worker:       "polecats/test",
		IssueID:      "gt-proof",
		TargetBranch: "main",
		CommitSHA:    "abc123def456",
	}
}

func TestRunVerifiedMQPostMerge_ProofFailurePreservesRecordsAndBranch(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{verifyErr: errors.New("not reachable")}

	_, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err == nil || !strings.Contains(err.Error(), "merge proof failed") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want merge proof failure", err)
	}
	if !strings.Contains(err.Error(), mgr.mr.CommitSHA) {
		t.Fatalf("proof error %q does not mention submitted head %s", err, mgr.mr.CommitSHA)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called after failed proof")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted after failed proof: %v", rigGit.deletedBranches)
	}
	if len(rigGit.localDeleted) != 0 {
		t.Fatalf("local branch deleted after failed proof: %v", rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_VerifiedHeadClosesAndLeaseDeletes(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{remoteTip: mgr.mr.CommitSHA, localHead: mgr.mr.CommitSHA}

	outcome, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	cleanup := outcome.Cleanup
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if mgr.postMergeMR != mgr.mr {
		t.Fatal("PostMerge did not use the verified MR snapshot")
	}
	if len(rigGit.verifiedCommits) != 1 || rigGit.verifiedCommits[0] != mgr.mr.CommitSHA {
		t.Fatalf("verified commits = %v, want [%s]", rigGit.verifiedCommits, mgr.mr.CommitSHA)
	}
	if !cleanup.RemoteDeleted || len(rigGit.deletedBranches) != 1 || rigGit.deletedBranches[0] != mgr.mr.Branch {
		t.Fatalf("remote delete = cleanup=%+v branches=%v", cleanup, rigGit.deletedBranches)
	}
	if len(rigGit.deletedHeads) != 1 || rigGit.deletedHeads[0] != mgr.mr.CommitSHA {
		t.Fatalf("deleted heads = %v, want [%s]", rigGit.deletedHeads, mgr.mr.CommitSHA)
	}
	if !cleanup.LocalDeleted || len(rigGit.localDeleted) != 1 || rigGit.localDeleted[0] != mgr.mr.Branch {
		t.Fatalf("local delete = cleanup=%+v local=%v", cleanup, rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_SkipBranchDeleteStillRequiresProof(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{}

	outcome, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, true, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	cleanup := outcome.Cleanup
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if len(rigGit.verifiedCommits) != 1 || rigGit.verifiedCommits[0] != mgr.mr.CommitSHA {
		t.Fatalf("verified commits = %v, want [%s]", rigGit.verifiedCommits, mgr.mr.CommitSHA)
	}
	if !cleanup.Skipped {
		t.Fatalf("cleanup.Skipped = false, cleanup=%+v", cleanup)
	}
	if len(rigGit.deletedBranches) != 0 || len(rigGit.localDeleted) != 0 {
		t.Fatalf("branch deleted despite skip: remote=%v local=%v", rigGit.deletedBranches, rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_OpenPRSkipsRemoteDeleteAfterProof(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{openPR: true, localHead: mgr.mr.CommitSHA}

	outcome, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	cleanup := outcome.Cleanup
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if !cleanup.OpenPR {
		t.Fatalf("cleanup.OpenPR = false, cleanup=%+v", cleanup)
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted despite open PR: %v", rigGit.deletedBranches)
	}
	if len(rigGit.localDeleted) != 1 || rigGit.localDeleted[0] != mgr.mr.Branch {
		t.Fatalf("local branch cleanup = %v, want [%s]", rigGit.localDeleted, mgr.mr.Branch)
	}
}

func TestRunVerifiedMQPostMerge_LeaseDeleteFailureReturnsAfterPostMerge(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{remoteTip: mgr.mr.CommitSHA, deleteErr: errors.New("stale info")}

	_, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err == nil || !strings.Contains(err.Error(), "remote branch delete") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want remote branch delete failure", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if len(rigGit.deletedBranches) != 1 || rigGit.deletedBranches[0] != mgr.mr.Branch {
		t.Fatalf("remote delete attempts = %v, want [%s]", rigGit.deletedBranches, mgr.mr.Branch)
	}
	if len(rigGit.deletedHeads) != 1 || rigGit.deletedHeads[0] != mgr.mr.CommitSHA {
		t.Fatalf("delete lease heads = %v, want [%s]", rigGit.deletedHeads, mgr.mr.CommitSHA)
	}
	if len(rigGit.localDeleted) != 0 {
		t.Fatalf("local branch deleted after remote lease failure: %v", rigGit.localDeleted)
	}
}

func TestRunVerifiedMQPostMerge_MissingRemoteBranchIsIdempotentAfterProof(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{localHead: mgr.mr.CommitSHA}

	outcome, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	cleanup := outcome.Cleanup
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after successful proof")
	}
	if !cleanup.AlreadyGone {
		t.Fatalf("cleanup.AlreadyGone = false, cleanup=%+v", cleanup)
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch delete attempted for missing branch: %v", rigGit.deletedBranches)
	}
}

func TestRunVerifiedMQPostMerge_MissingSubmittedHeadFailsClosed(t *testing.T) {
	mr := testMQPostMergeMR()
	mr.CommitSHA = ""
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{}

	_, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err == nil || !strings.Contains(err.Error(), "missing submitted commit_sha") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want missing submitted head", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called with missing submitted head")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("branch deleted with missing submitted head: %v", rigGit.deletedBranches)
	}
}

func TestRunVerifiedMQPostMerge_SourceTargetBranchFailsClosed(t *testing.T) {
	mr := testMQPostMergeMR()
	mr.Branch = "main"
	mr.TargetBranch = "main"
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{}

	_, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err == nil || !strings.Contains(err.Error(), "matches target branch") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want source/target failure", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called when source branch matched target")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("branch deleted when source matched target: %v", rigGit.deletedBranches)
	}
}

// A rebased merge is the documented refinery procedure, not a failed merge:
// the submitted sha is absent from the target precisely BECAUSE the branch was
// rebased. Post-merge must accept the combined-diff patch-id proof and clean up.
func TestRunVerifiedMQPostMerge_RebasedMergeIsProvenByPatchID(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	patchProof := &git.LandedProof{
		Method:     git.LandedProofPatchID,
		Submitted:  mgr.mr.CommitSHA,
		Base:       "af9173c",
		LandedBase: "9676a9c",
		LandedHead: "3ca43e6",
		PatchID:    "73f5a3f3add3ec15",
	}
	rigGit := &fakeMQPostMergeGit{
		verifyErr: errors.New("verified_push_failed: commit not on origin/main"),
		proofs:    map[string]*git.LandedProof{mgr.mr.CommitSHA: patchProof},
		remoteTip: mgr.mr.CommitSHA,
		localHead: mgr.mr.CommitSHA,
	}

	outcome, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge on a rebased merge: %v", err)
	}
	if outcome.Proof == nil || outcome.Proof.Method != git.LandedProofPatchID {
		t.Fatalf("proof = %+v, want a patch-id proof", outcome.Proof)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge was not called after a patch-id proof")
	}
	if !outcome.Cleanup.RemoteDeleted || outcome.Cleanup.DeletedAt != mgr.mr.CommitSHA {
		t.Fatalf("cleanup = %+v, want remote delete at the submitted head", outcome.Cleanup)
	}
	if !strings.Contains(outcome.Proof.Describe(), "73f5a3f3add3ec15") {
		t.Fatalf("proof description %q does not carry the patch-id", outcome.Proof.Describe())
	}
}

// Fail-closed stays fail-closed: with neither ancestry nor patch-id, nothing is
// closed and nothing is deleted, and the error names both questions asked.
func TestRunVerifiedMQPostMerge_UnprovenMergeStillRefuses(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{
		unproven: map[string]error{mgr.mr.CommitSHA: errors.New("verified_push_failed: commit not on origin/main; patch_id_landing_not_found: no matching range")},
	}

	_, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err == nil || !strings.Contains(err.Error(), "merge proof failed") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want merge proof failure", err)
	}
	if !strings.Contains(err.Error(), "patch_id_landing_not_found") {
		t.Fatalf("proof error %q does not report the patch-id search", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("PostMerge called after an unproven merge")
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("remote branch deleted after an unproven merge: %v", rigGit.deletedBranches)
	}
}

// cl-z37k: review legitimately moves a branch after submit. The delete lease
// must follow the branch to its current tip — once that tip is proven landed.
func TestRunVerifiedMQPostMerge_MovedTipDeletesAtCurrentTip(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	const revisedTip = "a21dbfa0000000000000000000000000000000ff"
	rigGit := &fakeMQPostMergeGit{
		remoteTip: revisedTip,
		localHead: revisedTip,
		proofs: map[string]*git.LandedProof{
			mgr.mr.CommitSHA: {Method: git.LandedProofPatchID, Submitted: mgr.mr.CommitSHA},
			revisedTip:       {Method: git.LandedProofAncestry, Submitted: revisedTip},
		},
	}

	outcome, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err != nil {
		t.Fatalf("runVerifiedMQPostMerge with a moved tip: %v", err)
	}
	cleanup := outcome.Cleanup
	if !cleanup.RemoteDeleted || cleanup.DeletedAt != revisedTip {
		t.Fatalf("cleanup = %+v, want remote delete at the current tip %s", cleanup, revisedTip)
	}
	if !cleanup.TipMoved || cleanup.TipProof == nil {
		t.Fatalf("cleanup = %+v, want a recorded proof for the moved tip", cleanup)
	}
	if len(rigGit.deletedHeads) != 1 || rigGit.deletedHeads[0] != revisedTip {
		t.Fatalf("delete lease heads = %v, want [%s]", rigGit.deletedHeads, revisedTip)
	}
	if len(rigGit.fetchedBranches) != 1 || rigGit.fetchedBranches[0] != mgr.mr.Branch {
		t.Fatalf("fetched branches = %v, want [%s]", rigGit.fetchedBranches, mgr.mr.Branch)
	}
	if !cleanup.LocalDeleted {
		t.Fatalf("cleanup = %+v, want the local branch at the current tip deleted too", cleanup)
	}
}

// Following the branch must not become a way to discard unreviewed work: a tip
// that moved to something NOT on the target is refused, loudly, with both shas.
func TestRunVerifiedMQPostMerge_MovedTipNotLandedRefusesDelete(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	const unmergedTip = "deadbee00000000000000000000000000000000f"
	rigGit := &fakeMQPostMergeGit{
		remoteTip: unmergedTip,
		proofs:    map[string]*git.LandedProof{mgr.mr.CommitSHA: {Method: git.LandedProofAncestry, Submitted: mgr.mr.CommitSHA}},
		unproven:  map[string]error{unmergedTip: errors.New("patch_id_landing_not_found: no matching range")},
	}

	_, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, false)
	if err == nil || !strings.Contains(err.Error(), "not proven landed") {
		t.Fatalf("runVerifiedMQPostMerge error = %v, want a refusal to delete an unlanded tip", err)
	}
	if !strings.Contains(err.Error(), mgr.mr.CommitSHA) || !strings.Contains(err.Error(), unmergedTip) {
		t.Fatalf("refusal %q must name both the submitted head and the current tip", err)
	}
	if len(rigGit.deletedBranches) != 0 {
		t.Fatalf("branch deleted at an unproven tip: %v", rigGit.deletedBranches)
	}
	if len(rigGit.localDeleted) != 0 {
		t.Fatalf("local branch deleted at an unproven tip: %v", rigGit.localDeleted)
	}
}

// An MR is not always the whole bead: --skip-issue-close reaches the manager so
// a partial landing cannot close the source issue.
func TestRunVerifiedMQPostMerge_SkipIssueClosePassesThrough(t *testing.T) {
	mgr := &fakeMQPostMergeManager{mr: testMQPostMergeMR()}
	rigGit := &fakeMQPostMergeGit{remoteTip: mgr.mr.CommitSHA, localHead: mgr.mr.CommitSHA}

	if _, err := runVerifiedMQPostMerge(mgr, t.TempDir(), rigGit, mgr.mr.ID, false, true); err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeOpts.SkipSourceIssueClose {
		t.Fatalf("PostMergeMR options = %+v, want SkipSourceIssueClose", mgr.postMergeOpts)
	}
}
