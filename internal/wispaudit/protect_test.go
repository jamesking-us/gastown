package wispaudit

import "testing"

func TestIsComplianceSeatAuthor(t *testing.T) {
	cases := []struct {
		author string
		want   bool
	}{
		{"cloudcontentmanager/crew/compliance", true},
		{"cloudcontentmanager/crew/compliance_b", true},
		{"gastown/crew/compliance", true},
		{"crew/compliance", true},
		{"crew/compliance_b", true},
		{"mayor", false},
		{"gastown/witness", false},
		{"gastown/crew/compliance-reviewer", false}, // not an exact seat segment
		{"notcrew/compliance", false},
		{"", false},
		// Text markers must never substitute for authorship.
		{"gastown/polecats/toast", false},
	}
	for _, c := range cases {
		if got := IsComplianceSeatAuthor(c.author); got != c.want {
			t.Errorf("IsComplianceSeatAuthor(%q) = %v, want %v", c.author, got, c.want)
		}
	}
}

func TestHasProtectedLabel(t *testing.T) {
	cases := []struct {
		labels []string
		want   bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"gt:keep"}, false},
		{[]string{"gt:merge-request"}, true},
		{[]string{"gt:task", "gt:merge-request"}, true},
	}
	for _, c := range cases {
		if got := HasProtectedLabel(c.labels); got != c.want {
			t.Errorf("HasProtectedLabel(%v) = %v, want %v", c.labels, got, c.want)
		}
	}
}

func TestAnyComplianceSeatAuthor(t *testing.T) {
	if AnyComplianceSeatAuthor([]string{"mayor", "gastown/witness"}) {
		t.Error("expected no compliance seat author")
	}
	if !AnyComplianceSeatAuthor([]string{"mayor", "cloudcontentmanager/crew/compliance"}) {
		t.Error("expected compliance seat author to be found")
	}
}

func TestCommentsProtected(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wantProtected bool
		wantReadable  bool
	}{
		{"empty array, no comments", `[]`, false, true},
		{"unprotected comment", `[{"author":"gastown/polecats/toast"}]`, false, true},
		{"compliance seat comment", `[{"author":"cloudcontentmanager/crew/compliance"}]`, true, true},
		{"mixed, one compliance", `[{"author":"mayor"},{"author":"crew/compliance_b"}]`, true, true},
		{"warning prefix before array", "Warning: stale cache\n[{\"author\":\"mayor\"}]", false, true},
		// gt-12f round 2: a prior version of this check treated any
		// non-array output as "no comments" (false, true) — exactly the
		// fail-open bug this function exists to close. Anything that is not
		// a parseable JSON array must be unreadable, never "unprotected".
		{"not found prose", "Error: issue not found", false, false},
		{"empty string", "", false, false},
		{"malformed json", `[{"author":`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			protected, readable := CommentsProtected([]byte(c.raw))
			if protected != c.wantProtected || readable != c.wantReadable {
				t.Errorf("CommentsProtected(%q) = (%v, %v), want (%v, %v)",
					c.raw, protected, readable, c.wantProtected, c.wantReadable)
			}
		})
	}
}
