package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// Merge-proof methods, in the order they are attempted.
const (
	// LandedProofAncestry means the submitted head is itself reachable from the
	// target: the branch was merged without being rewritten.
	LandedProofAncestry = "ancestry"

	// LandedProofPatchID means the submitted head is not on the target, but the
	// change it introduced is: a range of the target's first-parent history has
	// the same combined-diff patch-id as the submitted range. This is the case
	// produced by the documented refinery procedure, which rebases (and may
	// squash) a branch before merging it.
	LandedProofPatchID = "patch-id"
)

// landedSearchWindow bounds how far back along the target's first-parent history
// a landed range is looked for. Post-merge cleanup normally runs within a few
// merges of the landing, so this is generous.
const landedSearchWindow = 50

// landedMaxSpan bounds how many first-parent commits a single landed range may
// span. A rebase preserves commit count and a squash reduces it to one, so the
// landed range is never longer than the submitted range; this only caps the
// search cost for very long branches.
const landedMaxSpan = 20

// LandedProof records how a submitted head was proven to have landed on a
// target branch. A nil proof never means "landed" — every caller either has a
// proof or an error explaining why the question could not be answered.
type LandedProof struct {
	// Method is LandedProofAncestry or LandedProofPatchID.
	Method string

	// Submitted is the head the MR recorded.
	Submitted string

	// Base is the merge base of the submitted head and the target: the start of
	// the reviewed range. Empty for an ancestry proof.
	Base string

	// LandedBase and LandedHead delimit the range on the target whose combined
	// diff matched. Empty for an ancestry proof.
	LandedBase string
	LandedHead string

	// PatchID is the stable combined-diff patch-id shared by both ranges.
	// Empty for an ancestry proof.
	PatchID string
}

// Describe renders a one-line, auditable summary of the proof.
func (p *LandedProof) Describe() string {
	if p == nil {
		return "unproven"
	}
	if p.Method != LandedProofPatchID {
		return fmt.Sprintf("%s (%s on target)", p.Method, shortSHA(p.Submitted))
	}
	return fmt.Sprintf("%s %s (reviewed %s..%s landed as %s..%s)",
		p.Method, p.PatchID,
		shortSHA(p.Base), shortSHA(p.Submitted),
		shortSHA(p.LandedBase), shortSHA(p.LandedHead))
}

// CombinedDiffPatchID returns the stable patch-id of the combined diff of
// base..head. Unlike a per-commit patch-id it is invariant under BOTH rebase
// and squash, which is exactly the pair of transformations the refinery's merge
// procedure applies to a branch between submit and merge.
//
// An empty string means the range introduces no change; that is not an error,
// but it is never a proof of anything either.
func (g *Git) CombinedDiffPatchID(base, head string) (string, error) {
	diff, err := g.rawOutput("diff", "--no-color", "--no-ext-diff", base, head)
	if err != nil {
		return "", fmt.Errorf("combined diff %s..%s: %w", shortSHA(base), shortSHA(head), err)
	}
	if strings.TrimSpace(diff) == "" {
		return "", nil
	}
	return g.patchID(diff)
}

// rawOutput runs git and returns stdout untrimmed. patch-id hashes the diff
// text, so trailing bytes must survive.
func (g *Git) rawOutput(args ...string) (string, error) {
	if err := g.guardUnsafeTownRootMutation(args); err != nil {
		return "", err
	}
	if g.gitDir != "" {
		args = append([]string{"--git-dir=" + g.gitDir}, args...)
	}

	cmd := exec.Command("git", args...)
	util.SetDetachedProcessGroup(cmd)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", g.wrapError(err, stdout.String(), stderr.String(), args)
	}
	return stdout.String(), nil
}

// patchID feeds a diff to 'git patch-id --stable' and returns the id.
func (g *Git) patchID(diff string) (string, error) {
	cmd := exec.Command("git", "patch-id", "--stable")
	util.SetDetachedProcessGroup(cmd)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}
	cmd.Stdin = strings.NewReader(diff)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git patch-id --stable: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// ProveLandedByPatchID proves that the change submitted at commit has landed on
