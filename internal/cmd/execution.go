package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/execution"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	executionJSON           bool
	executionIdempotencyKey string
	executionActor          string
	executionRig            string
	executionRepository     string
	executionRole           string
	executionID             string
	executionGeneration     uint64
	executionAgentID        string
	executionRuntime        string
	executionReason         string
	executionLeaseUntil     string
	executionEvidence       []string
	executionAsOf           string
)

var executionCmd = &cobra.Command{
	Use:     "execution",
	GroupID: GroupWork,
	Short:   "Manage durable agent execution records",
	Long: `Manage durable, generation-fenced records for work executed by agents.

This command records lifecycle facts. It does not choose work, dispatch agents,
expire leases, or enforce repository policy. Those decisions belong to the
operator or an external controller.

Lifecycle:
  ready -> claimed -> starting -> running -> committing -> submitted -> merged

An attempt may move to recoverable, blocked, or canceled. Claiming recoverable
work creates a new generation and fences updates from the prior attempt.`,
	RunE: requireSubcommand,
}

var executionCreateCmd = &cobra.Command{
	Use:   "create <work-id>",
	Short: "Create a ready execution record",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		record, err := store.Create(execution.CreateRequest{
			WorkID: args[0], Rig: executionRig, Repository: executionRepository, Role: executionRole,
			IdempotencyKey: executionIdempotencyKey, Actor: executionActor,
		})
		if err != nil {
			return err
		}
		return printExecution(record)
	},
}

var executionClaimCmd = &cobra.Command{
	Use:   "claim <work-id>",
	Short: "Claim ready or recoverable work as a new generation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		lease, evidence, err := executionCommandMetadata()
		if err != nil {
			return err
		}
		return runExecutionCommand(args[0], execution.Command{
			Operation: "claim", IdempotencyKey: executionIdempotencyKey,
			ExecutionID: executionID, Generation: executionGeneration, AgentID: executionAgentID,
			Runtime: executionRuntime, Actor: executionActor, Reason: executionReason,
			LeaseExpiresAt: lease, Evidence: evidence,
		})
	},
}

var executionTransitionCmd = &cobra.Command{
	Use:   "transition <work-id> <from-state> <to-state>",
	Short: "Advance a fenced execution attempt",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		lease, evidence, err := executionCommandMetadata()
		if err != nil {
			return err
		}
		return runExecutionCommand(args[0], execution.Command{
			Operation: "transition", IdempotencyKey: executionIdempotencyKey,
			ExecutionID: executionID, Generation: executionGeneration,
			From: execution.State(strings.ToLower(args[1])), To: execution.State(strings.ToLower(args[2])),
			Actor: executionActor, Reason: executionReason,
			LeaseExpiresAt: lease, Evidence: evidence,
		})
	},
}

var executionHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat <work-id>",
	Short: "Record liveness for a fenced execution attempt",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		lease, evidence, err := executionCommandMetadata()
		if err != nil {
			return err
		}
		return runExecutionCommand(args[0], execution.Command{
			Operation: "heartbeat", IdempotencyKey: executionIdempotencyKey,
			ExecutionID: executionID, Generation: executionGeneration,
			Actor: executionActor, LeaseExpiresAt: lease, Evidence: evidence,
		})
	},
}

var executionShowCmd = &cobra.Command{
	Use:   "show <work-id>",
	Short: "Show the replayed current record",
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
		return printExecution(record)
	},
}

var executionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List records reconstructed from their journals",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		records, err := store.List()
		if err != nil {
			return err
		}
		return printExecution(records)
	},
}

var executionEventsCmd = &cobra.Command{
	Use:   "events <work-id>",
	Short: "Show the verified append only journal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		events, err := store.Events(args[0])
		if err != nil {
			return err
		}
		return printExecution(events)
	},
}

var executionVerifyCmd = &cobra.Command{
	Use:   "verify <work-id>",
	Short: "Verify journal sequence and hash chain integrity",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		verification, err := store.Verify(args[0])
		if err != nil {
			return err
		}
		return printExecution(verification)
	},
}

var executionLeasesCmd = &cobra.Command{
	Use:   "leases",
	Short: "Inspect explicit lease timestamps without taking recovery action",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		asOf := time.Now().UTC()
		if executionAsOf != "" {
			asOf, err = time.Parse(time.RFC3339, executionAsOf)
			if err != nil {
				return fmt.Errorf("invalid --as-of %q (use RFC3339): %w", executionAsOf, err)
			}
		}
		records, err := store.List()
		if err != nil {
			return err
		}
		inspections := make([]execution.LeaseInspection, 0, len(records))
		for _, record := range records {
			inspections = append(inspections, execution.InspectLease(record, asOf))
		}
		return printExecution(inspections)
	},
}

func init() {
	executionCmd.PersistentFlags().BoolVar(&executionJSON, "json", false, "Output JSON")
	for _, command := range []*cobra.Command{executionCreateCmd, executionClaimCmd, executionTransitionCmd, executionHeartbeatCmd} {
		command.Flags().StringVar(&executionIdempotencyKey, "idempotency-key", "", "Unique command key (required)")
		_ = command.MarkFlagRequired("idempotency-key")
		command.Flags().StringVar(&executionActor, "actor", "", "Actor recording the command")
	}
	executionCreateCmd.Flags().StringVar(&executionRig, "rig", "", "Owning rig")
	executionCreateCmd.Flags().StringVar(&executionRepository, "repository", "", "Repository containing the work")
	executionCreateCmd.Flags().StringVar(&executionRole, "role", "", "Requested agent role")

	for _, command := range []*cobra.Command{executionClaimCmd, executionTransitionCmd, executionHeartbeatCmd} {
		command.Flags().StringVar(&executionID, "execution-id", "", "Attempt identity (required)")
		_ = command.MarkFlagRequired("execution-id")
		command.Flags().StringVar(&executionLeaseUntil, "lease-until", "", "Lease expiry in RFC3339 format")
		command.Flags().StringSliceVar(&executionEvidence, "evidence", nil, "Evidence reference as kind=value (repeatable)")
	}
	executionClaimCmd.Flags().Uint64Var(&executionGeneration, "generation", 0, "Expected new generation (0 accepts the next generation)")
	executionClaimCmd.Flags().StringVar(&executionAgentID, "agent", "", "Agent identity")
	executionClaimCmd.Flags().StringVar(&executionRuntime, "runtime", "", "Agent runtime, such as codex or claude")
	executionClaimCmd.Flags().StringVar(&executionReason, "reason", "", "Reason for claim or recovery")

	for _, command := range []*cobra.Command{executionTransitionCmd, executionHeartbeatCmd} {
		command.Flags().Uint64Var(&executionGeneration, "generation", 0, "Attempt generation (required)")
		_ = command.MarkFlagRequired("generation")
	}
	executionTransitionCmd.Flags().StringVar(&executionReason, "reason", "", "Reason for the transition")
	executionLeasesCmd.Flags().StringVar(&executionAsOf, "as-of", "", "Inspection time in RFC3339 format (default now)")

	executionCmd.AddCommand(executionCreateCmd, executionClaimCmd, executionTransitionCmd, executionHeartbeatCmd)
	executionCmd.AddCommand(executionShowCmd, executionListCmd, executionEventsCmd, executionVerifyCmd, executionLeasesCmd)
	rootCmd.AddCommand(executionCmd)
}

func executionStore() (*execution.Store, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return execution.NewStore(townRoot), nil
}

func runExecutionCommand(workID string, command execution.Command) error {
	store, err := executionStore()
	if err != nil {
		return err
	}
	record, err := store.Apply(workID, command)
	if err != nil {
		return err
	}
	return printExecution(record)
}

func executionCommandMetadata() (*time.Time, []execution.Evidence, error) {
	lease, err := parseExecutionLease()
	if err != nil {
		return nil, nil, err
	}
	evidence, err := parseExecutionEvidence()
	if err != nil {
		return nil, nil, err
	}
	return lease, evidence, nil
}

func parseExecutionLease() (*time.Time, error) {
	if executionLeaseUntil == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, executionLeaseUntil)
	if err != nil {
		return nil, fmt.Errorf("invalid --lease-until %q (use RFC3339): %w", executionLeaseUntil, err)
	}
	return &parsed, nil
}

func parseExecutionEvidence() ([]execution.Evidence, error) {
	var evidence []execution.Evidence
	for _, item := range executionEvidence {
		kind, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(kind) == "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("invalid --evidence %q (use kind=value)", item)
		}
		evidence = append(evidence, execution.Evidence{Kind: strings.TrimSpace(kind), Value: strings.TrimSpace(value)})
	}
	return evidence, nil
}

func printExecution(value any) error {
	if executionJSON {
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	switch value := value.(type) {
	case *execution.Record:
		generation := uint64(0)
		executionID := "-"
		if value.Current != nil {
			generation = value.Current.Generation
			executionID = value.Current.ExecutionID
		}
		fmt.Printf("%s  state=%s  revision=%d  execution=%s  generation=%d\n", value.WorkID, value.State, value.Revision, executionID, generation)
	case []execution.Record:
		for i := range value {
			if err := printExecution(&value[i]); err != nil {
				return err
			}
		}
	case []execution.Event:
		for _, event := range value {
			fmt.Printf("%s  %s  seq=%d  revision=%d  hash=%s\n", event.Timestamp.Format(time.RFC3339), event.Operation, event.Sequence, event.Record.Revision, event.Hash[:12])
		}
	case *execution.Verification:
		fmt.Printf("%s  verified events=%d revision=%d hash=%s\n", value.WorkID, value.Events, value.Revision, value.LastHash)
	case *execution.GateEvaluation:
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	case []execution.LeaseInspection:
		for _, lease := range value {
			fmt.Printf("%s  state=%s  lease=%s", lease.WorkID, lease.State, lease.Condition)
			if lease.LeaseExpiresAt != nil {
				fmt.Printf("  expires=%s", lease.LeaseExpiresAt.Format(time.RFC3339))
			}
			fmt.Println()
		}
	case *execution.ControllerLease:
		fmt.Printf("controller=%s  epoch=%d  revision=%d  expires=%s\n",
			value.ControllerID, value.Epoch, value.Revision, value.LeaseExpiresAt.Format(time.RFC3339))
	case []execution.ControllerEvent:
		for _, event := range value {
			fmt.Printf("%s  %s  seq=%d  epoch=%d  revision=%d  hash=%s\n",
				event.Timestamp.Format(time.RFC3339), event.Operation, event.Sequence,
				event.Lease.Epoch, event.Lease.Revision, event.Hash[:12])
		}
	case *execution.ControllerVerification:
		fmt.Printf("controller verified events=%d epoch=%d revision=%d hash=%s\n",
			value.Events, value.Epoch, value.Revision, value.LastHash)
	default:
		return fmt.Errorf("unsupported execution output %s", strconv.Quote(fmt.Sprintf("%T", value)))
	}
	return nil
}
