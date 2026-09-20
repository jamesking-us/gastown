package execution

import (
	"errors"
	"testing"
)

func TestGateDecisionIsBoundToCommitAndGeneration(t *testing.T) {
	record := readyRecord(t)
	record, changed, err := ApplyGate(record, GateCommand{
		Operation: "require", Name: "quality", Commit: "abc123",
		IdempotencyKey: "gate-require-1", Actor: "policy", At: fixedTime(2),
	})
	if err != nil || !changed {
		t.Fatalf("require changed=%v err=%v", changed, err)
	}
	gate := record.Gates["quality"]
	if gate.Status != GatePending || gate.DecisionGeneration != 1 {
		t.Fatalf("gate=%+v", gate)
	}

	_, _, err = ApplyGate(record, GateCommand{
		Operation: "decide", Name: "quality", Commit: "different", Status: GatePassed,
		DecisionGeneration: 1, IdempotencyKey: "wrong-commit", Actor: "quality-agent", At: fixedTime(3),
	})
	if !errors.Is(err, ErrFenceMismatch) {
		t.Fatalf("wrong commit error=%v", err)
	}
	_, _, err = ApplyGate(record, GateCommand{
		Operation: "decide", Name: "quality", Commit: "abc123", Status: GatePassed,
		DecisionGeneration: 2, IdempotencyKey: "wrong-generation", Actor: "quality-agent", At: fixedTime(3),
	})
	if !errors.Is(err, ErrFenceMismatch) {
		t.Fatalf("wrong generation error=%v", err)
	}

	passed, changed, err := ApplyGate(record, GateCommand{
		Operation: "decide", Name: "quality", Commit: "abc123", Status: GatePassed,
		DecisionGeneration: 1, IdempotencyKey: "decision-1", Actor: "quality-agent",
		Evidence: []Evidence{{Kind: "test_run", Value: "run-42"}}, At: fixedTime(3),
	})
	if err != nil || !changed {
		t.Fatalf("decision changed=%v err=%v", changed, err)
	}
	if !EvaluateGates(*passed, "abc123").Ready {
		t.Fatal("passed required gate did not make evaluation ready")
	}
	if EvaluateGates(*passed, "new456").Ready {
		t.Fatal("old-commit verdict was accepted for a new commit")
	}
}

func TestChangedCommitResetsGateAndIncrementsDecisionGeneration(t *testing.T) {
	record := readyRecord(t)
	record, _, _ = ApplyGate(record, GateCommand{
		Operation: "require", Name: "compliance", Commit: "old",
		IdempotencyKey: "require-old", Actor: "policy", At: fixedTime(2),
	})
	record, _, _ = ApplyGate(record, GateCommand{
		Operation: "decide", Name: "compliance", Commit: "old", Status: GateBlocked,
		DecisionGeneration: 1, IdempotencyKey: "block-old", Actor: "compliance-agent", At: fixedTime(3),
	})
	record, _, err := ApplyGate(record, GateCommand{
		Operation: "require", Name: "compliance", Commit: "old",
		IdempotencyKey: "reassert-old", Actor: "policy", At: fixedTime(4),
	})
	if err != nil || record.Gates["compliance"].Status != GateBlocked {
		t.Fatalf("same-commit requirement erased verdict: gate=%+v err=%v", record.Gates["compliance"], err)
	}
	record, _, err = ApplyGate(record, GateCommand{
		Operation: "require", Name: "compliance", Commit: "new",
		IdempotencyKey: "require-new", Actor: "policy", At: fixedTime(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := record.Gates["compliance"]
	if gate.Status != GatePending || gate.DecisionGeneration != 2 || gate.Commit != "new" || gate.DecidedBy != "" {
		t.Fatalf("reset gate=%+v", gate)
	}
}

func TestGateCommandsAreIdempotentAndSorted(t *testing.T) {
	record := readyRecord(t)
	for _, name := range []string{"quality", "compliance"} {
		var err error
		record, _, err = ApplyGate(record, GateCommand{
			Operation: "require", Name: name, Commit: "abc",
			IdempotencyKey: "require-" + name, Actor: "policy", At: fixedTime(2),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	command := GateCommand{
		Operation: "decide", Name: "quality", Commit: "abc", Status: GatePassed,
		DecisionGeneration: 1, IdempotencyKey: "quality-pass", Actor: "quality", At: fixedTime(3),
	}
	first, changed, err := ApplyGate(record, command)
	if err != nil || !changed {
		t.Fatalf("first changed=%v err=%v", changed, err)
	}
	command.At = fixedTime(9)
	retry, changed, err := ApplyGate(first, command)
	if err != nil || changed || retry.Revision != first.Revision {
		t.Fatalf("retry changed=%v revision=%d err=%v", changed, retry.Revision, err)
	}
	evaluation := EvaluateGates(*retry, "abc")
	if evaluation.Ready || len(evaluation.Pending) != 1 || evaluation.Pending[0].Name != "compliance" {
		t.Fatalf("evaluation=%+v", evaluation)
	}
}

func TestEvaluateRequiredGatesTreatsMissingPolicyGateAsPending(t *testing.T) {
	record := readyRecord(t)
	evaluation := EvaluateRequiredGates(*record, "abc", []string{"compliance", "quality", "quality", ""})
	if evaluation.Ready || len(evaluation.Pending) != 2 {
		t.Fatalf("evaluation=%+v", evaluation)
	}
	if evaluation.Pending[0].Name != "compliance" || evaluation.Pending[1].Name != "quality" {
		t.Fatalf("pending=%+v", evaluation.Pending)
	}
	for _, gate := range evaluation.Pending {
		if gate.Commit != "abc" || gate.Status != GatePending || !gate.Required {
			t.Fatalf("synthetic pending gate=%+v", gate)
		}
	}
}

func TestEvaluateRequiredGatesAlsoHonorsRecordedRequirements(t *testing.T) {
	record := readyRecord(t)
	record, _, err := ApplyGate(record, GateCommand{
		Operation: "require", Name: "security", Commit: "abc",
		IdempotencyKey: "require-security", Actor: "policy", At: fixedTime(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluation := EvaluateRequiredGates(*record, "abc", []string{"quality"})
	if evaluation.Ready || len(evaluation.Pending) != 2 {
		t.Fatalf("evaluation=%+v", evaluation)
	}
}
