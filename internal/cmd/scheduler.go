package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	schedulerStatusJSON bool
	schedulerListJSON   bool
	schedulerClearBead  string
	schedulerRunBatch   int
	schedulerRunDryRun  bool
)

var schedulerCmd = &cobra.Command{
	Use:     "scheduler",
	GroupID: GroupWork,
	Short:   "Manage dispatch scheduler",
	Long: `Manage the capacity-controlled dispatch scheduler.

Subcommands:
  gt scheduler status    # Show scheduler state
  gt scheduler list      # List all scheduled beads
  gt scheduler run       # Manual dispatch trigger
  gt scheduler pause     # Pause dispatch
  gt scheduler resume    # Resume dispatch
  gt scheduler clear     # Remove beads from scheduler

Config:
  gt config set scheduler.max_polecats 5    # Enable deferred dispatch
  gt config set scheduler.max_polecats -1   # Direct dispatch (default)`,
	RunE: requireSubcommand,
}

var schedulerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show scheduler state: pending, capacity, active polecats",
	RunE:  runSchedulerStatus,
}

var schedulerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all scheduled beads with titles, rig, blocked status",
	RunE:  runSchedulerList,
}

var schedulerPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause all scheduler dispatch (town-wide)",
	RunE:  runSchedulerPause,
}

var schedulerResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume scheduler dispatch",
	RunE:  runSchedulerResume,
}

var schedulerClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Remove beads from the scheduler",
	Long: `Remove beads from the scheduler by closing sling context beads.

Without --bead, removes ALL beads from the scheduler.
With --bead, removes only the specified bead.`,
	RunE: runSchedulerClear,
}

var schedulerRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Manually trigger scheduler dispatch",
	Long: `Manually trigger dispatch of scheduled work.

This dispatches scheduled beads using the same logic as the daemon heartbeat,
but can be run ad-hoc. Useful for testing or when the daemon is not running.

  gt scheduler run                  # Dispatch using config defaults
  gt scheduler run --batch 5        # Dispatch up to 5
  gt scheduler run --dry-run        # Preview what would dispatch`,
	RunE: runSchedulerRun,
}

func init() {
	// Status flags
	schedulerStatusCmd.Flags().BoolVar(&schedulerStatusJSON, "json", false, "Output as JSON")

	// List flags
	schedulerListCmd.Flags().BoolVar(&schedulerListJSON, "json", false, "Output as JSON")

	// Clear flags
	schedulerClearCmd.Flags().StringVar(&schedulerClearBead, "bead", "", "Remove specific bead from scheduler")

	// Run flags
	schedulerRunCmd.Flags().IntVar(&schedulerRunBatch, "batch", 0, "Override batch size (0 = use config)")
	schedulerRunCmd.Flags().BoolVar(&schedulerRunDryRun, "dry-run", false, "Preview what would dispatch")

	// Build command tree (flat — no intermediary "capacity" level)
	schedulerCmd.AddCommand(schedulerStatusCmd)
	schedulerCmd.AddCommand(schedulerListCmd)
	schedulerCmd.AddCommand(schedulerPauseCmd)
	schedulerCmd.AddCommand(schedulerResumeCmd)
	schedulerCmd.AddCommand(schedulerClearCmd)
	schedulerCmd.AddCommand(schedulerRunCmd)

	rootCmd.AddCommand(schedulerCmd)
}

// scheduledBeadInfo holds info about a scheduled bead for display.
type scheduledBeadInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	TargetRig string `json:"target_rig"`
	Blocked   bool   `json:"blocked,omitempty"`
}

func runSchedulerStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	state, err := capacity.LoadState(townRoot)
	if err != nil {
		return fmt.Errorf("loading scheduler state: %w", err)
	}

	scheduled, scanFailures, err := listScheduledBeads(townRoot)
	if err != nil {
		return fmt.Errorf("listing scheduled beads: %w", err)
	}
	if schedulerStatusJSON {
		reportSlingContextScanFailures(scanFailures)
	}

	capacitySnapshot, err := polecatCapacitySnapshotForTown(townRoot)
	if err != nil {
		return fmt.Errorf("loading polecat capacity: %w", err)
	}

	if schedulerStatusJSON {
		out := struct {
			Paused         bool                    `json:"paused"`
			PausedBy       string                  `json:"paused_by,omitempty"`
			ScheduledTotal int                     `json:"queued_total"`
			ScheduledReady int                     `json:"queued_ready"`
			ActivePolecats int                     `json:"active_polecats"`
			Capacity       polecatCapacitySnapshot `json:"capacity"`
			LastDispatchAt string                  `json:"last_dispatch_at,omitempty"`
			Beads          []scheduledBeadInfo     `json:"beads"`
			Skipped        []skippedContextInfo    `json:"skipped_contexts,omitempty"`
		}{
			Paused:         state.Paused,
			PausedBy:       state.PausedBy,
			ScheduledTotal: len(scheduled),
			ActivePolecats: capacitySnapshot.ActiveSessions,
			Capacity:       capacitySnapshot,
			LastDispatchAt: state.LastDispatchAt,
			Beads:          scheduled,
			Skipped:        skippedContextInfos(scanFailures),
		}
		for _, b := range scheduled {
			if !b.Blocked {
				out.ScheduledReady++
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	readyCount := 0
	for _, b := range scheduled {
		if !b.Blocked {
			readyCount++
		}
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Scheduler Status"))
	if state.Paused {
		fmt.Printf("  State:    %s (by %s)\n", style.Warning.Render("PAUSED"), state.PausedBy)
	} else {
		fmt.Printf("  State:    active\n")
	}
	fmt.Printf("  Scheduled: %d total, %d ready\n", len(scheduled), readyCount)
	printSkippedContexts(scanFailures)
	fmt.Printf("  Active:    %d polecats\n", capacitySnapshot.ActiveSessions)
	if capacitySnapshot.Max > 0 {
		fmt.Printf("  Capacity:  %d free of %d (working: %d, recovery: %d, reservations: %d, reusable idle: %d, pending MR: %d)\n",
			capacitySnapshot.Free,
			capacitySnapshot.Max,
			capacitySnapshot.Working,
			capacitySnapshot.RecoveryBlocked,
			capacitySnapshot.Reservations,
			capacitySnapshot.ReusableIdle,
			capacitySnapshot.PendingMR,
		)
	} else {
		fmt.Printf("  Capacity:  direct dispatch (scheduler.max_polecats=%d)\n", capacitySnapshot.Max)
	}
	if state.LastDispatchAt != "" {
		fmt.Printf("  Last dispatch: %s (%d beads)\n", state.LastDispatchAt, state.LastDispatchCount)
	}

	return nil
}

func runSchedulerList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	scheduled, scanFailures, err := listScheduledBeads(townRoot)
	if err != nil {
		return fmt.Errorf("listing scheduled beads: %w", err)
	}
	if schedulerListJSON {
		// stdout stays pure JSON; skipped contexts are named on stderr.
		reportSlingContextScanFailures(scanFailures)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(scheduled)
	}

	if len(scheduled) == 0 {
		fmt.Println("No beads scheduled.")
		printSkippedContexts(scanFailures)
		fmt.Println("Enable deferred dispatch with: gt config set scheduler.max_polecats <N>")
		return nil
	}

	byRig := make(map[string][]scheduledBeadInfo)
	for _, b := range scheduled {
		byRig[b.TargetRig] = append(byRig[b.TargetRig], b)
	}

	fmt.Printf("%s (%d beads)\n\n", style.Bold.Render("Scheduled Work"), len(scheduled))
	printSkippedContexts(scanFailures)
	for rig, beads := range byRig {
		fmt.Printf("  %s (%d):\n", style.Bold.Render(rig), len(beads))
		for _, b := range beads {
			indicator := "○"
			if b.Blocked {
				indicator = "⏸"
			}
			fmt.Printf("    %s %s: %s\n", indicator, b.ID, b.Title)
		}
		fmt.Println()
	}

	return nil
}

func runSchedulerPause(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	state, err := capacity.LoadState(townRoot)
	if err != nil {
		return fmt.Errorf("loading scheduler state: %w", err)
	}

	if state.Paused {
		fmt.Printf("%s Scheduler is already paused (by %s)\n", style.Dim.Render("○"), state.PausedBy)
		return nil
	}

	actor := detectActor()
	state.SetPaused(actor)
	if err := capacity.SaveState(townRoot, state); err != nil {
		return fmt.Errorf("saving scheduler state: %w", err)
	}

	fmt.Printf("%s Scheduler paused\n", style.Bold.Render("⏸"))
	return nil
}

func runSchedulerResume(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	state, err := capacity.LoadState(townRoot)
	if err != nil {
		return fmt.Errorf("loading scheduler state: %w", err)
	}

	if !state.Paused {
		fmt.Printf("%s Scheduler is not paused\n", style.Dim.Render("○"))
		return nil
	}

	state.SetResumed()
	if err := capacity.SaveState(townRoot, state); err != nil {
		return fmt.Errorf("saving scheduler state: %w", err)
	}

	fmt.Printf("%s Scheduler resumed\n", style.Bold.Render("▶"))
	return nil
}

func runSchedulerClear(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if schedulerClearBead != "" {
		// Close ALL sling contexts for this specific work bead (there may be
		// duplicates if concurrent scheduleBead calls raced past idempotency).
		// Scan all rig dirs since contexts live in target rig beads. (GH#3468)
		contexts, err := listAllSlingContextRecords(townRoot)
		if err != nil {
			return fmt.Errorf("listing sling contexts: %w", err)
		}

		closed := 0
		for _, ctx := range contexts {
			fields := beads.ParseSlingContextFields(ctx.issue.Description)
			if fields != nil && fields.WorkBeadID == schedulerClearBead {
				if err := beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "cleared"); err != nil {
					fmt.Printf("  %s Could not close context %s: %v\n", style.Dim.Render("Warning:"), ctx.issue.ID, err)
					continue
				}
				closed++
			}
		}

		if closed == 0 {
			fmt.Printf("%s No sling context found for %s\n", style.Dim.Render("○"), schedulerClearBead)
		} else {
			fmt.Printf("%s Removed %s from scheduler (closed %d context(s))\n",
				style.Bold.Render("✓"), schedulerClearBead, closed)
		}
		return nil
	}

	// Close all open sling contexts across all dirs
	allContexts, err := listAllSlingContextRecords(townRoot)
	if err != nil {
		return fmt.Errorf("listing sling contexts: %w", err)
	}

	if len(allContexts) == 0 {
		fmt.Println("Scheduler is already empty.")
		return nil
	}

	cleared := 0
	for _, ctx := range allContexts {
		if err := beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "cleared"); err != nil {
			fmt.Printf("  %s Could not close context %s: %v\n", style.Dim.Render("Warning:"), ctx.issue.ID, err)
			continue
		}
		cleared++
	}

	fmt.Printf("%s Cleared %d context bead(s) from scheduler\n", style.Bold.Render("✓"), cleared)
	return nil
}

