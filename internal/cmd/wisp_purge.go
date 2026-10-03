package cmd

// Scoped wisp purging (hq-g3zx).
//
// gt done used to end every polecat completion with a bare
// `bd purge --force --quiet`. Per `bd purge --help`, --force without
// --older-than means "delete all closed ephemeral beads" — the WHOLE rig
// database, at any age, belonging to anyone. Measurement bore that out: busy
// rigs sat at exactly zero closed wisps around the clock while the town DB,
// which no polecat runs gt done against, held hundreds. One polecat finishing
// unrelated work erased every other agent's closed wisps in the rig.
//
// The loss is unrecoverable and unauditable: wisps and wisp_% are in
// dolt_ignore (hq-6ewp), so no wisp table is ever committed and there is no
// AS OF to read back. Nothing but an external record can say what was removed.
//
// Two rules follow, and both are enforced here rather than left to callers:
//
//  1. Scope. gt done purges only the completing agent's OWN molecule subtree.
//     If the molecule is unknown, the answer is "purge nothing" — an unknown
//     scope must fail closed, never widen to the database.
//  2. Receipt before deletion. The audit record naming every id is written
//     BEFORE the delete, because after the delete there is nothing left to
//     name them. If the record cannot be written, the delete does not happen.
//
// Rule 2 is no longer local to this file. hq-6ewp generalized it: every path in
// the tree that removes rows from the wisps family — compaction, the reaper, the
// pre-push GC, the patrol digest cleanup — writes to the same log through
// internal/wispaudit, under the same refuse-if-unrecordable rule. The helpers
// below are this file's callers of it.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/wispaudit"
)

// unscopedPurgeMinAge is the age floor for the purge paths that still operate
// on a whole database (polecat nuke). Age-blindness is what made the old call
// destroy live evidence, so even an unscoped purge now leaves a week of
// forensic headroom — the same span as the longest wisp TTL in compact.go.
const unscopedPurgeMinAge = "7d"

// unscopedPurgeMinAgeDuration is unscopedPurgeMinAge as a duration, used only
// to predict the purge set for the audit record. bd owns the real decision.
const unscopedPurgeMinAgeDuration = 7 * 24 * time.Hour

// maxWispDeleteBatch caps how many ids go into a single `bd delete` argv.
const maxWispDeleteBatch = 100

// purgeCandidate is a wisp considered for deletion. It carries the fields the
// decision needs, including the ones bd only reports in list/query output.
type purgeCandidate struct {
	beads.Issue
	CommentCount int `json:"comment_count"`
}

// listAllWisps returns every wisp in the database bd is bound to.
//
// This deliberately does not use compact.go's listWisps: that one goes through
// `bd list`, which does not surface wisps (hq-v9t), so it can silently return
// an empty set. `bd query ephemeral=true --all` is the form that actually
// reaches the wisps table.
func listAllWisps(bd *beads.Beads) ([]*purgeCandidate, error) {
	out, err := bd.Run("query", "--json", "ephemeral=true", "--all", "--limit=0")
	if err != nil {
		return nil, err
	}
	out = extractJSONArray(out)
	// bd answers an empty result set with prose ("No issues found."), which
	// extractJSONArray leaves untouched because there is no '[' to find.
	if len(out) == 0 || out[0] != '[' {
		return nil, nil
	}
	var wisps []*purgeCandidate
	if err := json.Unmarshal(out, &wisps); err != nil {
		return nil, fmt.Errorf("parsing wisp query output: %w", err)
	}
	return wisps, nil
}

// moleculeSubtree returns rootID's wisp plus every wisp reachable from it
// through parent links. Walking an in-memory index rather than issuing one
// `bd list --parent` per node keeps this to a single bd call on a path that
// runs on every completion.
func moleculeSubtree(all []*purgeCandidate, rootID string) []*purgeCandidate {
	byParent := make(map[string][]*purgeCandidate, len(all))
	byID := make(map[string]*purgeCandidate, len(all))
	for _, w := range all {
		byID[w.ID] = w
		if w.Parent != "" {
			byParent[w.Parent] = append(byParent[w.Parent], w)
		}
	}

	var subtree []*purgeCandidate
	seen := map[string]bool{}
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue // cycle or diamond: never revisit
		}
		seen[id] = true
		if w, ok := byID[id]; ok {
			subtree = append(subtree, w)
		}
		for _, child := range byParent[id] {
			queue = append(queue, child.ID)
		}
	}
	return subtree
}

