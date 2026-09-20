package execution

import (
	"testing"
	"time"
)

func TestInspectLeaseReportsFactsWithoutChangingState(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		state     State
		expires   *time.Time
		condition LeaseCondition
		seconds   float64
	}{
		{name: "missing", state: StateRunning, condition: LeaseMissing},
		{name: "active", state: StateRunning, expires: timePointer(now.Add(30 * time.Second)), condition: LeaseActive, seconds: 30},
		{name: "expired", state: StateStarting, expires: timePointer(now.Add(-45 * time.Second)), condition: LeaseExpired, seconds: 45},
		{name: "boundary is expired", state: StateCommitting, expires: timePointer(now), condition: LeaseExpired},
		{name: "terminal", state: StateSubmitted, expires: timePointer(now.Add(-time.Hour)), condition: LeaseNotApplicable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := Record{
				WorkID: "work-1", State: test.state,
				Current: &Attempt{ExecutionID: "exec-1", Generation: 3, State: test.state, LeaseExpiresAt: test.expires},
			}
			got := InspectLease(record, now)
			if got.Condition != test.condition {
				t.Fatalf("condition=%s, want %s", got.Condition, test.condition)
			}
			seconds := got.RemainingSeconds
			if test.condition == LeaseExpired {
				seconds = got.OverdueSeconds
			}
			if seconds != test.seconds {
				t.Fatalf("seconds=%v, want %v", seconds, test.seconds)
			}
			if record.State != test.state {
				t.Fatalf("inspection changed state to %s", record.State)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }
