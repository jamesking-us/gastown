package refinery

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/execution"
)

const (
	executionGatesEnabledEnv = "GT_EXECUTION_GATES_ENABLED"
	executionGateCanaryEnv   = "GT_EXECUTION_GATE_CANARY_RIG"
	executionRequiredEnv     = "GT_EXECUTION_REQUIRED_GATES"
)

type structuralGatePolicy struct {
	enabled   bool
	canaryRig string
	required  []string
}

func structuralGatePolicyFromEnvironment() (structuralGatePolicy, error) {
	raw := strings.TrimSpace(os.Getenv(executionGatesEnabledEnv))
	if raw == "" {
		return structuralGatePolicy{}, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return structuralGatePolicy{}, fmt.Errorf("%s must be a boolean: %w", executionGatesEnabledEnv, err)
	}
	policy := structuralGatePolicy{enabled: enabled}
	if !enabled {
		return policy, nil
	}
	policy.canaryRig = strings.TrimSpace(os.Getenv(executionGateCanaryEnv))
	if policy.canaryRig == "" {
		return structuralGatePolicy{}, fmt.Errorf("%s is required when structural gates are enabled", executionGateCanaryEnv)
	}
	seen := make(map[string]struct{})
	for _, name := range strings.Split(os.Getenv(executionRequiredEnv), ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	for name := range seen {
		policy.required = append(policy.required, name)
	}
	sort.Strings(policy.required)
	if len(policy.required) == 0 {
		return structuralGatePolicy{}, fmt.Errorf("%s must name at least one gate when structural gates are enabled", executionRequiredEnv)
	}
	return policy, nil
}

func (e *Engineer) checkStructuralExecutionGates(mr *MRInfo) error {
	policy, err := structuralGatePolicyFromEnvironment()
	if err != nil {
		return err
	}
	if !policy.enabled || e.rig == nil || policy.canaryRig != strings.TrimSpace(e.rig.Name) {
		return nil
	}
	return e.evaluateStructuralExecutionGates(mr, policy.required)
}

func (e *Engineer) evaluateStructuralExecutionGates(mr *MRInfo, required []string) error {
	workID := strings.TrimSpace(mr.SourceIssue)
	if workID == "" {
		return fmt.Errorf("merge request %s has no source issue", mr.ID)
	}
	commit := strings.TrimSpace(mr.CommitSHA)
	if commit == "" {
		return fmt.Errorf("merge request %s has no submitted commit", mr.ID)
	}
	townRoot := beads.FindTownRoot(e.rig.Path)
	if townRoot == "" {
		return fmt.Errorf("cannot locate town root from rig %s", e.rig.Path)
	}
	record, err := execution.NewStore(townRoot).Load(workID)
	if err != nil {
		return fmt.Errorf("load execution record for %s: %w", workID, err)
	}
	if strings.TrimSpace(record.Rig) != strings.TrimSpace(e.rig.Name) {
		return fmt.Errorf("execution record rig %q does not match refinery rig %q", record.Rig, e.rig.Name)
	}
	evaluation := execution.EvaluateRequiredGates(*record, commit, required)
	if evaluation.Ready {
		return nil
	}
	return fmt.Errorf("work %s at %s has pending gates %s and blocked gates %s",
		workID, shortSHA(commit), gateNames(evaluation.Pending), gateNames(evaluation.Blocked))
}

func gateNames(gates []execution.Gate) string {
	if len(gates) == 0 {
		return "[]"
	}
	names := make([]string, 0, len(gates))
	for _, gate := range gates {
		names = append(names, gate.Name)
	}
	sort.Strings(names)
	return "[" + strings.Join(names, ",") + "]"
}
