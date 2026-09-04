//go:build !windows

package procutil

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsAlive_CurrentProcess(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Error("IsAlive(os.Getpid()) = false, want true for the running test process")
	}
}

func TestIsAlive_InvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1, -12345} {
		if IsAlive(pid) {
			t.Errorf("IsAlive(%d) = true, want false", pid)
		}
	}
}

func TestIsAlive_ExitedProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running `true`: %v", err)
	}
	pid := cmd.Process.Pid
	// cmd.Run() waits and reaps, so the PID is fully gone (modulo reuse,
	// which a same-process re-check moments later will not hit in practice).
	if IsAlive(pid) {
		t.Errorf("IsAlive(%d) = true for a reaped, exited process, want false", pid)
	}
}

// TestIsAlive_Zombie reproduces the false-positive that motivated this
// package (cl-d77p / boot's hq-deacon nudge-poller cross-reference,
// hq-d16k): a child that has exited but not yet been reaped by its parent
// still answers Signal(0) successfully. IsAlive must additionally consult
// `ps` state and report the zombie as not alive.
func TestIsAlive_Zombie(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting `true`: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Wait() // reap, regardless of how the test ends

	// Poll until the kernel reports zombie state, rather than sleeping a
	// fixed guess.
	deadline := time.Now().Add(5 * time.Second)
	var stat string
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err == nil {
			stat = strings.TrimSpace(string(out))
			if strings.HasPrefix(stat, "Z") {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(stat, "Z") {
		t.Skipf("process %d never reached zombie state (last ps stat=%q); skipping", pid, stat)
	}

	if IsAlive(pid) {
		t.Errorf("IsAlive(%d) = true for a zombie process, want false (Signal(0) succeeds against zombies)", pid)
	}
}

func TestParseZombieStat(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"Z\n", true},
		{"Z+\n", true},
		{"S+\n", false},
		{"R\n", false},
		{"Ss\n", false},
		{"", false},
		{"  Z  \n", true},
	}
	for _, c := range cases {
		if got := parseZombieStat([]byte(c.out)); got != c.want {
			t.Errorf("parseZombieStat(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

func TestFilterPgrepOutput_ExcludesSelfAndParent(t *testing.T) {
	out := []byte("100\n200\n300\n")
	alwaysAlive := func(int) bool { return true }

	got := filterPgrepOutput(out, 200, 300, alwaysAlive)
	want := []int{100}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("filterPgrepOutput excluding self=200 parent=300 = %v, want %v", got, want)
	}
}

func TestFilterPgrepOutput_ExcludesZombies(t *testing.T) {
	out := []byte("100\n200\n300\n")
	deadPIDs := map[int]bool{200: true} // simulate 200 as a zombie
	alive := func(pid int) bool { return !deadPIDs[pid] }

	got := filterPgrepOutput(out, -1, -1, alive)
	for _, pid := range got {
		if pid == 200 {
			t.Errorf("filterPgrepOutput(%v) kept zombie pid 200, want it excluded", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("filterPgrepOutput(%v) = %d entries, want 2", got, len(got))
	}
}

func TestFilterPgrepOutput_IgnoresMalformedLines(t *testing.T) {
	out := []byte("100\nnot-a-pid\n\n300\n")
	got := filterPgrepOutput(out, -1, -1, func(int) bool { return true })
	if len(got) != 2 || got[0] != 100 || got[1] != 300 {
		t.Errorf("filterPgrepOutput(%v) = %v, want [100 300]", string(out), got)
	}
}

// TestFindByPattern_FindsRealMatch is an end-to-end check that FindByPattern
// locates a genuinely running, distinctively-named process and excludes the
// calling test binary (which does not itself match the marker).
func TestFindByPattern_FindsRealMatch(t *testing.T) {
	marker := "procutil-findbypattern-marker-" + strconv.Itoa(os.Getpid())
	// Route through a shell so the marker sits in the process's own visible
	// command line (`pgrep -f` matches on that) without being parsed as a
	// `sleep` argument.
	cmd := exec.Command("sh", "-c", "sleep 5 # "+marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting marked sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		pids, err = FindByPattern(marker)
		if err != nil {
			t.Fatalf("FindByPattern: %v", err)
		}
		if len(pids) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(pids) != 1 || pids[0] != cmd.Process.Pid {
		t.Errorf("FindByPattern(%q) = %v, want [%d]", marker, pids, cmd.Process.Pid)
	}
}

func TestFindByPattern_NoMatches(t *testing.T) {
	pids, err := FindByPattern("procutil-pattern-that-should-never-match-anything-real-xyz")
	if err != nil {
		t.Fatalf("FindByPattern: %v", err)
	}
	if len(pids) != 0 {
		t.Errorf("FindByPattern(no-match) = %v, want empty", pids)
	}
}
