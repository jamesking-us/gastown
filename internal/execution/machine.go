package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound            = errors.New("execution record not found")
	ErrAlreadyExists       = errors.New("execution record already exists")
	ErrInvalidTransition   = errors.New("invalid execution transition")
	ErrFenceMismatch       = errors.New("execution fence mismatch")
	ErrIdempotencyConflict = errors.New("idempotency key reused for a different command")
)

var transitions = map[State]map[State]bool{
	StateReady:       {StateClaimed: true, StateCancelled: true},
	StateClaimed:     {StateStarting: true, StateRecoverable: true, StateCancelled: true},
	StateStarting:    {StateRunning: true, StateRecoverable: true, StateBlocked: true, StateCancelled: true},
	StateRunning:     {StateCommitting: true, StateRecoverable: true, StateBlocked: true, StateCancelled: true},
	StateCommitting:  {StateSubmitted: true, StateRecoverable: true, StateBlocked: true},
	StateSubmitted:   {StateMerged: true, StateBlocked: true},
	StateRecoverable: {StateClaimed: true, StateCancelled: true},
}

func normalizeTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func validateID(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func fingerprint(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func commandFingerprint(command Command) (string, error) {
	// Time records when a command was first accepted. It is not part of the
	// command's semantic identity, so a transport retry may supply a new clock
	// value while retaining the same idempotency key.
	command.At = time.Time{}
	return fingerprint(command)
}

func createFingerprint(req CreateRequest) (string, error) {
	req.At = time.Time{}
	return fingerprint(req)
}

func receiptMatches(record *Record, key, operation, fp string) (bool, error) {
	if err := validateID("idempotency key", key); err != nil {
		return false, err
	}
	receipt, ok := record.Receipts[key]
	if !ok {
		return false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fp {
		return false, fmt.Errorf("%w: %q", ErrIdempotencyConflict, key)
	}
	return true, nil
}

func storeReceipt(record *Record, key, operation, fp string) {
	if record.Receipts == nil {
		record.Receipts = make(map[string]Receipt)
	}
	record.Receipts[key] = Receipt{Operation: operation, Fingerprint: fp, Revision: record.Revision}
}

// NewRecord creates the ready state. It performs no persistence.
func NewRecord(req CreateRequest) (*Record, bool, error) {
	if err := validateID("work id", req.WorkID); err != nil {
		return nil, false, err
	}
	if err := validateID("idempotency key", req.IdempotencyKey); err != nil {
		return nil, false, err
	}
	req.At = normalizeTime(req.At)
	fp, err := createFingerprint(req)
	if err != nil {
		return nil, false, err
	}
	record := &Record{
		SchemaVersion: SchemaVersion,
		WorkID:        req.WorkID,
		Rig:           req.Rig,
		Repository:    req.Repository,
		Role:          req.Role,
		State:         StateReady,
		Revision:      1,
		CreatedAt:     req.At,
		UpdatedAt:     req.At,
		Receipts:      make(map[string]Receipt),
	}
	storeReceipt(record, req.IdempotencyKey, "create", fp)
	return record, true, nil
}

// Apply applies one command to a copy of record. The bool reports whether the
// command changed state; false means an identical idempotent retry.
func Apply(record *Record, command Command) (*Record, bool, error) {
	if record == nil {
		return nil, false, ErrNotFound
	}
	if err := validateID("idempotency key", command.IdempotencyKey); err != nil {
		return nil, false, err
	}
	command.Operation = strings.ToLower(strings.TrimSpace(command.Operation))
	command.At = normalizeTime(command.At)
	fp, err := commandFingerprint(command)
	if err != nil {
		return nil, false, err
	}
	if ok, err := receiptMatches(record, command.IdempotencyKey, command.Operation, fp); err != nil {
		return nil, false, err
	} else if ok {
		return cloneRecord(record), false, nil
	}

	next := cloneRecord(record)
	switch command.Operation {
	case "claim":
		if next.State != StateReady && next.State != StateRecoverable {
			return nil, false, transitionError(next.State, StateClaimed)
		}
		if err := validateID("execution id", command.ExecutionID); err != nil {
			return nil, false, err
		}
		if executionIDUsed(next, command.ExecutionID) {
			return nil, false, fmt.Errorf("execution id %q was already used for this work item", command.ExecutionID)
		}
		if next.Current != nil {
			next.History = append(next.History, *next.Current)
		}
		generation := uint64(1)
		if next.Current != nil {
			generation = next.Current.Generation + 1
		}
		if command.Generation != 0 && command.Generation != generation {
			return nil, false, fmt.Errorf("%w: expected generation %d, got %d", ErrFenceMismatch, generation, command.Generation)
		}
		next.Current = &Attempt{
			ExecutionID:    command.ExecutionID,
			Generation:     generation,
			State:          StateClaimed,
			AgentID:        command.AgentID,
			Runtime:        command.Runtime,
			LeaseExpiresAt: cloneTime(command.LeaseExpiresAt),
			Reason:         command.Reason,
			Evidence:       append([]Evidence(nil), command.Evidence...),
		}
		next.State = StateClaimed

	case "transition":
		if err := checkFence(next, command); err != nil {
			return nil, false, err
		}
		if command.From == "" {
			return nil, false, fmt.Errorf("from state is required")
		}
		if command.From != next.State {
			return nil, false, fmt.Errorf("%w: expected state %s, current state %s", ErrFenceMismatch, command.From, next.State)
		}
		if !transitions[next.State][command.To] || command.To == StateClaimed {
			return nil, false, transitionError(next.State, command.To)
		}
		previous := next.State
		next.State = command.To
		next.Current.State = command.To
		next.Current.Reason = command.Reason
		next.Current.Evidence = append(next.Current.Evidence, command.Evidence...)
		if command.LeaseExpiresAt != nil {
			next.Current.LeaseExpiresAt = cloneTime(command.LeaseExpiresAt)
		}
		if previous == StateClaimed && command.To == StateStarting {
			t := command.At
			next.Current.StartedAt = &t
		}
		if command.To == StateMerged || command.To == StateBlocked || command.To == StateCancelled {
			t := command.At
			next.Current.FinishedAt = &t
		}

	case "heartbeat":
		if err := checkFence(next, command); err != nil {
			return nil, false, err
		}
		switch next.State {
		case StateClaimed, StateStarting, StateRunning, StateCommitting:
		default:
			return nil, false, fmt.Errorf("heartbeat is not valid in %q", next.State)
		}
		t := command.At
		next.Current.LastHeartbeat = &t
		if command.LeaseExpiresAt != nil {
			next.Current.LeaseExpiresAt = cloneTime(command.LeaseExpiresAt)
		}
		next.Current.Evidence = append(next.Current.Evidence, command.Evidence...)

	default:
		return nil, false, fmt.Errorf("unsupported execution operation %q", command.Operation)
	}

	next.Revision++
	next.UpdatedAt = command.At
	storeReceipt(next, command.IdempotencyKey, command.Operation, fp)
	return next, true, nil
}

func executionIDUsed(record *Record, executionID string) bool {
	if record.Current != nil && record.Current.ExecutionID == executionID {
		return true
	}
	for i := range record.History {
		if record.History[i].ExecutionID == executionID {
			return true
		}
	}
	return false
}

func checkFence(record *Record, command Command) error {
	if record.Current == nil || command.ExecutionID != record.Current.ExecutionID || command.Generation != record.Current.Generation {
		currentID := ""
		currentGeneration := uint64(0)
		if record.Current != nil {
			currentID = record.Current.ExecutionID
			currentGeneration = record.Current.Generation
		}
		return fmt.Errorf("%w: current execution=%q generation=%d", ErrFenceMismatch, currentID, currentGeneration)
	}
	return nil
}

func transitionError(from, to State) error {
	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	copy := *t
	return &copy
}

func cloneRecord(record *Record) *Record {
	data, _ := json.Marshal(record)
	var clone Record
	_ = json.Unmarshal(data, &clone)
	return &clone
}
