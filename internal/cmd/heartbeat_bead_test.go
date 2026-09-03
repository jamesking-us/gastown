package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/deacon"
)

func TestThrottleBeadHeartbeat(t *testing.T) {
	// The measurement in hq-huln: label frozen at 1788382012, probed 106s and
	// 135s later, both inside the window, so no write was attempted.
	const stored = 1788382012
	now := time.Unix(stored+106, 0)

	tests := []struct {
		name          string
		labels        []string
		now           time.Time
		wantThrottled bool
		wantNext      time.Duration
		wantEpoch     int64
	}{
		{
			name:          "fresh label throttles and reports the wait",
			labels:        []string{"role:deacon", "heartbeat:1788382012"},
			now:           now,
			wantThrottled: true,
			wantNext:      deaconBeadHeartbeatSyncThreshold - 106*time.Second,
			wantEpoch:     stored,
		},
		{
			name:   "label at the threshold refreshes",
			labels: []string{"heartbeat:1788382012"},
			now:    time.Unix(stored, 0).Add(deaconBeadHeartbeatSyncThreshold),
		},
		{
			name:   "stale label refreshes",
			labels: []string{"heartbeat:1788382012"},
			now:    time.Unix(stored+600, 0),
		},
		{
			name:   "no heartbeat label refreshes",
			labels: []string{"role:deacon", "idle:0"},
			now:    now,
		},
		{
			name:   "unparseable heartbeat label is ignored",
			labels: []string{"heartbeat:not-an-epoch"},
			now:    now,
		},
		{
			// The bead has carried duplicate heartbeat labels (hq-cvk6); the
			// freshest is the one watchers act on.
			name:          "duplicate labels use the newest",
			labels:        []string{"heartbeat:1788380000", "heartbeat:1788382012"},
			now:           now,
			wantThrottled: true,
			wantNext:      deaconBeadHeartbeatSyncThreshold - 106*time.Second,
			wantEpoch:     stored,
		},
		{
			name:          "an old duplicate does not defeat the throttle",
			labels:        []string{"heartbeat:1788382012", "heartbeat:1700000000"},
			now:           now,
			wantThrottled: true,
			wantNext:      deaconBeadHeartbeatSyncThreshold - 106*time.Second,
			wantEpoch:     stored,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, throttled := throttleBeadHeartbeat(tt.labels, tt.now)
			if throttled != tt.wantThrottled {
				t.Fatalf("throttled = %v, want %v", throttled, tt.wantThrottled)
			}
			if !throttled {
				return
			}
			if got.NextRefresh != tt.wantNext {
				t.Errorf("NextRefresh = %v, want %v", got.NextRefresh, tt.wantNext)
			}
			if got.Epoch != tt.wantEpoch {
				t.Errorf("Epoch = %d, want %d", got.Epoch, tt.wantEpoch)
			}
			if got.Err != nil {
				t.Errorf("throttling is not a failure, got %v", got.Err)
			}
		})
	}
}

// TestThrottleBeadHeartbeatWindow pins the window itself. The Deacon's five
// probes spanned ~135s and every one was refused; ordinary patrol cycles are
// longer than the window, which is why the label appeared to advance until the
// call rate went up.
func TestThrottleBeadHeartbeatWindow(t *testing.T) {
	if deaconBeadHeartbeatSyncThreshold != deacon.HeartbeatStaleThreshold/2 {
		t.Fatalf("throttle = %v, want half the stale threshold %v",
			deaconBeadHeartbeatSyncThreshold, deacon.HeartbeatStaleThreshold)
	}
	if _, throttled := throttleBeadHeartbeat([]string{"heartbeat:1000000000"}, time.Unix(1000000135, 0)); !throttled {
		t.Error("a 135s-old label should still be throttled")
	}
	if _, throttled := throttleBeadHeartbeat([]string{"heartbeat:1000000000"}, time.Unix(1000000151, 0)); throttled {
		t.Error("a 151s-old label should be refreshable")
	}
}

func TestVerifyBeadHeartbeat(t *testing.T) {
	const wrote = 1788382163

	tests := []struct {
		name    string
		labels  []string
		wantErr string
	}{
		{
			name:   "label present",
			labels: []string{"role:deacon", "heartbeat:1788382163"},
		},
		{
			// await-signal may refresh the same label between our write and
			// our read-back; the store advanced, which is the invariant.
			name:   "a newer label is accepted",
			labels: []string{"heartbeat:1788382200"},
		},
		{
			name:    "label absent",
			labels:  []string{"role:deacon"},
			wantErr: "no heartbeat label",
		},
		{
			name:    "label did not advance",
			labels:  []string{"heartbeat:1788382012"},
			wantErr: "heartbeat label is heartbeat:1788382012",
		},
		{
			name:    "unparseable label does not count as written",
			labels:  []string{"heartbeat:garbage"},
			wantErr: "no heartbeat label",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyBeadHeartbeat(tt.labels, wrote)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestBeadHeartbeatResultSummary(t *testing.T) {
	tests := []struct {
		res  beadHeartbeatResult
		want string
	}{
		{beadHeartbeatResult{Epoch: 1788382163}, "bead label 1788382163"},
		{beadHeartbeatResult{Throttled: true, NextRefresh: 44 * time.Second}, "bead label fresh, next refresh in 44s"},
		{beadHeartbeatResult{Err: errors.New("bd: connection refused")}, "bead label NOT written"},
	}
	for _, tt := range tests {
		if got := tt.res.Summary(); got != tt.want {
			t.Errorf("Summary() = %q, want %q", got, tt.want)
		}
	}
}
