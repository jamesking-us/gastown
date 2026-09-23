package formula

import (
	"strings"
	"testing"
)

func TestRefineryPatrolComplianceGateIsAlwaysOnAndFailClosed(t *testing.T) {
	f := loadRefineryPatrolFormula(t)

	complianceGate := requireFormulaStep(t, f, "compliance-gate")
	mergePush := requireFormulaStep(t, f, "merge-push")

	if !containsStepNeed(complianceGate, "handle-failures") {
		t.Fatalf("compliance-gate needs = %v, want handle-failures", complianceGate.Needs)
	}
	if !containsStepNeed(mergePush, "compliance-gate") {
		t.Fatalf("merge-push needs = %v, want compliance-gate", mergePush.Needs)
	}
	if containsStepNeed(mergePush, "handle-failures") {
		t.Fatalf("merge-push has stale direct need on handle-failures: %v", mergePush.Needs)
	}
	if _, err := f.TopologicalSort(); err != nil {
		t.Fatalf("formula DAG has dangling or cyclic needs: %v", err)
	}

	if got := f.Vars["judgment_enabled"].Default; got != "false" {
		t.Fatalf("judgment_enabled default = %q, want false", got)
	}
	if strings.Contains(complianceGate.Description, "{{judgment_enabled}}") ||
		strings.Contains(complianceGate.Description, "skip this step") {
		t.Fatalf("compliance gate must remain mandatory when judgment_enabled=false: %q", complianceGate.Description)
	}

	for _, authority := range []string{
		"docs/compliance-log.md",
		"docs/compliance-gate-procedure.md",
		"ground 1",
		"ground 2",
		"ground 3",
		"checklist control",
		"NOT_TRIGGERED",
		"HOLD",
	} {
		if !strings.Contains(complianceGate.Description, authority) {
			t.Errorf("compliance gate missing authority or fail-closed rule %q", authority)
		}
	}

	for _, instruction := range []string{
		"./tools/refinery/safemerge.sh <full-mr-id>",
		"without a pipeline",
		"SAFE_MERGE_RC=$?",
		"MUST NOT run a\nsecond raw `git merge` after it",
		"PR strategy",
		"target other than `main`",
	} {
		if !strings.Contains(mergePush.Description, instruction) {
			t.Errorf("triggered CCM merge instructions missing %q", instruction)
		}
	}
}