// isEvidenceBearing reports whether a wisp holds something a human wrote or
// asked to keep. compact.go promotes these rather than deleting them; the
// purge path has no promotion step, so it simply leaves them alone.
func isEvidenceBearing(w *purgeCandidate) bool {
	if w.CommentCount > 0 {
		return true
	}
	if wispaudit.HasProtectedLabel(w.Labels) {
		return true
	}
	for _, label := range w.Labels {
		if label == "keep" || label == "gt:keep" {
			return true
		}
	}
	return false
}

// purgeOwnClosedWisps deletes the closed wisps of the completing agent's own
// molecule subtree, and nothing else.
//
// moleculeID is the agent's attached molecule root. An empty moleculeID means
// the scope could not be determined, and the correct response to an unknown
// scope is to purge nothing at all — the whole defect this replaces was a
// missing scope silently meaning "everything".
//
// Best-effort with respect to completion: nothing here blocks gt done. It is
// not best-effort with respect to the audit record — see recordWispPurgePlan.
func purgeOwnClosedWisps(bd *beads.Beads, actor, scopeDB, moleculeID string) {
	if moleculeID == "" {
		// No molecule, no owned wisps. Say so, so the absence of a purge line
		// is not read as a silent failure.
		fmt.Fprintf(os.Stderr, "Note: no attached molecule — skipping wisp purge (nothing owned to purge)\n")
		return
	}

	all, err := listAllWisps(bd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't list wisps for scoped purge: %v\n", err)
		return
	}

	var doomed []wispaudit.Wisp
	kept := 0
	for _, w := range moleculeSubtree(all, moleculeID) {
		// Closed only. A wisp that is open, blocked, in_progress or pinned is
		// live state, and pinned rows are protected by policy (hq-gk8d interim).
		if w.Status != "closed" {
			continue
		}
		if isEvidenceBearing(w) {
			kept++
			continue
		}
		doomed = append(doomed, wispaudit.Wisp{ID: w.ID, Title: w.Title})
	}

	if len(doomed) == 0 {
		return
	}

	extra := map[string]interface{}{"molecule": moleculeID}
	if kept > 0 {
		extra["kept_evidence_bearing"] = kept
	}
	scope := "molecule:" + moleculeID
	if !recordWispPurgePlan(actor, wispaudit.PathDonePurge, scope, scopeDB, doomed, extra) {
		return
	}

	deleted, failures := deleteWisps(bd, doomed)
	reportWispPurge(actor, wispaudit.PathDonePurge, scope, scopeDB, bd, deleted, failures, extra)
}

// purgeExclusion counts wisps withheld from an unscoped purge, by reason.
// gt-12f requires these exclusions be visible in the audit record, not just
// applied silently.
type purgeExclusion struct {
	mergeRequest int // excluded via ProtectedLabel (gt:merge-request)
	compliance   int // excluded via a compliance-seat comment author
	unreadable   int // comments could not be read — failed closed
}

// asExtra renders non-zero exclusion counts for the audit record's extra
// fields. A zero count is omitted rather than printed as noise.
func (e purgeExclusion) asExtra(extra map[string]interface{}) {
	if e.mergeRequest > 0 {
		extra["kept_merge_request"] = e.mergeRequest
	}
	if e.compliance > 0 {
		extra["kept_compliance_commented"] = e.compliance
	}
	if e.unreadable > 0 {
		extra["kept_unreadable"] = e.unreadable
	}
}