// targetRef even though commit itself is not an ancestor of it — the shape a
// rebase, a squash, or both leave behind.
//
// The reviewed change is the combined diff of merge-base(commit, targetRef)..commit.
// The target's first-parent history is then searched for a contiguous range with
// the same combined-diff patch-id. Note that tree equality is NOT the check: a
// rebased branch lands on a target that also carries everything else merged in
// the meantime, so the trees legitimately differ while the change is identical.
//
// A returned error means UNPROVEN, which is not the same as "did not land":
// the message distinguishes an inconclusive measurement from a completed search
// that found nothing.
func (g *Git) ProveLandedByPatchID(commit, targetRef string) (*LandedProof, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: empty submitted commit")
	}
	submitted, err := g.Rev(commit + "^{commit}")
	if err != nil {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: submitted commit %s not present locally: %w", shortSHA(commit), err)
	}
	targetTip, err := g.Rev(targetRef + "^{commit}")
	if err != nil {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: cannot resolve target %s: %w", targetRef, err)
	}

	base, err := g.run("merge-base", submitted, targetTip)
	if err != nil {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: no merge base between %s and target %s: %w", shortSHA(submitted), shortSHA(targetTip), err)
	}
	base = strings.TrimSpace(base)

	reviewed, err := g.CombinedDiffPatchID(base, submitted)
	if err != nil {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: %w", err)
	}
	if reviewed == "" {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: submitted range %s..%s introduces no change", shortSHA(base), shortSHA(submitted))
	}

	span := landedMaxSpan
	if n, err := g.CommitsAhead(base, submitted); err == nil && n > 0 && n < span {
		span = n
	}

	chain, err := g.firstParentChain(targetTip, landedSearchWindow)
	if err != nil {
		return nil, fmt.Errorf("patch_id_proof_inconclusive: cannot read first-parent history of target %s: %w", shortSHA(targetTip), err)
	}

	for i := range chain {
		for s := 1; s <= span && i+s < len(chain); s++ {
			landedBase, landedHead := chain[i+s], chain[i]
			id, err := g.CombinedDiffPatchID(landedBase, landedHead)
			if err != nil || id == "" {
				continue
			}
			if id == reviewed {
				return &LandedProof{
					Method:     LandedProofPatchID,
					Submitted:  submitted,
					Base:       base,
					LandedBase: landedBase,
					LandedHead: landedHead,
					PatchID:    reviewed,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("patch_id_landing_not_found: combined-diff patch-id %s of %s..%s does not match any range of the last %d first-parent commits of the target (tip %s)",
		reviewed, shortSHA(base), shortSHA(submitted), len(chain), shortSHA(targetTip))
}

// firstParentChain returns up to n commits of ref's first-parent history,
// newest first.
func (g *Git) firstParentChain(ref string, n int) ([]string, error) {
	out, err := g.run("rev-list", "--first-parent", fmt.Sprintf("-n%d", n), ref)
	if err != nil {
		return nil, err
	}
	var chain []string
	for _, line := range strings.Split(out, "\n") {
		if sha := strings.TrimSpace(line); sha != "" {
			chain = append(chain, sha)
		}
	}
	return chain, nil
}

// VerifyLandedOnPushTarget proves that the work submitted at commit is present
// on remote/branch. It first asks the cheap, exact question — is the submitted
// head an ancestor of the target — and, when the answer is no, asks the question
// that survives the rewrite: did the reviewed change land verbatim?
//
// A rebase is the documented refinery procedure and a gated MR is guaranteed to
// need one, so "the submitted sha is not on the target" is the normal case for a
// successful merge, not evidence of a failed one.
func (g *Git) VerifyLandedOnPushTarget(remote, branch, commit string) (*LandedProof, error) {
	ancestryErr := g.VerifyPushedCommitReachableFromPushTarget(remote, branch, commit)
	if ancestryErr == nil {
		return &LandedProof{Method: LandedProofAncestry, Submitted: strings.TrimSpace(commit)}, nil
	}

	fetchTarget := g.pushTarget(remote)
	if _, err := g.run("fetch", "--no-tags", fetchTarget, "refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("%w; patch_id_proof_inconclusive: unable to fetch %s/%s: %v", ancestryErr, remote, branch, err)
	}

	proof, err := g.ProveLandedByPatchID(commit, "FETCH_HEAD")
	if err != nil {
		return nil, fmt.Errorf("%w; %v", ancestryErr, err)
	}
	return proof, nil
}
