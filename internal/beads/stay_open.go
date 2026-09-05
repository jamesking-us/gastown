package beads

import "strings"

// stayOpenLabels are labels that exempt an issue from automated closure.
var stayOpenLabels = map[string]bool{
	"gt:stay-open":     true,
	"gt:hold-open":     true,
	"gt:no-auto-close": true,
}

// StayOpenReason reports why an issue must not be closed by automation, or ""
// when nothing on the bead says so.
//
// Some beads carry an explicit, human-authored condition for their own closure:
// "this bead survives the merge; it closes on root-cause-found, or after a
// stated no-recurrence interval". Automated close paths — refinery post-merge
// cleanup above all — used to discharge those conditions as a side effect of
// merging, and the only control left was a seat noticing and reopening by hand.
// That control has been measured failing: it held once because the seat happened
// to carry the condition in prior-session context, and failed on the very next
// merge when that accident was absent. A control that depends on which context a
// seat happens to hold is not a control, so the condition is read off the bead.
//
// A bead declares itself exempt with either:
//   - a label: gt:stay-open, gt:hold-open, gt:no-auto-close
//   - a field line in its description, design, notes or acceptance criteria:
//     stay_open: true, keep_open: true, no_auto_close: true, or a stated
//     release_condition / close_condition / reopen_condition
//
// Keys match case-insensitively, treat '-' and ' ' as '_', and tolerate the
// markdown decoration beads are usually written with ("**Release condition:**").
// A condition key stating "none" or "n/a" is an author waiving it, not setting it.
func StayOpenReason(issue *Issue) string {
	if issue == nil {
		return ""
	}
	for _, label := range issue.Labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if stayOpenLabels[normalized] {
			return "label:" + normalized
		}
	}
	for _, text := range []string{issue.Description, issue.Design, issue.Notes, issue.AcceptanceCriteria} {
		if reason := StayOpenTextReason(text); reason != "" {
			return reason
		}
	}
	for _, comment := range issue.Comments {
		if reason := StayOpenTextReason(comment.Text); reason != "" {
			return "comment:" + reason
		}
	}
	return ""
}

// StayOpenTextReason scans one block of free text for a stay-open field line.
// Callers that read comments separately from the issue body use it directly.
func StayOpenTextReason(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		for _, field := range stayOpenFieldCandidates(line) {
			switch field.key {
			case "stay_open", "keep_open", "no_auto_close":
				// The value routinely trails prose ("stay_open: true until the
				// RCA lands"), so the flag is the first word of it.
				if isStayOpenTruthy(firstToken(field.value)) {
					return field.key
				}
			case "release_condition", "close_condition", "reopen_condition":
				if field.value != "" && !isStayOpenWaived(field.value) {
					return field.key
				}
			}
		}
	}
	return ""
}

type stayOpenField struct {
	key   string
	value string
}

// stayOpenFieldCandidates reads every "key: value" a line could be stating.
// Beads are written by hand, so the marker is as often introduced ("ratified:
// release_condition: ...") or decorated ("**Release condition:** ...") as it is
// written bare, and only the segment immediately before a colon is treated as a
// key — prose that merely mentions a key in passing does not match.
func stayOpenFieldCandidates(line string) []stayOpenField {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, ">-*#+ \t")
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return nil
	}

	fields := make([]stayOpenField, 0, len(parts)-1)
	for i := 0; i < len(parts)-1; i++ {
		key := normalizeStayOpenKey(parts[i])
		if key == "" {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.Join(parts[i+1:], ":")), "*`_ \t")
		fields = append(fields, stayOpenField{key: key, value: value})
	}
	return fields
}

// normalizeStayOpenKey lowercases a field key and folds '-' and ' ' to '_', so
// "Release-Condition", "release condition" and "**Release condition**" all match.
func normalizeStayOpenKey(key string) string {
	key = strings.Trim(strings.TrimSpace(key), "*`_ \t")
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	return key
}

func firstToken(value string) string {
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(value), " ", 2)[0])
}

func isStayOpenTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "y", "1", "on":
		return true
	default:
		return false
	}
}

// isStayOpenWaived reports whether a condition value explicitly states that
// there is no condition.
func isStayOpenWaived(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "n/a", "na", "false", "no", "-":
		return true
	default:
		return false
	}
}
