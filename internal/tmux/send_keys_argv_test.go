package tmux

import (
	"strings"
	"testing"
)

// The argv half of the gt-sve regression suite. send_keys_literal_test.go
// covers the literal (-l) sends where the failure was measured; this file
// covers the argument vector itself and the one text-carrying path that was
// wrongly exonerated.

func TestBuildSendKeysArgsTerminatesFlagsBeforeText(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		literal bool
		args    []string
		want    []string
	}{
		{
			name:    "literal text",
			target:  "sess:0.0",
			literal: true,
			args:    []string{"-u hello"},
			want:    []string{"send-keys", "-t", "sess:0.0", "-l", "--", "-u hello"},
		},
		{
			name:    "key-name position with a trailing Enter",
			target:  "%3",
			literal: false,
			args:    []string{"-not-a-flag", "Enter"},
			want:    []string{"send-keys", "-t", "%3", "--", "-not-a-flag", "Enter"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSendKeysArgs(tc.target, tc.literal, tc.args)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("buildSendKeysArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTmuxRejectsUnseparatedLeadingDash pins the tmux grammar the fix rests
// on. Every other test here asserts that delivery works, which would keep
// passing if tmux stopped rejecting the unseparated form — this one fails if
// the premise changes, so the reason for "--" is checked rather than
// remembered.
func TestTmuxRejectsUnseparatedLeadingDash(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-sve-grammar"

	if _, err := tm.run("new-session", "-d", "-s", session, "cat"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _, _ = tm.run("kill-session", "-t", session) }()

	if _, err := tm.run("send-keys", "-t", session, "-l", "-u leading dash"); err == nil {
		t.Error("unseparated '-'-leading text was accepted; the send-keys grammar sendKeysText assumes no longer holds")
	}
	if err := tm.sendKeysLiteral(session, "-u leading dash"); err != nil {
		t.Errorf("sendKeysLiteral with separator: %v", err)
	}
}

// TestSendKeysDebouncedDeliversLeadingDash covers the general `gt nudge`
// path. It is here because SendKeysDebounced was explicitly CLEARED by an
// earlier elimination pass, on the reasoning that it "uses only valid flags
// (-t, -l, then a separate Enter)". That is true of the flags and misses the
// defect: the argument tmux rejected was the message.
//
// The session runs `cat`, so the Enter this path sends echoes the line back
// instead of executing it.
func TestSendKeysDebouncedDeliversLeadingDash(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-sve-debounced"

	if _, err := tm.run("new-session", "-d", "-s", session, "cat"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _, _ = tm.run("kill-session", "-t", session) }()

	const msg = "-u debounced leading dash"
	if err := tm.SendKeysDebounced(session, msg, 50); err != nil {
		t.Fatalf("SendKeysDebounced: %v", err)
	}

	out, err := tm.CapturePaneAll(session)
	if err != nil {
		t.Fatalf("CapturePaneAll: %v", err)
	}
	if !strings.Contains(out, msg) {
		t.Errorf("pane does not contain %q; captured:\n%s", msg, out)
	}
}

// TestSendKeysRawDeliversLeadingDash covers the key-name position, which the
// witness flagged as an unconfirmed review item rather than a known bug. It
// is reachable with caller-supplied text through the exported SendKeysRaw.
func TestSendKeysRawDeliversLeadingDash(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-sve-raw"

	if _, err := tm.run("new-session", "-d", "-s", session, "cat"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _, _ = tm.run("kill-session", "-t", session) }()

	// A key name that begins with "-" is not a valid key, but it must reach
	// tmux's key lookup rather than its flag parser: the failure mode being
	// pinned is "unknown flag", not "unknown key".
	err := tm.SendKeysRaw(session, "-u")
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("SendKeysRaw sent %q into the flag parser: %v", "-u", err)
	}
}
