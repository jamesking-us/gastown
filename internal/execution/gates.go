package execution

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ApplyGate applies a gate requirement or verdict to a copy of record.
func ApplyGate(record *Record, command GateCommand) (*Record, bool, error) {
	if record == nil {
		return nil, false, ErrNotFound
	}
	command.Operation = strings.ToLower(strings.TrimSpace(command.Operation))
	command.Name = strings.TrimSpace(command.Name)
	command.Commit = strings.TrimSpace(command.Commit)
	command.At = normalizeTime(command.At)
	if err := validateID("gate name", command.Name); err != nil {
		return nil, false, err
	}
	if err := validateID("commit", command.Commit); err != nil {
		return nil, false, err
	}
	if err := validateID("idempotency key", command.IdempotencyKey); err != nil {
		return nil, false, err
	}
	fp, err := gateFingerprint(command)
	if err != nil {
		return nil, false, err
	}
	if ok, err := receiptMatches(record, command.IdempotencyKey, "gate-"+command.Operation, fp); err != nil {
		return nil, false, err
	} else if ok {
		return cloneRecord(record), false, nil
	}

	next := cloneRecord(record)
	if next.Gates == nil {
		next.Gates = make(map[string]Gate)
	}
	switch command.Operation {
	case "require":
		gate, exists := next.Gates[command.Name]
		if exists && gate.Required && gate.Commit == command.Commit {
			// Reasserting the same requirement must not erase a verdict or make
			// the same decision generation reviewable twice.
			break
		}
		generation := uint64(1)
		if exists {
			generation = gate.DecisionGeneration + 1
		}
		next.Gates[command.Name] = Gate{
			Name: command.Name, Required: true, Status: GatePending,
			Commit: command.Commit, DecisionGeneration: generation,
			Reason: command.Reason, UpdatedAt: command.At,
		}
	case "decide":
		gate, exists := next.Gates[command.Name]
		if !exists || !gate.Required {
			return nil, false, fmt.Errorf("required gate %q not found", command.Name)
		}
		if gate.Commit != command.Commit {
			return nil, false, fmt.Errorf("%w: gate %s is bound to commit %s", ErrFenceMismatch, command.Name, gate.Commit)
		}
		if command.DecisionGeneration != gate.DecisionGeneration {
			return nil, false, fmt.Errorf("%w: gate %s decision generation is %d", ErrFenceMismatch, command.Name, gate.DecisionGeneration)
		}
		if command.Status != GatePassed && command.Status != GateBlocked {
			return nil, false, fmt.Errorf("gate decision must be passed or blocked")
		}
		if err := validateID("decided by", command.Actor); err != nil {
			return nil, false, err
		}
		gate.Status = command.Status
		gate.DecidedBy = command.Actor
		gate.Reason = command.Reason
		gate.Evidence = append([]Evidence(nil), command.Evidence...)
		gate.UpdatedAt = command.At
		next.Gates[command.Name] = gate
	default:
		return nil, false, fmt.Errorf("unsupported gate operation %q", command.Operation)
	}
	next.Revision++
	next.UpdatedAt = command.At
	storeReceipt(next, command.IdempotencyKey, "gate-"+command.Operation, fp)
	return next, true, nil
}

func gateFingerprint(command GateCommand) (string, error) {
	command.At = time.Time{}
	return fingerprint(command)
}

func EvaluateGates(record Record, commit string) GateEvaluation {
	evaluation := GateEvaluation{WorkID: record.WorkID, Commit: commit, Ready: true}
	for _, gate := range record.Gates {
		if !gate.Required {
			continue
		}
		classified := gate
		if gate.Commit != commit {
			classified.Status = GateSuperseded
		}
		switch classified.Status {
		case GatePassed:
			evaluation.Passed = append(evaluation.Passed, classified)
		case GateBlocked:
			evaluation.Blocked = append(evaluation.Blocked, classified)
			evaluation.Ready = false
		default:
			evaluation.Pending = append(evaluation.Pending, classified)
			evaluation.Ready = false
		}
	}
	gateSort := func(values []Gate) {
		sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	}
	gateSort(evaluation.Pending)
	gateSort(evaluation.Blocked)
	gateSort(evaluation.Passed)
	return evaluation
}
