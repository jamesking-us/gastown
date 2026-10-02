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
