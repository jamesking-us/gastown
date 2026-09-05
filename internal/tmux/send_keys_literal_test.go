package tmux

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSendKeysLiteralAcceptsLeadingDash is the regression test for gt-sve.
//
// tmux parses a subcommand's arguments with getopt(3), which keeps scanning
// for flags after "-l". Without a "--" terminator, literal text beginning with
// "-" is read as flags and the command fails outright — on tmux 3.4,
// "tmux send-keys -t pane -l '-u hello'" reports "command send-keys: unknown
// flag -u" and delivers nothing. Nudge text arrives here in fixed-size chunks,
// so which byte lands first in a chunk is an accident of message length, and
// any leading "-" is enough.
func TestSendKeysLiteralAcceptsLeadingDash(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-sve-literal"

	if _, err := tm.run("new-session", "-d", "-s", session, "cat"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _, _ = tm.run("kill-session", "-t", session) }()

	// "-u" is the flag the production failure reported, but the defect is the
	// leading "-", not the letter: a batch splitting "<system-reminder>"
	// reports "-r" from the same code path.
	texts := []string{
		"-u leading unknown flag",
		"-urgent nudge(s):",
		"-reminder>",
		"--force",
		"-l",
	}
	for _, text := range texts {
		if err := tm.sendKeysLiteral(session, text); err != nil {
			t.Errorf("sendKeysLiteral(%q) = %v, want nil (send-keys parsed the text as flags)", text, err)
		}
	}

	out, err := tm.CapturePaneAll(session)
	if err != nil {
		t.Fatalf("CapturePaneAll: %v", err)
	}
	for _, text := range texts {
		if !strings.Contains(out, text) {
			t.Errorf("pane does not contain %q; captured:\n%s", text, out)
		}
	}
}

// TestSendMessageToTargetChunkBoundaryLeadingDash covers the production shape:
// the text is long enough to be chunked, and a chunk boundary lands exactly
// before a "-". Every chunk after the first goes through the unretried branch
// of sendMessageToTarget, which is where the injection errors came from.
func TestSendMessageToTargetChunkBoundaryLeadingDash(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-sve-chunked"

	if _, err := tm.run("new-session", "-d", "-s", session, "cat"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer func() { _, _ = tm.run("kill-session", "-t", session) }()

	// Pad to exactly one chunk so the next chunk starts on the "-".
	tail := "-urgent nudge(s): tail"
	msg := strings.Repeat("x", sendKeysChunkSize) + tail
	if msg[sendKeysChunkSize] != '-' {
		t.Fatalf("test setup: byte at chunk boundary is %q, want '-'", msg[sendKeysChunkSize])
	}

	if err := tm.sendMessageToTarget(session, msg); err != nil {
		t.Fatalf("sendMessageToTarget: %v", err)
	}

	out, err := tm.CapturePaneAll(session)
	if err != nil {
		t.Fatalf("CapturePaneAll: %v", err)
	}
	if !strings.Contains(strings.ReplaceAll(out, "\n", ""), tail) {
		t.Errorf("pane missing tail %q; captured:\n%s", tail, out)
	}
}

// TestSendMessageToTargetPartialStageIsFlagged checks that a chunked send
// failing after the first chunk reports errPartialStage, which is what tells
// the nudge path a fragment is sitting in the composer and has to be cleared.
// A fragment is not inert: the next Enter from any source submits it.
func TestSendMessageToTargetPartialStageIsFlagged(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-sve-partial"

	if _, err := tm.run("new-session", "-d", "-s", session, "cat"); err != nil {
		t.Fatalf("new-session: %v", err)
	}

	// First chunk lands, then the session is killed so a later chunk fails.
	msg := strings.Repeat("y", sendKeysChunkSize*3)
	if err := tm.sendKeysLiteral(session, msg[:sendKeysChunkSize]); err != nil {
		t.Fatalf("priming send: %v", err)
	}
	if _, err := tm.run("kill-session", "-t", session); err != nil {
		t.Fatalf("kill-session: %v", err)
	}

	err := tm.sendMessageToTarget(session, msg)
	if err == nil {
		t.Fatal("sendMessageToTarget to a dead session: got nil, want error")
	}
	// The first chunk fails here (retry path), so this is the not-partial case:
	// nothing was staged, and clearing would be gratuitous.
	if errors.Is(err, errPartialStage) {
		t.Errorf("first-chunk failure reported as partial stage: %v", err)
	}
}

// TestPartialStageErrorWrapsCause keeps the chunk offset and underlying cause
// in the message — a partial stage is diagnosed from logs, not a debugger.
func TestPartialStageErrorWrapsCause(t *testing.T) {
	err := fmt.Errorf("%w: chunk at byte %d: %v", errPartialStage, 512, errors.New("boom"))
	if !errors.Is(err, errPartialStage) {
		t.Error("errors.Is(err, errPartialStage) = false, want true")
	}
	for _, want := range []string{"512", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
