package execution

import "time"

type LeaseCondition string

const (
	LeaseActive        LeaseCondition = "active"
	LeaseExpired       LeaseCondition = "expired"
	LeaseMissing       LeaseCondition = "missing"
	LeaseNotApplicable LeaseCondition = "not_applicable"
)

// LeaseInspection reports clock arithmetic only. It does not decide whether an
// expired attempt is dead, recoverable, or eligible for replacement.
type LeaseInspection struct {
	WorkID           string         `json:"work_id"`
	Rig              string         `json:"rig,omitempty"`
	ExecutionID      string         `json:"execution_id,omitempty"`
	Generation       uint64         `json:"generation,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
	State            State          `json:"state"`
	Condition        LeaseCondition `json:"condition"`
	LeaseExpiresAt   *time.Time     `json:"lease_expires_at,omitempty"`
	InspectedAt      time.Time      `json:"inspected_at"`
	OverdueSeconds   float64        `json:"overdue_seconds,omitempty"`
	RemainingSeconds float64        `json:"remaining_seconds,omitempty"`
}

func InspectLease(record Record, at time.Time) LeaseInspection {
	at = normalizeTime(at)
	inspection := LeaseInspection{
		WorkID: record.WorkID, Rig: record.Rig, State: record.State,
		Condition: LeaseNotApplicable, InspectedAt: at,
	}
	if record.Current != nil {
		inspection.ExecutionID = record.Current.ExecutionID
		inspection.Generation = record.Current.Generation
		inspection.AgentID = record.Current.AgentID
		inspection.LeaseExpiresAt = cloneTime(record.Current.LeaseExpiresAt)
	}
	if !leaseBearingState(record.State) {
		return inspection
	}
	if record.Current == nil || record.Current.LeaseExpiresAt == nil {
		inspection.Condition = LeaseMissing
		return inspection
	}
	delta := record.Current.LeaseExpiresAt.Sub(at).Seconds()
	if delta > 0 {
		inspection.Condition = LeaseActive
		inspection.RemainingSeconds = delta
	} else {
		inspection.Condition = LeaseExpired
		inspection.OverdueSeconds = -delta
	}
	return inspection
}

func leaseBearingState(state State) bool {
	switch state {
	case StateClaimed, StateStarting, StateRunning, StateCommitting:
		return true
	default:
		return false
	}
}
