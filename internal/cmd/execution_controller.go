package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/execution"
)

var (
	executionControllerID    string
	executionControllerEpoch uint64
	executionControllerUntil string
)

var executionControllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "Manage the durable controller epoch and lease",
	RunE:  requireSubcommand,
}

var executionControllerAcquireCmd = &cobra.Command{
	Use:   "acquire",
	Short: "Acquire a new controller epoch when no active lease exists",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExecutionController("acquire")
	},
}

var executionControllerRenewCmd = &cobra.Command{
	Use:   "renew",
	Short: "Extend the active controller lease with an epoch fence",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExecutionController("renew")
	},
}

var executionControllerReleaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release the active controller lease with an epoch fence",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExecutionController("release")
	},
}

var executionControllerShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the replayed current controller lease",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		lease, err := store.LoadController()
		if err != nil {
			return err
		}
		return printExecution(lease)
	},
}

var executionControllerEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Show the verified append only controller journal",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		events, err := store.ControllerEvents()
		if err != nil {
			return err
		}
		return printExecution(events)
	},
}

var executionControllerVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify controller journal sequence and hash chain integrity",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := executionStore()
		if err != nil {
			return err
		}
		verification, err := store.VerifyController()
		if err != nil {
			return err
		}
		return printExecution(verification)
	},
}

func runExecutionController(operation string) error {
	var until time.Time
	var err error
	if operation != "release" {
		until, err = time.Parse(time.RFC3339, executionControllerUntil)
		if err != nil {
			return fmt.Errorf("invalid --lease-until %q (use RFC3339): %w", executionControllerUntil, err)
		}
	}
	store, err := executionStore()
	if err != nil {
		return err
	}
	lease, err := store.ApplyController(execution.ControllerCommand{
		Operation: operation, ControllerID: executionControllerID,
		ExpectedEpoch: executionControllerEpoch, LeaseExpiresAt: until,
		IdempotencyKey: executionIdempotencyKey, Actor: executionActor,
	})
	if err != nil {
		return err
	}
	return printExecution(lease)
}

func init() {
	for _, command := range []*cobra.Command{
		executionControllerAcquireCmd, executionControllerRenewCmd, executionControllerReleaseCmd,
	} {
		command.Flags().StringVar(&executionControllerID, "controller-id", "", "Controller identity (required)")
		_ = command.MarkFlagRequired("controller-id")
		command.Flags().StringVar(&executionIdempotencyKey, "idempotency-key", "", "Unique command key (required)")
		_ = command.MarkFlagRequired("idempotency-key")
		command.Flags().StringVar(&executionActor, "actor", "", "Actor recording the command")
	}
	for _, command := range []*cobra.Command{executionControllerAcquireCmd, executionControllerRenewCmd} {
		command.Flags().StringVar(&executionControllerUntil, "lease-until", "", "Lease expiry in RFC3339 format (required)")
		_ = command.MarkFlagRequired("lease-until")
	}
	for _, command := range []*cobra.Command{executionControllerRenewCmd, executionControllerReleaseCmd} {
		command.Flags().Uint64Var(&executionControllerEpoch, "epoch", 0, "Expected controller epoch (required)")
		_ = command.MarkFlagRequired("epoch")
	}
	executionControllerCmd.AddCommand(
		executionControllerAcquireCmd, executionControllerRenewCmd, executionControllerReleaseCmd,
		executionControllerShowCmd, executionControllerEventsCmd, executionControllerVerifyCmd,
	)
	executionCmd.AddCommand(executionControllerCmd)
}
