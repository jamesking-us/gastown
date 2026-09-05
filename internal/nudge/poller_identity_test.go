//go:build !windows

package nudge

import (
	"os"
	"strconv"
	"testing"
)

// The recycled-PID half of gt-sve item 3. The zombie tests in
// poller_zombie_test.go cover a PID whose process has died; these cover a PID
// whose process is alive and is somebody else's.
//
// The two arrive by the same route. The 21:25Z container restart replayed the
// low PID range while .runtime/nudge_poller/*.pid still named PIDs from the
// previous boot — 7 of 14 seats had a pidfile whose process was simply gone.
// Any one of those numbers being reissued to an unrelated process is enough
// to pin the seat at "already running" for as long as that process lives.

func TestCommandLineIsPoller(t *testing.T) {
	const session = "gt-witness"
	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "our poller",
			cmdline: "gt nudge-poller gt-witness --interval 10s\n",
			want:    true,
		},
		{
			name:    "poller for a different seat",
			cmdline: "gt nudge-poller cl-refinery --interval 10s\n",
			want:    false,
		},
		{
			name:    "recycled pid running something else",
			cmdline: "/usr/bin/dolt sql-server --port 3307\n",
			want:    false,
		},
		{
			name:    "gt doing something other than polling",
			cmdline: "gt witness patrol\n",
			want:    false,
		},
		{
			// ps told us nothing usable. A false "not ours" costs a duplicate
			// poller on every hiccup, so silence defers to the liveness check.
			name:    "empty output fails open",
			cmdline: "   \n",
			want:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandLineIsPoller(tc.cmdline, session); got != tc.want {
				t.Errorf("commandLineIsPoller(%q, %q) = %v, want %v", tc.cmdline, session, got, tc.want)
			}
		})
	}
}

// TestPollerAliveRejectsLivePidThatIsNotOurs is the end-to-end shape: the PID
// in the pidfile is alive and answers every liveness probe, but it is not a
// poller. Before the identity check this returned "already running" and the
// seat never got one.
func TestPollerAliveRejectsLivePidThatIsNotOurs(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	// This test process: unquestionably alive, unquestionably not a poller.
	if err := os.MkdirAll(pollerPidDir(townRoot), 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}

	if pid, alive := pollerAlive(townRoot, session); alive {
		t.Errorf("pollerAlive = (%d, true) for a live non-poller PID, want not alive", pid)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("pidfile naming a foreign process was not removed; StartPoller would keep reading it")
	}
}
