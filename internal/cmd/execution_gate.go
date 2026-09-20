package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/execution"
)

var (
	executionGateCommit     string
	executionGateGeneration uint64
	executionGateDecidedBy  string
	executionGateRequired   []string
)

var executionGateCmd = &cobra.Command{
	Use:   "gate",
	Short: "Manage commit-bound structural gate records",
	RunE:  requireSubcommand,
}

var executionGateRequireCmd = &cobra.Command{
	Use:   "require <work-id> <gate>",
	Short: "Require a pending gate for an exact commit",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		record, err := store.ApplyGate(args[0], execution.GateCommand{
			Operation: "require", Name: args[1], Commit: executionGateCommit,
			IdempotencyKey: executionIdempotencyKey, Actor: executionActor, Reason: executionReason,
		})
		if err != nil {
			return err
		}
		return printExecution(record)
	},
}

var executionGateDecideCmd = &cobra.Command{
	Use:   "decide <work-id> <gate> <passed|blocked>",
	Short: "Record a fenced verdict for an exact commit",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		evidence, err := parseExecutionEvidence()
		if err != nil {
			return err
		}
		store, err := executionStore()
		if err != nil {
			return err
		}
		record, err := store.ApplyGate(args[0], execution.GateCommand{
			Operation: "decide", Name: args[1], Commit: executionGateCommit,
			Status:             execution.GateStatus(strings.ToLower(args[2])),
			DecisionGeneration: executionGateGeneration,
			IdempotencyKey:     executionIdempotencyKey, Actor: executionGateDecidedBy,
			Reason: executionReason, Evidence: evidence,
		})
		if err != nil {
			return err
		}
		return printExecution(record)
	},
}

var executionGateStatusCmd = &cobra.Command{
	Use:   "status <work-id>",
	Short: "Evaluate required gates for an exact commit without enforcement",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		record, err := store.Load(args[0])
		if err != nil {
			return err
		}
		evaluation := execution.EvaluateRequiredGates(*record, executionGateCommit, executionGateRequired)
		if executionJSON {
			return printExecution(&evaluation)
		}
		fmt.Printf("%s  commit=%s  ready=%t  passed=%d  pending=%d  blocked=%d\n",
			evaluation.WorkID, evaluation.Commit, evaluation.Ready,
			len(evaluation.Passed), len(evaluation.Pending), len(evaluation.Blocked))
		return nil
	},
}

func init() {
	for _, command := range []*cobra.Command{executionGateRequireCmd, executionGateDecideCmd} {
		command.Flags().StringVar(&executionGateCommit, "commit", "", "Exact submitted commit (required)")
		_ = command.MarkFlagRequired("commit")
		command.Flags().StringVar(&executionIdempotencyKey, "idempotency-key", "", "Unique command key (required)")
		_ = command.MarkFlagRequired("idempotency-key")
		command.Flags().StringVar(&executionReason, "reason", "", "Requirement or verdict reason")
	}
	executionGateRequireCmd.Flags().StringVar(&executionActor, "actor", "", "Policy actor requiring the gate")
	executionGateDecideCmd.Flags().Uint64Var(&executionGateGeneration, "decision-generation", 0, "Expected gate generation (required)")
	_ = executionGateDecideCmd.MarkFlagRequired("decision-generation")
	executionGateDecideCmd.Flags().StringVar(&executionGateDecidedBy, "decided-by", "", "Human or agent recording the verdict (required)")
	_ = executionGateDecideCmd.MarkFlagRequired("decided-by")
	executionGateDecideCmd.Flags().StringSliceVar(&executionEvidence, "evidence", nil, "Evidence reference as kind=value (repeatable)")
	executionGateStatusCmd.Flags().StringVar(&executionGateCommit, "commit", "", "Exact submitted commit (required)")
	_ = executionGateStatusCmd.MarkFlagRequired("commit")
	executionGateStatusCmd.Flags().StringSliceVar(&executionGateRequired, "required", nil, "Policy-required gate name (repeatable or comma-separated)")

	executionGateCmd.AddCommand(executionGateRequireCmd, executionGateDecideCmd, executionGateStatusCmd)
	executionCmd.AddCommand(executionGateCmd)
}
