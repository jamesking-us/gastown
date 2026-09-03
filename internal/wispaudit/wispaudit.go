// Package wispaudit is the one durable record of wisp deletion (hq-6ewp).
//
// The wisps table family — wisps, wisp_comments, wisp_labels, wisp_events,
// wisp_dependencies, wisp_child_counters — is in dolt_ignore, so no wisp table
// is ever committed. `SELECT ... FROM wisps AS OF 'HEAD'` answers
// table-not-found while the same query against issues returns a real count.
// There is no undo and no history: a deleted wisp leaves no trace anywhere in
// Dolt. That is why the 1449 closed hq wisps destroyed on 2026-09-01 were never
// recovered, and why the investigation into who destroyed them had to identify
// its actor by reading source code.
//
// The ignore is staying. It was measured before this package was written
// (2026-09-02, hq database): one day of wisp mutations — 8000 wisp_events on
// 09-01, and that is a lower bound, because a deleted wisp's events cascade
// away with it — exceeds one day of the ENTIRE committed history, 4133 commits,
// against a store already at 335 MB in three days that has already needed its
// history flattened once. Un-ignoring the family multiplies commit volume
// several-fold. So the record lives beside the ignore rather than replacing it.
//
// Two properties the record has to have, and this package exists to hold both
// in one place rather than in six:
//
//  1. It survives the deletion. It is an append-only line in the town's
//     .events.jsonl, not a row in a table the deletion can reach.
//  2. It is written BEFORE the delete. After the delete there is nothing left
//     to name what went. A caller that cannot write the record does not delete.
//
// Every path in this repository that removes rows from the wisps family calls
// Plan first and honours its error. The Paths block below is the census; adding
// a new deleter means adding a constant there and a Plan call beside it.
package wispaudit

import (
	"os"

	"github.com/steveyegge/gastown/internal/events"
)

// Path names the code path doing the deleting. A record without it answers
// "what went" but not "what took it", which is the question the 2026-09-01
// investigation could not answer.
const (
	// PathDonePurge is gt done purging the completing agent's own molecule.
	PathDonePurge = "gt done: molecule purge"
	// PathPolecatNuke is gt polecat nuke purging a rig database.
	PathPolecatNuke = "gt polecat nuke: database purge"
	// PathCompaction is gt compact deleting a closed wisp past its TTL.
	PathCompaction = "gt compact: ttl delete"
	// PathDoltSyncGC is gt dolt sync --gc purging before a push.
	PathDoltSyncGC = "gt dolt sync --gc: pre-push purge"
	// PathMaintain is gt maintain purging before a push.
	PathMaintain = "gt maintain: pre-push purge"
	// PathReaper is the reaper purge, from the daemon patrol or gt reaper purge.
	PathReaper = "reaper: purge closed wisps"
	// PathPatrolDigest is gt patrol removing a day's digest wisps.
	PathPatrolDigest = "gt patrol: digest cleanup"
)

// Phases of a deletion. A "planned" record is the one that matters: it is
// written before anything is removed and so survives a crash mid-delete. A
// "completed" record says what actually went, and is advisory — by the time it
// could fail to be written, the planned record already names the set.
const (
	phasePlanned   = "planned"
	phaseCompleted = "completed"
)

// Wisp is one wisp in a deletion record: enough to know what was lost.
//
// Title is what makes the record useful to a human — an id alone identifies a
// row that no longer exists anywhere. It is allowed to be empty for the paths
// that can only predict their delete set (bd purge reports a count, never the
// ids it removed), and an empty title is honest about that rather than absent.
type Wisp struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// WispsFromIDs builds records for a set of ids whose titles are not available.
func WispsFromIDs(ids []string) []Wisp {
	if len(ids) == 0 {
		return nil
	}
	out := make([]Wisp, 0, len(ids))
	for _, id := range ids {
		out = append(out, Wisp{ID: id})
	}
	return out
}

// IDs returns just the ids of a wisp set.
func IDs(wisps []Wisp) []string {
	if len(wisps) == 0 {
		return nil
	}
	out := make([]string, 0, len(wisps))
	for _, w := range wisps {
		out = append(out, w.ID)
	}
	return out
}

// Plan writes the pre-deletion record and reports whether it landed.
//
// A non-nil error means the deletion must not happen. Callers do not get to
// downgrade that to a warning: an unrecorded wisp deletion is exactly the
// defect this package exists to end, and "the log was unwritable" is a reason
// to stop, not a reason to proceed quietly.
//
// scope describes what the deletion is bounded by — "molecule:hq-wisp-abc",
// "database", "closed_at<2026-08-26T00:00:00Z". db names the database.
func Plan(actor, path, scope, db string, wisps []Wisp, extra map[string]interface{}) error {
	return events.LogAuditDurable(events.TypeWispPurge, actor,
		events.WispPurgePayload(phasePlanned, path, scope, db, wispPayload(wisps), extra))
}

// Completed writes the post-deletion record. It is best-effort by design: the
// deletion has already happened and the planned record already names the set,
// so a failure here loses the outcome, not the evidence. It returns the error
// for callers that want to mention it; nothing should abort on it.
func Completed(actor, path, scope, db string, wisps []Wisp, failures []string, extra map[string]interface{}) error {
	payload := events.WispPurgePayload(phaseCompleted, path, scope, db, wispPayload(wisps), extra)
	if len(failures) > 0 {
		payload["failed"] = failures
	}
	return events.LogAudit(events.TypeWispPurge, actor, payload)
}

// wispPayload converts to the interface slice the events payload carries, so
// internal/events keeps depending on nothing in this package.
func wispPayload(wisps []Wisp) []interface{} {
	out := make([]interface{}, 0, len(wisps))
	for _, w := range wisps {
		entry := map[string]interface{}{"id": w.ID}
		if w.Title != "" {
			entry["title"] = w.Title
		}
		out = append(out, entry)
	}
	return out
}

// Actor names who is deleting, for the packages below internal/cmd that have no
// access to its address detection. GT_ROLE in full-address form ("gastown/
// polecats/nux") is the identity every agent session carries; fallback names
// the process for the paths that run without one, such as the daemon patrol.
func Actor(fallback string) string {
	if role := os.Getenv("GT_ROLE"); role != "" {
		return role
	}
	return fallback
}
