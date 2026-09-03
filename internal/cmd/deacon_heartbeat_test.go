package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/deacon"
)

// stubBeadHeartbeatSync replaces the agent-bead heartbeat store for one test.
func stubBeadHeartbeatSync(t *testing.T, res beadHeartbeatResult) *int {
	t.Helper()
	calls := 0
	old := deaconAgentBeadHeartbeatSync
	deaconAgentBeadHeartbeatSync = func(string) beadHeartbeatResult {
		calls++
		return res
	}
	t.Cleanup(func() { deaconAgentBeadHeartbeatSync = old })
	return &calls
}

// TestDeaconHeartbeat_FailsWhenBeadStoreFails is the hq-huln regression: the
// bead label store failed against bd, the heartbeat file store succeeded, and
// the command printed "✓ Heartbeat updated" anyway. The Witness reads the bead
// label, so that success message described a liveness signal that had stopped
// advancing.
func TestDeaconHeartbeat_FailsWhenBeadStoreFails(t *testing.T) {
	townRoot := t.TempDir()
	stubBeadHeartbeatSync(t, beadHeartbeatResult{Err: errors.New("bd: connection refused")})

	var out bytes.Buffer
	err := deaconHeartbeat(townRoot, "patrol cycle", &out)

	if err == nil {
		t.Fatal("expected an error when the agent-bead heartbeat store fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should carry bd's own diagnosis, got %v", err)
	}
	if strings.Contains(out.String(), "✓") {
		t.Errorf("printed success while the bead label was not written: %q", out.String())
	}
	if !strings.Contains(out.String(), "bead label NOT written") {
		t.Errorf("output should name the store that failed, got %q", out.String())
	}
	// The file store still wrote; only the bead store failed.
	if hb := deacon.ReadHeartbeat(townRoot); hb == nil {
		t.Error("expected the heartbeat file store to still be written")
	}
}

// TestDeaconHeartbeat_ReportsThrottledBeadLabel covers the other half of the
// silence: the refresh throttle is by design, but skipping the write must not
// read as having performed one.
func TestDeaconHeartbeat_ReportsThrottledBeadLabel(t *testing.T) {
	townRoot := t.TempDir()
	stubBeadHeartbeatSync(t, beadHeartbeatResult{
		Throttled:   true,
		NextRefresh: 44 * time.Second,
		Epoch:       1788382012,
	})

	var out bytes.Buffer
	if err := deaconHeartbeat(townRoot, "", &out); err != nil {
		t.Fatalf("a throttled label is fresh, not a failure: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "✓") {
		t.Errorf("expected success, got %q", got)
	}
	if !strings.Contains(got, "next refresh in 44s") {
		t.Errorf("output should say the refresh was throttled and for how long, got %q", got)
	}
}

func TestDeaconHeartbeat_NamesTheEpochItWrote(t *testing.T) {
	townRoot := t.TempDir()
	calls := stubBeadHeartbeatSync(t, beadHeartbeatResult{Epoch: 1788382163})

	var out bytes.Buffer
	if err := deaconHeartbeat(townRoot, "all clear", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *calls != 1 {
		t.Errorf("agent bead syncs = %d, want 1", *calls)
	}
	got := out.String()
	for _, want := range []string{"✓", "all clear", "file", "bead label 1788382163"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}
}

func TestDeaconHeartbeat_PausedWritesNothing(t *testing.T) {
	townRoot := t.TempDir()
	calls := stubBeadHeartbeatSync(t, beadHeartbeatResult{Epoch: 1})
	if err := deacon.Pause(townRoot, "maintenance", "test"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := deaconHeartbeat(townRoot, "", &out); err == nil {
		t.Fatal("expected an error while paused")
	}
	if *calls != 0 {
		t.Errorf("agent bead syncs = %d, want 0 while paused", *calls)
	}
	if hb := deacon.ReadHeartbeat(townRoot); hb != nil {
		t.Errorf("expected no heartbeat file while paused, got %+v", hb)
	}
}

func TestDeaconHeartbeatResult_ErrAndSummary(t *testing.T) {
	fileErr := errors.New("disk full")
	beadErr := errors.New("bd timeout")

	tests := []struct {
		name        string
		res         deaconHeartbeatResult
		wantErr     bool
		wantSummary string
	}{
		{
			name:        "both stores wrote",
			res:         deaconHeartbeatResult{Bead: beadHeartbeatResult{Epoch: 7}},
			wantSummary: "file, bead label 7",
		},
		{
			name:        "bead throttled is not a failure",
			res:         deaconHeartbeatResult{Bead: beadHeartbeatResult{Throttled: true, NextRefresh: 90 * time.Second}},
			wantSummary: "file, bead label fresh, next refresh in 1m30s",
		},
		{
			name:        "bead store failed",
			res:         deaconHeartbeatResult{Bead: beadHeartbeatResult{Err: beadErr}},
			wantErr:     true,
			wantSummary: "file, bead label NOT written",
		},
		{
			name:        "file store failed",
			res:         deaconHeartbeatResult{FileErr: fileErr, Bead: beadHeartbeatResult{Epoch: 7}},
			wantErr:     true,
			wantSummary: "file NOT written, bead label 7",
		},
		{
			name:        "both stores failed",
			res:         deaconHeartbeatResult{FileErr: fileErr, Bead: beadHeartbeatResult{Err: beadErr}},
			wantErr:     true,
			wantSummary: "file NOT written, bead label NOT written",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotErr := tt.res.Err(); (gotErr != nil) != tt.wantErr {
				t.Errorf("Err() = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if got := tt.res.Summary(); got != tt.wantSummary {
				t.Errorf("Summary() = %q, want %q", got, tt.wantSummary)
			}
		})
	}
}

func TestDeaconHeartbeatResult_ErrUnwrapsBothStores(t *testing.T) {
	fileErr := errors.New("disk full")
	beadErr := errors.New("bd timeout")

	if err := (deaconHeartbeatResult{FileErr: fileErr}).Err(); !errors.Is(err, fileErr) {
		t.Errorf("file error not unwrappable: %v", err)
	}
	if err := (deaconHeartbeatResult{Bead: beadHeartbeatResult{Err: beadErr}}).Err(); !errors.Is(err, beadErr) {
		t.Errorf("bead error not unwrappable: %v", err)
	}
	both := (deaconHeartbeatResult{FileErr: fileErr, Bead: beadHeartbeatResult{Err: beadErr}}).Err()
	if !strings.Contains(both.Error(), "disk full") || !strings.Contains(both.Error(), "bd timeout") {
		t.Errorf("combined error should name both stores, got %v", both)
	}
}
