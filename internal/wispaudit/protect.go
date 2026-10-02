package wispaudit

import "strings"

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