func runSchedulerRun(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	_, err = dispatchScheduledWork(townRoot, detectActor(), schedulerRunBatch, schedulerRunDryRun)
	return err
}

// listScheduledBeads returns info about all scheduled beads for display, plus
// one entry per sling context that could not be resolved. Reconciles sling
// context beads with work bead readiness to mark blocked status. Uses batch
// fetch for work bead info to avoid N+1 subprocess spawns.
//
// An unresolvable context is skipped and reported, not fatal: `gt scheduler
// status` is the gate every SLOT_OPEN dispatch trigger runs first, so aborting
// the whole listing on one broken context dir would stop town-wide dispatch.
func listScheduledBeads(townRoot string) ([]scheduledBeadInfo, []slingContextScanFailure, error) {
	assessments, failures, err := assessScheduledContexts(townRoot)
	if err != nil {
		return nil, failures, err
	}
	return scheduledBeadInfosFromAssessments(assessments), failures, nil
}

// skippedContextInfo reports one unresolvable sling context in --json output.
type skippedContextInfo struct {
	BeadsDir string `json:"beads_dir"`
	Error    string `json:"error"`
}

func skippedContextInfos(failures []slingContextScanFailure) []skippedContextInfo {
	if len(failures) == 0 {
		return nil
	}
	out := make([]skippedContextInfo, 0, len(failures))
	for _, f := range failures {
		out = append(out, skippedContextInfo{BeadsDir: f.beadsDir, Error: f.err.Error()})
	}
	return out
}

// printSkippedContexts names each skipped sling context in human-readable output.
func printSkippedContexts(failures []slingContextScanFailure) {
	if len(failures) == 0 {
		return
	}
	fmt.Printf("  %s  %d unresolvable sling context(s):\n",
		style.Warning.Render("Skipped:"), len(failures))
	for _, f := range failures {
		fmt.Printf("    %s: %v\n", f.beadsDir, f.err)
	}
}

func scheduledBeadInfosFromAssessments(assessments []scheduledContextAssessment) []scheduledBeadInfo {
	var result []scheduledBeadInfo
	for _, assessment := range assessments {
		bead, ok := scheduledBeadInfoFromWork(assessment.context.issue.Title, assessment.fields, assessment.info, assessment.found, assessment.ready)
		if !ok {
			continue
		}
		result = append(result, bead)
	}

	return result
}

func scheduledBeadInfoFromWork(ctxTitle string, fields *capacity.SlingContextFields, info beadStatusInfo, found, ready bool) (scheduledBeadInfo, bool) {
	if fields == nil {
		return scheduledBeadInfo{}, false
	}
	title := ctxTitle
	status := "open"
	if found {
		title = info.Title
		status = info.Status
		if status == string(beads.IssueStatusHooked) || status == "closed" || status == "tombstone" {
			return scheduledBeadInfo{}, false
		}
	}
	return scheduledBeadInfo{
		ID:        fields.WorkBeadID,
		Title:     title,
		Status:    status,
		TargetRig: fields.TargetRig,
		Blocked:   !ready,
	}, true
}

