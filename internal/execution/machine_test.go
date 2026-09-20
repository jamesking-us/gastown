package execution

import (
	"errors"
	"testing"
	"time"
)

func fixedTime(hour int) time.Time {
	return time.Date(2026, time.September, 20, hour, 0, 0, 0, time.UTC)
}

func readyRecord(t *testing.T) *Record {
	t.Helper()
	record, changed, err := NewRecord(CreateRequest{
		WorkID: "kf-123", Rig: "kingforge", Repository: "cloudcontentmanager",
		IdempotencyKey: "create-1", At: fixedTime(1),
	})
	if err != nil || !changed {
		t.Fatalf("NewRecord() changed=%v err=%v", changed, err)
	}
	return record
}

func claimRecord(t *testing.T, record *Record, key, executionID string) *Record {
	t.Helper()
	next, changed, err := Apply(record, Command{
		Operation: "claim", IdempotencyKey: key, ExecutionID: executionID,
		AgentID: "worker-a", Runtime: "codex", At: fixedTime(2),
	})
	if err != nil || !changed {
		t.Fatalf("claim changed=%v err=%v", changed, err)
	}
	return next
}

func TestLifecycleAndRecoveryGeneration(t *testing.T) {
	record := claimRecord(t, readyRecord(t), "claim-1", "exec-1")
	if record.Current.Generation != 1 || record.State != StateClaimed {
		t.Fatalf("first claim = generation %d state %s", record.Current.Generation, record.State)
	}

	for i, state := range []State{StateStarting, StateRunning, StateCommitting, StateSubmitted} {
		var err error
		record, _, err = Apply(record, Command{
			Operation: "transition", IdempotencyKey: "transition-" + string(rune('a'+i)),
			ExecutionID: "exec-1", Generation: 1, From: record.State, To: state, At: fixedTime(3 + i),
		})
		if err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	if record.State != StateSubmitted {
		t.Fatalf("state = %s, want submitted", record.State)
	}

	// A recoverable attempt is replaced by a new generation; the old attempt
	// remains in history and its fence can no longer modify the record.
	recoverable := claimRecord(t, readyRecord(t), "claim-r1", "exec-old")
	recoverable, _, _ = Apply(recoverable, Command{
		Operation: "transition", IdempotencyKey: "recoverable-1", ExecutionID: "exec-old",
		Generation: 1, From: StateClaimed, To: StateRecoverable, At: fixedTime(4),
	})
	recovered := claimRecord(t, recoverable, "claim-r2", "exec-new")
	if recovered.Current.Generation != 2 || len(recovered.History) != 1 {
		t.Fatalf("recovery generation=%d history=%d", recovered.Current.Generation, len(recovered.History))
	}
	_, _, err := Apply(recovered, Command{
		Operation: "heartbeat", IdempotencyKey: "late-heartbeat", ExecutionID: "exec-old",
		Generation: 1, At: fixedTime(5),
	})
	if !errors.Is(err, ErrFenceMismatch) {
		t.Fatalf("late heartbeat error = %v, want ErrFenceMismatch", err)
	}
}

func TestIdempotencyAndKeyCollision(t *testing.T) {
	record := readyRecord(t)
	command := Command{
		Operation: "claim", IdempotencyKey: "claim-1", ExecutionID: "exec-1",
		AgentID: "worker-a", At: fixedTime(2),
	}
	first, changed, err := Apply(record, command)
	if err != nil || !changed {
		t.Fatalf("first apply changed=%v err=%v", changed, err)
	}
	command.At = fixedTime(8) // transport retry time is intentionally ignored
	retry, changed, err := Apply(first, command)
	if err != nil || changed {
		t.Fatalf("retry changed=%v err=%v", changed, err)
	}
	if retry.Revision != first.Revision {
		t.Fatalf("retry revision=%d, want %d", retry.Revision, first.Revision)
	}

	command.AgentID = "worker-b"
	_, _, err = Apply(first, command)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("key collision error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestInvalidTransitionAndTerminalState(t *testing.T) {
	record := claimRecord(t, readyRecord(t), "claim-1", "exec-1")
	_, _, err := Apply(record, Command{
		Operation: "transition", IdempotencyKey: "skip", ExecutionID: "exec-1",
		Generation: 1, From: StateClaimed, To: StateSubmitted, At: fixedTime(3),
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped transition error = %v, want ErrInvalidTransition", err)
	}

	cancelled, _, err := Apply(record, Command{
		Operation: "transition", IdempotencyKey: "cancel", ExecutionID: "exec-1",
		Generation: 1, From: StateClaimed, To: StateCancelled, At: fixedTime(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Apply(cancelled, Command{
		Operation: "transition", IdempotencyKey: "revive", ExecutionID: "exec-1",
		Generation: 1, From: StateCancelled, To: StateRunning, At: fixedTime(4),
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition error = %v, want ErrInvalidTransition", err)
	}
}

func TestHeartbeatKeepsStateAndUpdatesLease(t *testing.T) {
	record := claimRecord(t, readyRecord(t), "claim-1", "exec-1")
	expires := fixedTime(9)
	next, changed, err := Apply(record, Command{
		Operation: "heartbeat", IdempotencyKey: "heartbeat-1", ExecutionID: "exec-1",
		Generation: 1, At: fixedTime(3), LeaseExpiresAt: &expires,
	})
	if err != nil || !changed {
		t.Fatalf("heartbeat changed=%v err=%v", changed, err)
	}
	if next.State != StateClaimed || next.Current.LastHeartbeat == nil || !next.Current.LeaseExpiresAt.Equal(expires) {
		t.Fatalf("unexpected heartbeat result: %+v", next.Current)
	}
}
