package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/execution"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestStructuralGatePolicyRequiresCanaryAndGateNames(t *testing.T) {
	t.Setenv(executionGatesEnabledEnv, "true")
	t.Setenv(executionGateCanaryEnv, "")
	t.Setenv(executionRequiredEnv, "")
	if _, err := structuralGatePolicyFromEnvironment(); err == nil || !strings.Contains(err.Error(), executionGateCanaryEnv) {
		t.Fatalf("missing canary error=%v", err)
	}

	t.Setenv(executionGateCanaryEnv, "ccm")
	if _, err := structuralGatePolicyFromEnvironment(); err == nil || !strings.Contains(err.Error(), executionRequiredEnv) {
		t.Fatalf("missing required gates error=%v", err)
	}

	t.Setenv(executionRequiredEnv, " quality,compliance,quality ")
	policy, err := structuralGatePolicyFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if !policy.enabled || policy.canaryRig != "ccm" || strings.Join(policy.required, ",") != "compliance,quality" {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestStructuralGateCheckIsScopedToCanaryRig(t *testing.T) {
	t.Setenv(executionGatesEnabledEnv, "true")
	t.Setenv(executionGateCanaryEnv, "ccm")
	t.Setenv(executionRequiredEnv, "quality")
	e := &Engineer{rig: &rig.Rig{Path: t.TempDir()}}
	if err := e.checkStructuralExecutionGates(&MRInfo{ID: "mr-1", Rig: "other"}); err != nil {
		t.Fatalf("non-canary rig was evaluated: %v", err)
	}
}

func TestStructuralGateEvaluationFailsClosedAndAcceptsExactCommit(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rigPath := filepath.Join(town, "ccm")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatal(err)
	}
	e := &Engineer{rig: &rig.Rig{Name: "ccm", Path: rigPath}}
	store := execution.NewStore(town)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if _, err := store.Create(execution.CreateRequest{
		WorkID: "ccm-123", Rig: "ccm", IdempotencyKey: "create", Actor: "test", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	mr := &MRInfo{ID: "mr-1", Rig: "ccm", SourceIssue: "ccm-123", CommitSHA: "abc123"}
	if err := e.evaluateStructuralExecutionGates(mr, []string{"quality", "compliance"}); err == nil || !strings.Contains(err.Error(), "[compliance,quality]") {
		t.Fatalf("missing gates error=%v", err)
	}

	for index, name := range []string{"quality", "compliance"} {
		if _, err := store.ApplyGate("ccm-123", execution.GateCommand{
			Operation: "require", Name: name, Commit: "abc123",
			IdempotencyKey: "require-" + name, Actor: "policy", At: now.Add(time.Duration(index+1) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyGate("ccm-123", execution.GateCommand{
			Operation: "decide", Name: name, Commit: "abc123", Status: execution.GatePassed,
			DecisionGeneration: 1, IdempotencyKey: "pass-" + name, Actor: name + "-agent",
			At: now.Add(time.Duration(index+3) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.evaluateStructuralExecutionGates(mr, []string{"quality", "compliance"}); err != nil {
		t.Fatalf("exact passed commit rejected: %v", err)
	}
	mr.CommitSHA = "changed"
	if err := e.evaluateStructuralExecutionGates(mr, []string{"quality", "compliance"}); err == nil || !strings.Contains(err.Error(), "[compliance,quality]") {
		t.Fatalf("changed commit error=%v", err)
	}
}
