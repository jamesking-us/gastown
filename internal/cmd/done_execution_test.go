package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/execution"
)

type memoryDoneExecutionStore struct {
	record *execution.Record
}

func (s *memoryDoneExecutionStore) Load(workID string) (*execution.Record, error) {
	if s.record == nil || s.record.WorkID != workID {
		return nil, execution.ErrNotFound
	}
	copy := *s.record
	if s.record.Current != nil {
		attempt := *s.record.Current
		copy.Current = &attempt
	}
	return &copy, nil
}

func (s *memoryDoneExecutionStore) Apply(workID string, command execution.Command) (*execution.Record, error) {
	next, _, err := execution.Apply(s.record, command)
	if err != nil {
		return nil, err
	}
	s.record = next
	return s.Load(workID)
}

func runningDoneRecord() *execution.Record {
	return &execution.Record{
		SchemaVersion: execution.SchemaVersion,
		WorkID:        "work-1",
		State:         execution.StateRunning,
		Revision:      4,
		CreatedAt:     time.Unix(1, 0).UTC(),
		UpdatedAt:     time.Unix(4, 0).UTC(),
		Current: &execution.Attempt{
			ExecutionID: "exec-1", Generation: 1, State: execution.StateRunning,
			AgentID: "rig/polecats/alpha",
		},
		Receipts: make(map[string]execution.Receipt),
	}
}

func TestDoneExecutionHandshakeRecordsCommittingAndSubmitted(t *testing.T) {
	store := &memoryDoneExecutionStore{record: runningDoneRecord()}
	handshake, err := beginDoneExecution(store, "work-1", "rig/polecats/alpha", time.Unix(5, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if store.record.State != execution.StateCommitting {
		t.Fatalf("state=%s, want committing", store.record.State)
	}
	if err := handshake.Submit("mr-1", "abc123", time.Unix(6, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if store.record.State != execution.StateSubmitted {
		t.Fatalf("state=%s, want submitted", store.record.State)
	}
	evidence := store.record.Current.Evidence
	if len(evidence) != 2 || evidence[0].Value != "abc123" || evidence[1].Value != "mr-1" {
		t.Fatalf("evidence=%v", evidence)
	}
	// A retry after durable submission is a no-op.
	retry, err := beginDoneExecution(store, "work-1", "rig/polecats/alpha", time.Unix(7, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.Submit("mr-1", "abc123", time.Unix(8, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if store.record.Revision != 6 {
		t.Fatalf("revision=%d, want 6", store.record.Revision)
	}
}

func TestDoneExecutionHandshakeRejectsWrongOwner(t *testing.T) {
	store := &memoryDoneExecutionStore{record: runningDoneRecord()}
	_, err := beginDoneExecution(store, "work-1", "rig/polecats/beta", time.Now())
	if err == nil {
		t.Fatal("wrong owner was accepted")
	}
	if store.record.State != execution.StateRunning {
		t.Fatalf("state changed to %s", store.record.State)
	}
}

func TestDoneExecutionHandshakeRejectsReplacementGeneration(t *testing.T) {
	store := &memoryDoneExecutionStore{record: runningDoneRecord()}
	handshake, err := beginDoneExecution(store, "work-1", "rig/polecats/alpha", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store.record.Current = &execution.Attempt{
		ExecutionID: "exec-2", Generation: 2, State: execution.StateRunning,
		AgentID: "rig/polecats/beta",
	}
	store.record.State = execution.StateRunning
	if err := handshake.Submit("mr-1", "abc123", time.Now()); !errors.Is(err, execution.ErrFenceMismatch) {
		t.Fatalf("Submit() error=%v, want fence mismatch", err)
	}
}
