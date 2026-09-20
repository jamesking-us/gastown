// Package execution provides durable, fenced execution records for work run by
// agents. It deliberately contains mechanism rather than scheduling policy:
// callers decide what to run and when; this package records those decisions and
// rejects stale or invalid updates.
package execution

import "time"

const SchemaVersion = 1

// State is a durable point in the execution lifecycle.
type State string

const (
	StateReady       State = "ready"
	StateClaimed     State = "claimed"
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateCommitting  State = "committing"
	StateSubmitted   State = "submitted"
	StateMerged      State = "merged"
	StateBlocked     State = "blocked"
	StateRecoverable State = "recoverable"
	StateCancelled   State = "cancelled"
)

// Evidence is a reference to an externally verifiable artifact. The execution
// layer stores references and does not interpret their meaning.
type Evidence struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Attempt preserves the identity and outcome of an execution generation.
type Attempt struct {
	ExecutionID    string     `json:"execution_id"`
	Generation     uint64     `json:"generation"`
	State          State      `json:"state"`
	AgentID        string     `json:"agent_id,omitempty"`
	Runtime        string     `json:"runtime,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	LastHeartbeat  *time.Time `json:"last_heartbeat_at,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Evidence       []Evidence `json:"evidence,omitempty"`
}

// Receipt makes a command idempotent and detects accidental key reuse.
type Receipt struct {
	Operation   string `json:"operation"`
	Fingerprint string `json:"fingerprint"`
	Revision    uint64 `json:"revision"`
}

// Record is the current durable view for one work item.
type Record struct {
	SchemaVersion int                `json:"schema_version"`
	WorkID        string             `json:"work_id"`
	Rig           string             `json:"rig,omitempty"`
	Repository    string             `json:"repository,omitempty"`
	Role          string             `json:"role,omitempty"`
	State         State              `json:"state"`
	Revision      uint64             `json:"revision"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	Current       *Attempt           `json:"current_attempt,omitempty"`
	History       []Attempt          `json:"attempt_history,omitempty"`
	Receipts      map[string]Receipt `json:"receipts,omitempty"`
}

// Command identifies one requested state change. IdempotencyKey must be
// unique within a work item. ExecutionID and Generation fence attempt-scoped
// updates so a delayed agent cannot mutate a replacement attempt.
type Command struct {
	Operation      string     `json:"operation"`
	IdempotencyKey string     `json:"idempotency_key"`
	ExecutionID    string     `json:"execution_id,omitempty"`
	Generation     uint64     `json:"generation,omitempty"`
	From           State      `json:"from,omitempty"`
	To             State      `json:"to,omitempty"`
	AgentID        string     `json:"agent_id,omitempty"`
	Runtime        string     `json:"runtime,omitempty"`
	Actor          string     `json:"actor,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	At             time.Time  `json:"at"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	Evidence       []Evidence `json:"evidence,omitempty"`
}

// CreateRequest describes an unclaimed work item.
type CreateRequest struct {
	WorkID         string    `json:"work_id"`
	Rig            string    `json:"rig,omitempty"`
	Repository     string    `json:"repository,omitempty"`
	Role           string    `json:"role,omitempty"`
	IdempotencyKey string    `json:"idempotency_key"`
	Actor          string    `json:"actor,omitempty"`
	At             time.Time `json:"at"`
}

// Event is an immutable journal entry. Record is the complete post-command
// state, allowing replay to repair a missing or stale snapshot after a crash.
type Event struct {
	SchemaVersion  int       `json:"schema_version"`
	Sequence       uint64    `json:"sequence"`
	Timestamp      time.Time `json:"timestamp"`
	WorkID         string    `json:"work_id"`
	Operation      string    `json:"operation"`
	IdempotencyKey string    `json:"idempotency_key"`
	Actor          string    `json:"actor,omitempty"`
	PreviousHash   string    `json:"previous_hash,omitempty"`
	Hash           string    `json:"hash"`
	Record         Record    `json:"record"`
}

// Verification summarizes an integrity check of one work item's journal.
type Verification struct {
	WorkID   string `json:"work_id"`
	Events   uint64 `json:"events"`
	LastHash string `json:"last_hash,omitempty"`
	Revision uint64 `json:"revision"`
}
