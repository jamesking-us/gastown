// Package testsink is the production-side half of the test-isolation layer.
//
// internal/testenv establishes isolation from a TestMain; this package is what
// the code under test consults to find out that it is isolated. They are two
// packages rather than one because testenv imports "testing", and linking
// "testing" into the gt binary is not acceptable — so the sentinel names are
// defined here, testenv sets them, and both sides are pinned together by
// TestSentinelNamesMatchTestenv.
//
// It exists because of gt-8f3, the third escape in the cl-69h family. cl-69h
// and cl-qaj3 isolated HOME and the Dolt endpoint. Neither isolated the NUDGE
// transport, so `go test ./internal/cmd/` delivered real nudges to real agents
// of the live town — hq-mayor, cl-witness, cl-refinery, the deacon and one of
// its dogs all received the literal string "test" on 2026-09-03. Delivery into
// a pane holding staged unsubmitted text appends and submits it (cl-jkr), so a
// test binary was doing, on every run, the precise act a standing order forbids
// the town's own infrastructure agents from doing deliberately.
//
// Keep this package dependency-free apart from the standard library. Every
// package that can deliver a nudge has to be able to import it — internal/tmux
// and internal/nudge included, which are close to the bottom of the graph.
package testsink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvIsolated is set to "1" by testenv.IsolateProcessEnv and by nothing else.
// It answers "is this process a test process?", which is the question the
// transport guards actually need to ask.
//
// It is deliberately separate from EnvNudgeLog. A test that wants to exercise
// the un-sunk delivery path clears the sink path (internal/cmd's
// TestNudgeRefineryNoOpWithoutLog does exactly that) and must still not be able
// to reach a live pane; keying the guards on the sink path alone would hand
// that test the live transport back.
const EnvIsolated = "GT_TEST_ISOLATED"

// EnvNudgeLog names the file that intercepted nudges are appended to. It
// predates this package as an opt-in hook that individual tests set with
// t.Setenv, and it keeps that name and its line format so those tests keep
// working; what changed is that isolation now sets it by default, so a test
// that never heard of it is covered too.
const EnvNudgeLog = "GT_TEST_NUDGE_LOG"

// EnvTmuxSockets lists, comma-separated, the tmux sockets this test process
// stood up itself and may therefore deliver to.
//
// The nudge guard cannot simply refuse every tmux delivery under test: the
// tmux package's own tests deliver a nudge into a pane on a server they created
// and then read the pane back, and those are the tests that prove the delivery
// protocol still works. Silencing them would satisfy "no nudge escaped" by no
// longer having a nudge path under test at all — the failure gt-8f3's
// acceptance criteria call out by name.
//
// So ownership is declared rather than guessed. A package that starts a tmux
// server registers its socket with testenv.AllowTmuxSocket; everything else —
// including the town socket that internal/session resolves from a live town
// root found by walking up from the working directory, which is how the tests
// reached hq-mayor's pane — is refused.
const EnvTmuxSockets = "GT_TEST_TMUX_SOCKETS"

// Active reports whether this process is an isolated test process, and
// therefore must not touch a live transport.
//
// Production binaries never see EnvIsolated, so every guard in this package
// compiles down to one getenv on the real delivery paths.
func Active() bool {
	return os.Getenv(EnvIsolated) == "1"
}

// NudgeLogPath returns the sink file, or "" when the process has none.
func NudgeLogPath() string {
	return os.Getenv(EnvNudgeLog)
}

// InterceptNudge reports whether the caller must abandon a nudge delivery
// because this is a test process, recording what would have been delivered
// first so a test can assert on it.
//
// Recording is what keeps the guard honest: an isolated transport that dropped
// nudges silently would let the delivery path rot without failing anything.
// Tests assert against the sink, so breaking the path they exercise still
// breaks them.
//
//	if testsink.InterceptNudge(session, sender, message) {
//	    return nil
//	}
func InterceptNudge(target, sender, message string) bool {
	if !Active() {
		return false
	}
	RecordNudge(target, sender, message)
	return true
}

// InterceptTmuxNudge is InterceptNudge for the tmux transport, which has one
// legitimate exception: a delivery to a socket the test process created.
//
// An empty socket is the ambient tmux server, which is never test-owned.
func InterceptTmuxNudge(socket, target, message string) bool {
	if !Active() {
		return false
	}
	if TmuxSocketAllowed(socket) {
		return false
	}
	RecordNudge(target, "tmux", message)
	return true
}

// TmuxSocketAllowed reports whether a socket was declared test-owned.
func TmuxSocketAllowed(socket string) bool {
	if socket == "" {
		return false
	}
	for _, allowed := range strings.Split(os.Getenv(EnvTmuxSockets), ",") {
		if allowed != "" && allowed == socket {
			return true
		}
	}
	return false
}

// RecordNudge appends one intercepted nudge to the sink file. It is a no-op
// when no sink is configured, and it swallows write errors: the sink is test
// observability, and failing a delivery because the observability file could
// not be opened would be a worse outcome than a thin log.
//
// The line format is the one the pre-existing GT_TEST_NUDGE_LOG hook used:
//
//	nudge:<target>:<sender>:<message>
//
// with newlines in the message escaped, so one delivery stays one line.
func RecordNudge(target, sender, message string) {
	path := NudgeLogPath()
	if path == "" {
		return
	}
	entry := fmt.Sprintf("nudge:%s:%s:%s\n", target, sender, escape(message))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(entry)
	_ = f.Close()
}

func escape(message string) string {
	return strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", " "), "\n", " ")
}

// BlocksTownWrite reports whether a test process is about to write agent-facing
// state into a town that is not its own — the nudge queue under
// <townRoot>/.runtime, say, which gt-8f3 requires to gain zero new entries
// during a test run.
//
// Isolation cannot answer this by environment alone. It strips GT_TOWN_ROOT and
// GT_ROOT (cl-qaj3), but workspace.FindFromCwd walks UP from the working
// directory, and a test's working directory is its package inside the gastown
// checkout — which on a Gas Town host sits inside the operator's real town. So
// the lookup that is supposed to return "no town here" returns the live one.
//
// The distinguishing property available at the write is the path: a town a test
// legitimately owns was made by t.TempDir or lives under the isolated HOME,
// both of which are temporary directories this process created. A town it does
// not own — /gt — is neither. Judging the destination rather than the
// environment is also what keeps internal/nudge's own queue tests working:
// they build a real queue under t.TempDir and read it back, and must keep
// doing so, or isolating the transport would have quietly stopped exercising it.
//
// It reports false outside a test process, so production writes are untouched.
func BlocksTownWrite(townRoot string) bool {
	if !Active() || townRoot == "" {
		return false
	}
	return !isTestOwned(townRoot)
}

// isTestOwned reports whether a path lies under a directory this test process
// owns: the OS temp directory (t.TempDir's parent) or the isolated HOME.
//
// Unresolvable paths count as NOT owned. The guard's job is to refuse a write
// it cannot prove is safe, so "I could not tell" has to fall on the refusing
// side — the failure it prevents is a nudge landing on the mayor's pane.
func isTestOwned(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = resolve(abs)

	for _, root := range []string{os.TempDir(), os.Getenv("HOME")} {
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if within(resolve(rootAbs), abs) {
			return true
		}
	}
	return false
}

// resolve follows symlinks where it can. macOS resolves /tmp to /private/tmp,
// so comparing os.TempDir() against a t.TempDir() path without this reports a
// test-owned directory as foreign.
func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// within reports whether path is root or lies beneath it, comparing whole path
// elements so "/tmproot" does not count as being inside "/tmp".
func within(root, path string) bool {
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
