package wispaudit

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Protected-bead rules for implicit wisp purges (gt-12f / cl-kf00).
//
// A database-wide or age-based purge must never remove a merge-request bead
// or a bead carrying a compliance verdict, no matter how old or how closed it
// is. Both properties are keyed on structured fields — a label and a comment
// author — never on a marker string inside free text, because text markers
// are trivially missed by a rephrase and give a false sense of safety.

// ProtectedLabel marks a merge-request bead. gt done applies it to every MR
// bead it creates (internal/scheduler/capacity/pipeline.go: LabelMergeRequest),
// and these beads carry the compliance verdicts cl-kf00 requires survive.
const ProtectedLabel = "gt:merge-request"

// complianceSeats names the crew addresses whose comments make a bead
// evidence a purge must not destroy. Matched on trailing address segments —
// "<rig>/crew/compliance" or a bare "crew/compliance" — never on substrings
// of comment text.
var complianceSeats = []string{"crew/compliance", "crew/compliance_b"}

// IsComplianceSeatAuthor reports whether author is a compliance seat address,
// e.g. "cloudcontentmanager/crew/compliance" or "gastown/crew/compliance_b".
func IsComplianceSeatAuthor(author string) bool {
	author = strings.TrimSpace(author)
	if author == "" {
		return false
	}
	for _, seat := range complianceSeats {
		if author == seat || strings.HasSuffix(author, "/"+seat) {
			return true
		}
	}
	return false
}

// HasProtectedLabel reports whether labels contains ProtectedLabel.
func HasProtectedLabel(labels []string) bool {
	for _, l := range labels {
		if l == ProtectedLabel {
			return true
		}
	}
	return false
}

// AnyComplianceSeatAuthor reports whether any comment author in authors is a
// compliance seat.
func AnyComplianceSeatAuthor(authors []string) bool {
	for _, a := range authors {
		if IsComplianceSeatAuthor(a) {
			return true
		}
	}
	return false
}

// CommentsProtected parses the raw output of `bd comments <id> --json` and
// reports whether any comment author is a compliance seat (protected), and
// whether the output could be read as a comment list at all (readable).
//
// Callers MUST treat readable=false as "do not purge" — never as "not
// protected". An empty, well-formed JSON array ("[]" or prose a caller has
// already recognized as "no comments") is the one case this function treats
// as readable with nothing found; anything else that doesn't parse as a JSON
// array of comments is an unknown state, not a safe one (gt-12f round 2: a
// prior version of this check treated any non-array output as "no comments",
// which let an actually-unreadable candidate through as unprotected).
func CommentsProtected(raw []byte) (protected, readable bool) {
	protected, readable, _ = CommentsProtectedCount(raw)
	return protected, readable
}

// CommentsProtectedCount is CommentsProtected with the number of decoded
// comments. Callers with a comment_count from a candidate listing must compare
// it: an empty or truncated comment response is unreadable when it contradicts
// that count, even if it is syntactically valid JSON.
func CommentsProtectedCount(raw []byte) (protected, readable bool, count int) {
	raw = bytes.TrimSpace(raw)
	start := bytes.IndexByte(raw, '[')
	if start < 0 {
		return false, false, 0
	}
	raw = raw[start:]
	var comments []struct {
		Author string `json:"author"`
	}
	if err := json.Unmarshal(raw, &comments); err != nil {
		return false, false, 0
	}
	for _, c := range comments {
		if IsComplianceSeatAuthor(c.Author) {
			return true, true, len(comments)
		}
	}
	return false, true, len(comments)
}
