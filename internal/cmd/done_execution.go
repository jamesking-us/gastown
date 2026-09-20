package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/execution"
)

const executionHandshakeEnv = "GT_EXECUTION_HANDSHAKE_ENABLED"

type doneExecutionStore interface {
	Load(workID string) (*execution.Record, error)
	Apply(workID string, command execution.Command) (*execution.Record, error)
}

type doneExecutionHandshake struct {
	store       doneExecutionStore
	workID      string
	executionID string
	generation  uint64
	actor       string
}

func executionHandshakeEnabled() bool {
	value, err := strconv.ParseBool(os.Getenv(executionHandshakeEnv))
	return err == nil && value
}

// beginDoneExecution moves a managed execution into committing before gt done
// performs any push or merge-request mutation. A retry may resume from
// committing or submitted; terminal and unrelated states fail closed.
func beginDoneExecution(store doneExecutionStore, workID, actor string, at time.Time) (*doneExecutionHandshake, error) {
	if strings.TrimSpace(workID) == "" {
		return nil, fmt.Errorf("execution handshake requires a work id")
	}
	record, err := store.Load(workID)
	if err != nil {
		return nil, fmt.Errorf("loading execution record for %s: %w", workID, err)
	}
	if record.Current == nil {
		return nil, fmt.Errorf("execution record %s has no active attempt", workID)
	}
	if actor != "" && record.Current.AgentID != "" && actor != record.Current.AgentID {
		return nil, fmt.Errorf("execution owner mismatch for %s: record=%q caller=%q", workID, record.Current.AgentID, actor)
	}
	handshake := &doneExecutionHandshake{
		store: store, workID: workID, executionID: record.Current.ExecutionID,
		generation: record.Current.Generation, actor: actor,
	}
	switch record.State {
	case execution.StateRunning:
		_, err = store.Apply(workID, execution.Command{
			Operation: "transition", IdempotencyKey: handshake.key("committing"),
			ExecutionID: handshake.executionID, Generation: handshake.generation,
			From: execution.StateRunning, To: execution.StateCommitting,
			Actor: actor, Reason: "gt done completion handshake started", At: at,
		})
		if err != nil {
			return nil, fmt.Errorf("recording committing state: %w", err)
		}
	case execution.StateCommitting, execution.StateSubmitted, execution.StateMerged:
		// An interrupted or repeated gt done resumes from the durable state.
	default:
		return nil, fmt.Errorf("execution %s cannot enter completion handshake from %s", workID, record.State)
	}
	return handshake, nil
}

// Submit records the verified handoff before gt done clears the hook or retires
// the session. It is safe to repeat after an interrupted invocation.
func (h *doneExecutionHandshake) Submit(mrID, commit string, at time.Time) error {
	record, err := h.store.Load(h.workID)
	if err != nil {
		return fmt.Errorf("reloading execution record: %w", err)
	}
	if record.Current == nil || record.Current.ExecutionID != h.executionID || record.Current.Generation != h.generation {
		return execution.ErrFenceMismatch
	}
	switch record.State {
	case execution.StateSubmitted, execution.StateMerged:
		return nil
	case execution.StateCommitting:
	default:
		return fmt.Errorf("execution %s cannot record submission from %s", h.workID, record.State)
	}
	evidence := make([]execution.Evidence, 0, 2)
	if commit != "" {
		evidence = append(evidence, execution.Evidence{Kind: "commit", Value: commit})
	}
	if mrID != "" {
		evidence = append(evidence, execution.Evidence{Kind: "merge_request", Value: mrID})
	}
	_, err = h.store.Apply(h.workID, execution.Command{
		Operation: "transition", IdempotencyKey: h.key("submitted"),
		ExecutionID: h.executionID, Generation: h.generation,
		From: execution.StateCommitting, To: execution.StateSubmitted,
		Actor: h.actor, Reason: "gt done durable handoff verified", Evidence: evidence, At: at,
	})
	if err != nil {
		return fmt.Errorf("recording submitted state: %w", err)
	}
	return nil
}

func (h *doneExecutionHandshake) key(stage string) string {
	return fmt.Sprintf("gt-done:%s:%d:%s", h.executionID, h.generation, stage)
}
