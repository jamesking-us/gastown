package tmux

import (
	"runtime"
	"testing"
)

// cl-3qccp: bare session targets are prefix-matched by tmux, so a down seat's name resolved to a
// running seat whose name it prefixes. ExactSessionTarget must add "=" (except on Windows/psmux,
// for pane IDs, and when already exact).
func TestExactSessionTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("psmux has no exact-match prefix")
	}
	cases := map[string]string{
		"cl-crew-compliance": "=cl-crew-compliance",
		"=hq-mayor":          "=hq-mayor",
		"%9":                 "%9",
	}
	for in, want := range cases {
		if got := ExactSessionTarget(in); got != want {
			t.Errorf("ExactSessionTarget(%q) = %q, want %q", in, got, want)
		}
	}
}