// beadsSearchDirs returns the directories to scan for scheduled beads: the town
// root plus every REGISTERED rig location that carries a .beads directory.
//
// Registration is authoritative, and deliberately so. This used to glob the town
// root — every directory under it holding a .beads subdir was treated as a rig —
// so any stray checkout, backup copy or leftover config dir left lying under the
// town root was pulled into the town-wide dispatch path. That is not
// hypothetical: /gt/beads (an unrouted, unregistered, server-mode config dir
// whose database could not be opened) failed dispatch 165 times over 9 hours;
// renaming it in place did not quarantine it because the scan simply followed
// the new name; and /gt/forkrig reproduced the same town-wide outage days later.
// A directory nobody registered must not be able to influence dispatch at all,
// broken or not — merely scanning one probes its metadata and can create a
// phantom database on the Dolt server.
//
// Both town registries are consulted, and their union is searched:
//   - mayor/rigs.json (falling back to <townRoot>/rigs.json) names the rigs; for
//     each, the rig dir and its mayor/rig dir are searched when they have .beads.
//   - .beads/routes.jsonl names the dirs bd itself routes to, which is where
//     sling contexts for a rig actually live.
//
// The union keeps coverage broad — a rig registered before its route is added,
// or routed before it is registered, is still searched — while excluding
// anything that appears in neither registry. Losing BOTH registries is an error
// rather than an empty result: no search dirs would read as "nothing scheduled".
func beadsSearchDirs(townRoot string) ([]string, error) {
	dirs := []string{townRoot}
	seen := map[string]bool{townRoot: true}
	add := func(dir string) {
		if seen[dir] {
			return
		}
		if _, err := os.Stat(filepath.Join(dir, constants.DirBeads)); err != nil {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}

	rigNames, rigsErr := registeredRigNames(townRoot)
	for _, name := range rigNames {
		rigDir := filepath.Join(townRoot, name)
		add(rigDir)
		add(filepath.Join(rigDir, constants.DirMayor, constants.DirRig))
	}

	routes, routesErr := townRoutes(townRoot)
	for _, route := range routes {
		if route.Path == "" {
			continue
		}
		routeDir := route.Path
		if !filepath.IsAbs(routeDir) {
			routeDir = filepath.Join(townRoot, routeDir)
		}
		add(routeDir)
	}

	if rigsErr != nil && routesErr != nil {
		return nil, fmt.Errorf("discovering scheduler beads search dirs: no readable town registry "+
			"(rigs.json: %v; routes.jsonl: %v) — run 'gt doctor'", rigsErr, routesErr)
	}
	return dirs, nil
}

// registeredRigNames reads the rig names from the town's rig registry,
// mayor/rigs.json, falling back to a town-root rigs.json the way the session
// prefix registry does.
func registeredRigNames(townRoot string) ([]string, error) {
	var lastErr error
	for _, path := range []string{
		constants.MayorRigsPath(townRoot),
		filepath.Join(townRoot, constants.FileRigsJSON),
	} {
		cfg, err := config.LoadRigsConfig(path)
		if err != nil {
			lastErr = err
			continue
		}
		names := make([]string, 0, len(cfg.Rigs))
		for name := range cfg.Rigs {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, nil
	}
	return nil, lastErr
}

// townRoutes loads the town's beads routing table. Unlike beads.LoadRoutes, a
// missing routes.jsonl is reported as an error: beadsSearchDirs needs to tell
// "this town routes nothing" from "this town's routing table is gone".
func townRoutes(townRoot string) ([]beads.Route, error) {
	townBeadsDir := filepath.Join(townRoot, constants.DirBeads)
	if _, err := os.Stat(filepath.Join(townBeadsDir, beads.RoutesFileName)); err != nil {
		return nil, err
	}
	return beads.LoadRoutes(townBeadsDir)
}

// countActivePolecats counts all running polecat tmux sessions across all rigs.
// Capacity admission uses polecatCapacitySnapshotForTown instead; active sessions
// are shown for operator context only.
func countActivePolecats() int {
	listCmd := tmux.BuildCommand("list-sessions", "-F", "#{session_name}")
	out, err := listCmd.Output()
	if err != nil {
		return 0
	}

	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		identity, err := session.ParseSessionName(line)
		if err != nil {
			continue
		}
		if identity.Role == session.RolePolecat {
			count++
		}
	}
	return count
}