// purgeClosedEphemeralBeads purges closed ephemeral beads across the database
// bd is bound to. It remains database-wide because its callers (gt polecat
// nuke and its batch forms) retire polecats whose molecules are already gone,
// so there is no subtree left to walk — but it is no longer age-blind, and no
// longer silent. Narrowing it to per-polecat scope needs the nuke path to
// resolve each retiring polecat's molecule before the sandbox goes; that is
// tracked separately rather than bolted on here.
//
// gt-12f: this purge is no longer delegated to `bd purge --older-than`, which
// has no concept of a protected bead and would delete a merge-request bead or
// a compliance-commented bead exactly as readily as anything else. The
// candidate set is computed and filtered here instead, and the delete is the
// same explicit, by-id path planUnscopedPurge's callers (including --dry-run)
// already see — so a dry-run can never show a different set than the one that
// actually gets deleted.
//
// Best-effort: failures are logged but don't block the caller.
func purgeClosedEphemeralBeads(bd *beads.Beads, actor, scopeDB string) {
	doomed, excluded, err := planUnscopedPurge(bd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't list wisps for purge: %v\n", err)
		return
	}
	if len(doomed) == 0 {
		return
	}

	extra := map[string]interface{}{"older_than": unscopedPurgeMinAge}
	excluded.asExtra(extra)

	if !recordWispPurgePlan(actor, wispaudit.PathPolecatNuke, "database", scopeDB, doomed, extra) {
		return
	}

	deleted, failures := deleteWisps(bd, doomed)
	reportWispPurge(actor, wispaudit.PathPolecatNuke, "database", scopeDB, bd, deleted, failures, extra)
}

// planUnscopedPurge returns the exact wisps the database-wide purge would
// delete — closed, older than unscopedPurgeMinAge, and not protected — plus a
// count of what was excluded and why.
//
// This is the single source of truth for both the real purge and --dry-run:
// gt-12f's bug report named "the purge is not discoverable before the act" as
// part of the defect, and a dry-run that calls a different function than the
// real delete can drift from it silently. Both call this one.
//
// Fails closed per cl-kf00: a wisp labelled gt:merge-request, or carrying a
// comment from a compliance seat, is excluded on that structured signal alone
// — never on a marker string in title or description text, which a rephrase
// can miss. A wisp whose comments cannot be read is excluded too; an unknown
// protection state is not evidence of safety to delete.
func planUnscopedPurge(bd *beads.Beads) ([]wispaudit.Wisp, purgeExclusion, error) {
	all, err := listAllWisps(bd)
	if err != nil {
		return nil, purgeExclusion{}, err
	}

	cutoff := time.Now().UTC().Add(-unscopedPurgeMinAgeDuration)
	var doomed []wispaudit.Wisp
	var excluded purgeExclusion
	for _, w := range all {
		if w.Status != "closed" {
			continue
		}
		closedAt, ok := wispClosedAt(w)
		if !ok {
			// An absent or malformed closure time is not evidence that this row
			// cleared the mandatory seven-day floor. Keep it rather than silently
			// turning an unparseable value into "old".
			excluded.unreadable++
			continue
		}
		if closedAt.After(cutoff) {
			continue
		}
		if wispaudit.HasProtectedLabel(w.Labels) {
			excluded.mergeRequest++
			continue
		}
		if w.CommentCount > 0 {
			protected, readable := commentsAreProtected(bd, w.ID, w.CommentCount)
			if !readable {
				excluded.unreadable++
				continue
			}
			if protected {
				excluded.compliance++
				continue
			}
		}
		doomed = append(doomed, wispaudit.Wisp{ID: w.ID, Title: w.Title})
	}
	return doomed, excluded, nil
}

// commentsAreProtected reports whether id carries a comment from a compliance
// seat, and whether its comments could be read at all. Callers must treat
// readable=false as "do not purge" (fail closed), not as "not protected".
func commentsAreProtected(bd *beads.Beads, id string, expectedCount int) (protected, readable bool) {
	comments, err := bd.Comments(id)
	if err != nil {
		return false, false
	}
	if len(comments) != expectedCount {
		return false, false
	}
	authors := make([]string, 0, len(comments))
	for _, c := range comments {
		authors = append(authors, c.Author)
	}
	return wispaudit.AnyComplianceSeatAuthor(authors), true
}

