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
		key, value, ok := splitStayOpenFieldLine(line)
		if !ok {
			continue
		}
		switch key {
		case "stay_open", "keep_open", "no_auto_close":
			if isStayOpenTruthy(value) {
				return key
			}
		case "release_condition", "close_condition", "reopen_condition":
			if value != "" && !isStayOpenWaived(value) {
				return key
			}
		}
	}
	return ""
}

// splitStayOpenFieldLine parses "key: value" out of a line, normalizing the key
// and tolerating the list, quote and emphasis markers around it.
func splitStayOpenFieldLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, ">-*#+ \t")
	colonIdx := strings.Index(line, ":")
	if colonIdx <= 0 {
		return "", "", false
	}
	key = normalizeStayOpenKey(line[:colonIdx])
	if key == "" {
		return "", "", false
	}
	value = strings.Trim(strings.TrimSpace(line[colonIdx+1:]), "*`_ \t")
	return key, value, true
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
