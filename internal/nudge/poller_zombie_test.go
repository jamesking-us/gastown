//go:build !windows

package nudge

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// startZombie leaves a child process exited but unreaped, so its PID stays
// allocated in state Z. The test process deliberately never Wait()s it; the
// cleanup reaps it at the end.
func startZombie(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start helper process: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() })

	// Wait for the child to exit and become defunct.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err == nil && len(out) > 0 && out[0] == 'Z' {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Skipf("helper pid %d did not reach zombie state", pid)
	return 0
}

// TestPollerAlive_ZombieIsNotAlive is the gt-sve regression for poller respawn.
//
// A zombie answers a bare kill(pid, 0) probe successfully, because the kernel
// keeps its PID allocated until the parent reaps it. The old check was exactly
// that bare probe, so a pidfile naming a corpse read as "poller running" and
// StartPoller's fast path returned it instead of respawning. Measured twice in
// production: gastown/witness sat 55 minutes with poller PID 3507 in STAT=Z
// while .runtime/nudge_poller/gt-witness.pid still named it, and a second
// witness seat reproduced the same shape.
func TestPollerAlive_ZombieIsNotAlive(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"
	pid := startZombie(t)

	if err := os.MkdirAll(pollerPidDir(townRoot), 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	if _, alive := pollerAlive(townRoot, session); alive {
		t.Errorf("pollerAlive() = true for zombie pid %d; the seat would go without a poller", pid)
	}

	// The pidfile naming the corpse must go, or the next StartPoller reads it
	// again and takes the same fast path.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("pidfile naming a zombie was not cleaned up")
	}
}

// TestStopPoller_Zombie confirms the same correction on the teardown side:
// there is nothing to signal, and the pidfile should not survive the call.
func TestStopPoller_Zombie(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"
	pid := startZombie(t)

	if err := os.MkdirAll(pollerPidDir(townRoot), 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	if err := StopPoller(townRoot, session); err != nil {
		t.Errorf("StopPoller() on zombie pid: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("StopPoller did not clean up the pidfile naming a zombie")
	}
}

// TestPollerAlive_LiveChildProcess is the other half of the pair: the zombie check
// must not report a working poller as dead, which would spawn a duplicate.
func TestPollerAlive_LiveChildProcess(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	livePid := startFakePoller(t, session)
	writePollerPid(t, townRoot, session, livePid)

	pid, alive := pollerAlive(townRoot, session)
	if !alive {
		t.Fatal("pollerAlive() = false for a live process")
	}
	if pid != livePid {
		t.Errorf("pollerAlive() pid = %d, want %d", pid, livePid)
	}
}