// wispClosedAt reports when a wisp was closed, falling back to its last update
// when bd omits closed_at. A wisp whose timestamp will not parse reports false;
// purge planning treats that as unknown and keeps it, preserving the age floor.
func wispClosedAt(w *purgeCandidate) (time.Time, bool) {
	ts := w.ClosedAt
	if ts == "" {
		ts = w.UpdatedAt
	}
	if ts == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// recordWispPurgePlan writes the pre-deletion audit record and reports whether
// it landed. It returns false when the record could not be written, and every
// caller treats that as "do not delete".
//
// The ordering is the point. Wisps are in dolt_ignore, so a deleted wisp
// leaves no trace anywhere else; a receipt written after the delete is lost
// exactly when it matters most (a crash mid-purge). Writing first costs one
// append and makes the set recoverable-by-name in every case.
func recordWispPurgePlan(actor, path, scope, scopeDB string, wisps []wispaudit.Wisp, extra map[string]interface{}) bool {
	err := wispaudit.Plan(actor, path, scope, scopeDB, wisps, extra)
	if err == nil {
		return true
	}
	fmt.Fprintf(os.Stderr,
		"Warning: skipping wisp purge — could not write the audit record first: %v\n", err)
	return false
}

// reportWispPurge writes the post-deletion record. Unlike the plan record this
// one is advisory: the deletion already happened, and the plan record already
// names the ids.
func reportWispPurge(actor, path, scope, scopeDB string, bd *beads.Beads, deleted []wispaudit.Wisp, failures []string, extra map[string]interface{}) {
	verified, survivors, verifyErr := confirmWispsGone(bd, deleted)
	if verifyErr != nil {
		extra["verify_error"] = verifyErr.Error()
		failures = append(failures, "post-delete verification: "+verifyErr.Error())
	} else if len(survivors) > 0 {
		extra["survived_purge"] = survivors
		for _, id := range survivors {
			failures = append(failures, id+": still present after purge")
		}
	}

	var err error
	if len(failures) > 0 || verifyErr != nil {
		err = wispaudit.Partial(actor, path, scope, scopeDB, verified, failures, extra)
	} else {
		err = wispaudit.Completed(actor, path, scope, scopeDB, verified, nil, extra)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't record wisp purge completion: %v\n", err)
	}
}

// confirmWispsGone reads the just-deleted IDs back from bd. A successful
// delete command is not evidence that every row disappeared: only IDs absent
// from this post-delete query may be named in a completed audit receipt.
func confirmWispsGone(bd *beads.Beads, wisps []wispaudit.Wisp) (verified []wispaudit.Wisp, survivors []string, err error) {
	if len(wisps) == 0 {
		return nil, nil, nil
	}
	args := append([]string{"show", "--json"}, wispaudit.IDs(wisps)...)
	out, err := bd.Run(args...)
	if err != nil {
		return nil, nil, fmt.Errorf("reading deleted wisps: %w", err)
	}
	out = extractJSONArray(out)
	if len(out) == 0 || out[0] != '[' {
		return nil, nil, fmt.Errorf("reading deleted wisps: expected JSON array")
	}
	var found []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &found); err != nil {
		return nil, nil, fmt.Errorf("parsing deleted wisp check: %w", err)
	}
	foundIDs := make(map[string]struct{}, len(found))
	for _, w := range found {
		foundIDs[w.ID] = struct{}{}
	}
	for _, w := range wisps {
		if _, found := foundIDs[w.ID]; found {
			survivors = append(survivors, w.ID)
			continue
		}
		verified = append(verified, w)
	}
	return verified, survivors, nil
}

// deleteWisps deletes wisps in batches, returning what went and what didn't.
// A failed batch is reported rather than retried one id at a time: the plan
// record already names every id, so a partial purge is auditable as it stands.
func deleteWisps(bd *beads.Beads, wisps []wispaudit.Wisp) (deleted []wispaudit.Wisp, failures []string) {
	for start := 0; start < len(wisps); start += maxWispDeleteBatch {
		end := start + maxWispDeleteBatch
		if end > len(wisps) {
			end = len(wisps)
		}
		batch := wisps[start:end]
		ids := wispaudit.IDs(batch)
		args := append([]string{"delete"}, ids...)
		args = append(args, "--force")
		if _, err := bd.Run(args...); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", strings.Join(ids, ","), err))
			continue
		}
		deleted = append(deleted, batch...)
	}
	if len(deleted) > 0 {
		fmt.Fprintf(os.Stderr, "Purged %d closed wisp(s) from this session's molecule\n", len(deleted))
	}
	for _, f := range failures {
		fmt.Fprintf(os.Stderr, "Warning: wisp delete failed: %s\n", f)
	}
	return deleted, failures
}
