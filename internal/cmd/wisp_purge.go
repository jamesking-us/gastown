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

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
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

	var ids []string
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
		ids = append(ids, w.ID)
	}

	if len(ids) == 0 {
		return
	}

	extra := map[string]interface{}{"molecule": moleculeID}
	if kept > 0 {
		extra["kept_evidence_bearing"] = kept
	}
	if !recordWispPurgePlan(actor, "molecule:"+moleculeID, scopeDB, ids, extra) {
		return
	}

	deleted, failures := deleteWispIDs(bd, ids)
	reportWispPurge(actor, "molecule:"+moleculeID, scopeDB, deleted, failures, extra)
}

// purgeClosedEphemeralBeads purges closed ephemeral beads across the database
// bd is bound to. It remains database-wide because its callers (gt polecat
// nuke and its batch forms) retire polecats whose molecules are already gone,
// so there is no subtree left to walk — but it is no longer age-blind, and no
// longer silent. Narrowing it to per-polecat scope needs the nuke path to
// resolve each retiring polecat's molecule before the sandbox goes; that is
// tracked separately rather than bolted on here.
//
// Best-effort: failures are logged but don't block the caller.
func purgeClosedEphemeralBeads(bd *beads.Beads, actor, scopeDB string) {
	// bd purge reports a count, never the ids it removed, so the ids are
	// enumerated here instead — an approximate list of names beats an exact
	// number when the question later is "what was in there".
	predicted := predictUnscopedPurge(bd)
	if !recordWispPurgePlan(actor, "database", scopeDB, predicted, map[string]interface{}{
		"older_than": unscopedPurgeMinAge,
		"predicted":  true,
	}) {
		return
	}

	out, err := bd.Run("purge", "--force", "--quiet", "--older-than", unscopedPurgeMinAge)
	if err != nil {
		// Non-fatal: purge failure shouldn't block session completion
		fmt.Fprintf(os.Stderr, "Warning: wisp purge failed: %v\n", err)
		return
	}
	// bd purge --force --quiet outputs the count of purged beads
	outStr := strings.TrimSpace(string(out))
	if outStr == "" || outStr == "0" {
		return
	}
	fmt.Fprintf(os.Stderr, "Purged closed ephemeral beads older than %s: %s\n", unscopedPurgeMinAge, outStr)
	reportWispPurge(actor, "database", scopeDB, predicted, nil, map[string]interface{}{
		"older_than":     unscopedPurgeMinAge,
		"predicted":      true,
		"reported_count": outStr,
	})
}

// predictUnscopedPurge names the wisps `bd purge --older-than` is expected to
// remove from this database. It is a prediction, not a result — bd applies its
// own definition of "closed ephemeral" and protects pinned rows — so callers
// record it as such. An error here is not fatal: an approximate receipt is
// still better than no receipt, and a purge with an empty prediction still
// runs under its age floor.
func predictUnscopedPurge(bd *beads.Beads) []string {
	all, err := listAllWisps(bd)
	if err != nil {
		return nil
	}
	cutoff := time.Now().UTC().Add(-unscopedPurgeMinAgeDuration)
	var ids []string
	for _, w := range all {
		if w.Status != "closed" {
			continue
		}
		if closedAt, ok := wispClosedAt(w); ok && closedAt.After(cutoff) {
			continue
		}
		ids = append(ids, w.ID)
	}
	return ids
}

// wispClosedAt reports when a wisp was closed, falling back to its last update
// when bd omits closed_at. A wisp whose timestamp will not parse reports false,
// and the caller then treats it as old enough — matching bd, which is the thing
// actually deciding.
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
func recordWispPurgePlan(actor, scope, scopeDB string, ids []string, extra map[string]interface{}) bool {
	err := events.LogAuditDurable(events.TypeWispPurge,
		actor, events.WispPurgePayload("planned", scope, scopeDB, ids, extra))
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
func reportWispPurge(actor, scope, scopeDB string, deleted []string, failures []string, extra map[string]interface{}) {
	payload := events.WispPurgePayload("completed", scope, scopeDB, deleted, extra)
	if len(failures) > 0 {
		payload["failed"] = failures
	}
	if err := events.LogAudit(events.TypeWispPurge, actor, payload); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't record wisp purge completion: %v\n", err)
	}
}

// deleteWispIDs deletes ids in batches, returning what went and what didn't.
// A failed batch is reported rather than retried one id at a time: the plan
// record already names every id, so a partial purge is auditable as it stands.
func deleteWispIDs(bd *beads.Beads, ids []string) (deleted []string, failures []string) {
	for start := 0; start < len(ids); start += maxWispDeleteBatch {
		end := start + maxWispDeleteBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		args := append([]string{"delete"}, batch...)
		args = append(args, "--force")
		if _, err := bd.Run(args...); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", strings.Join(batch, ","), err))
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
