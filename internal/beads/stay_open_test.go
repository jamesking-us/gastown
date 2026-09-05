package beads

import "testing"

func TestStayOpenReason_DetectsExplicitMarkers(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  string
	}{
		{
			name:  "stay_open field",
			issue: &Issue{ID: "gt-a", Description: "stay_open: true\n"},
			want:  "stay_open",
		},
		{
			name:  "release condition in description",
			issue: &Issue{ID: "gt-a", Description: "release_condition: closes on root-cause-found\n"},
			want:  "release_condition",
		},
		{
			name:  "markdown-decorated key in notes",
			issue: &Issue{ID: "gt-a", Notes: "**Release condition:** 7 days with no recurrence"},
			want:  "release_condition",
		},
		{
			name:  "hyphenated key in design",
			issue: &Issue{ID: "gt-a", Design: "- reopen-condition: any further auto-close"},
			want:  "reopen_condition",
		},
		{
			name:  "spaced key in acceptance criteria",
			issue: &Issue{ID: "gt-a", AcceptanceCriteria: "close condition: mayor ratifies the RCA"},
			want:  "close_condition",
		},
		{
			name:  "keep_open yes",
			issue: &Issue{ID: "gt-a", Description: "Keep-Open: yes"},
			want:  "keep_open",
		},
		{
			name:  "no_auto_close",
			issue: &Issue{ID: "gt-a", Description: "no_auto_close: true"},
			want:  "no_auto_close",
		},
		{
			name:  "stay-open label",
			issue: &Issue{ID: "gt-a", Labels: []string{"gt:stay-open"}},
			want:  "label:gt:stay-open",
		},
		{
			name:  "hold-open label",
			issue: &Issue{ID: "gt-a", Labels: []string{"bug", "GT:Hold-Open"}},
			want:  "label:gt:hold-open",
		},
		{
			name:  "condition carried in a comment",
			issue: &Issue{ID: "gt-a", Comments: []Comment{{Text: "ratified: release_condition: root cause found"}}},
			want:  "comment:release_condition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StayOpenReason(tt.issue); got != tt.want {
				t.Errorf("StayOpenReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStayOpenReason_IgnoresNonMarkers(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
	}{
		{name: "nil issue"},
		{name: "empty issue", issue: &Issue{ID: "gt-a"}},
		{name: "ordinary bead", issue: &Issue{ID: "gt-a", Description: "attached_molecule: gt-wisp-1\nno_merge: false\n"}},
		{name: "stay_open false", issue: &Issue{ID: "gt-a", Description: "stay_open: false"}},
		{name: "stay_open with no value", issue: &Issue{ID: "gt-a", Description: "stay_open:"}},
		{name: "release condition waived", issue: &Issue{ID: "gt-a", Description: "release_condition: none"}},
		{name: "release condition n/a", issue: &Issue{ID: "gt-a", Notes: "Release condition: N/A"}},
		{name: "prose about staying open", issue: &Issue{ID: "gt-a", Description: "The bead should stay open until we know more"}},
		{name: "unrelated label", issue: &Issue{ID: "gt-a", Labels: []string{"gt:keep"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StayOpenReason(tt.issue); got != "" {
				t.Errorf("StayOpenReason() = %q, want \"\"", got)
			}
		})
	}
}

func TestStayOpenTextReason_ScansOneBlock(t *testing.T) {
	if got := StayOpenTextReason("some prose\n> release_condition: RCA lands\nmore prose"); got != "release_condition" {
		t.Errorf("StayOpenTextReason() = %q, want release_condition", got)
	}
	if got := StayOpenTextReason("nothing to see here"); got != "" {
		t.Errorf("StayOpenTextReason() = %q, want \"\"", got)
	}
}
