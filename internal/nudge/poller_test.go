package nudge

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
)

func TestPollerPidFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-bear"

	pidFile := pollerPidFile(townRoot, session)
	expected := filepath.Join(townRoot, ".runtime", "nudge_poller", session+".pid")
	if pidFile != expected {
		t.Errorf("pollerPidFile() = %q, want %q", pidFile, expected)
	}
}

func TestPollerPidFile_SlashSanitized(t *testing.T) {
	townRoot := t.TempDir()
	session := "some/session"

	pidFile := pollerPidFile(townRoot, session)
	// Slashes should be replaced with underscores
	expected := filepath.Join(townRoot, ".runtime", "nudge_poller", "some_session.pid")
	if pidFile != expected {
		t.Errorf("pollerPidFile() = %q, want %q", pidFile, expected)
	}
}

func TestPollerAlive_NoPidFile(t *testing.T) {
	townRoot := t.TempDir()
	_, alive := pollerAlive(townRoot, "nonexistent-session")
	if alive {
		t.Error("pollerAlive() returned true for nonexistent PID file")
	}
}

func TestPollerAlive_StalePid(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write a PID file with an invalid PID (process doesn't exist).
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	// Use a very high PID that's almost certainly not running.
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	_, alive := pollerAlive(townRoot, session)
	if alive {
		t.Error("pollerAlive() returned true for dead PID")
	}

	// Stale PID file should be cleaned up.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("stale PID file was not cleaned up")
	}
}

func TestPollerAlive_CorruptPidFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0644); err != nil {
		t.Fatal(err)
	}

	_, alive := pollerAlive(townRoot, session)
	if alive {
		t.Error("pollerAlive() returned true for corrupt PID file")
	}
}

func TestStopPoller_NoPidFile(t *testing.T) {
	townRoot := t.TempDir()
	// Should be a no-op, no error.
	if err := StopPoller(townRoot, "nonexistent"); err != nil {
		t.Errorf("StopPoller() unexpected error: %v", err)
	}
}

func TestStopPoller_StalePid(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write a stale PID file.
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should succeed and clean up the stale PID file.
	if err := StopPoller(townRoot, session); err != nil {
		t.Errorf("StopPoller() unexpected error: %v", err)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("StopPoller did not clean up stale PID file")
	}
}

// startFakePoller launches a live helper process whose command line carries
// the poller signature, and returns its PID.
//
// A bare `sleep` is no longer enough. pollerAlive asks two questions — is the
// PID running, and is the process behind it this session's poller — because
// liveness alone cannot tell a working poller from a PID that was recycled to
// something unrelated (gt-sve item 3).
//
// The helper is a script named `gt` invoked as `gt nudge-poller <session>`,
// which is the shape buildPollerCommand produces. Hiding the signature in a
// `sh -c` string would be shorter and is not reliable: a shell is free to exec
// away a single simple command, and the replacement process does not inherit
// the -c text. Real arguments survive that; a comment does not.
func startFakePoller(t *testing.T, session string) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("helper relies on a shell script and ps")
	}

	script := filepath.Join(t.TempDir(), "gt")
	// Two statements, so a shell cannot exec the sleep in place of itself.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n:\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(script, "nudge-poller", session)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// The helper is scaffolding, not the thing under test. If this platform's
	// ps or shell does not preserve the argument vector, say so rather than
	// reporting it as a pollerAlive failure.
	if !pollerProcessMatches(cmd.Process.Pid, session) {
		t.Skipf("helper pid %d does not present a poller command line on this platform", cmd.Process.Pid)
	}
	return cmd.Process.Pid
}

// writePollerPid points a session's pidfile at pid.
func writePollerPid(t *testing.T, townRoot, session string, pid int) string {
	t.Helper()
	if err := os.MkdirAll(pollerPidDir(townRoot), 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}
	return pidPath
}

func TestPollerAlive_LiveProcess(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	livePid := startFakePoller(t, session)
	writePollerPid(t, townRoot, session, livePid)

	pid, alive := pollerAlive(townRoot, session)
	if !alive {
		t.Error("pollerAlive() returned false for live process")
	}
	if pid != livePid {
		t.Errorf("pollerAlive() pid = %d, want %d", pid, livePid)
	}
}

func TestBuildPollerCommand_UsesDetachedProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group management is not supported on Windows")
	}
	townRoot := t.TempDir()
	cmd := buildPollerCommand("/tmp/fake-gt", townRoot, "gt-gastown-crew-bear")

	if got, want := cmd.Dir, townRoot; got != want {
		t.Fatalf("cmd.Dir = %q, want %q", got, want)
	}
	if got, want := cmd.Path, "/tmp/fake-gt"; got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "nudge-poller" || cmd.Args[2] != "gt-gastown-crew-bear" {
		t.Fatalf("cmd.Args = %#v, want poller invocation", cmd.Args)
	}
	if cmd.Cancel != nil {
		t.Fatal("buildPollerCommand() installed cmd.Cancel; detached pollers must leave it nil")
	}
	if cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("buildPollerCommand() should discard stdout/stderr")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("buildPollerCommand() did not configure SysProcAttr")
	}
}

func TestSetProcessGroup_InstallsCancelHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SetProcessGroup is a no-op on Windows")
	}
	cmd := exec.Command("true")
	util.SetProcessGroup(cmd)

	if cmd.Cancel == nil {
		t.Fatal("SetProcessGroup() should install a cancel hook")
	}
}
