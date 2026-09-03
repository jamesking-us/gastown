package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/workspace"
)

var heartbeatCmd = &cobra.Command{
	Use:     "heartbeat",
	GroupID: GroupDiag,
	Short:   "Update agent heartbeat state",
	Long: `Update the agent heartbeat with a specific state.

Used by agents to self-report their state to the witness. The witness reads
the heartbeat state instead of inferring it from timers (ZFC: gt-3vr5).

States:
  working  - Actively processing (default)
  idle     - Waiting for input
  exiting  - In gt done flow
  stuck    - Self-reporting stuck (triggers witness escalation)

Examples:
  gt heartbeat --state=stuck "blocked on auth issue"
  gt heartbeat --state=idle
  gt heartbeat --state=working`,
	RunE: runHeartbeat,
}

var heartbeatState string

func init() {
	rootCmd.AddCommand(heartbeatCmd)
	heartbeatCmd.Flags().StringVar(&heartbeatState, "state", "working", "Agent state (working, idle, exiting, stuck)")
}

func runHeartbeat(cmd *cobra.Command, args []string) error {
	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		return fmt.Errorf("GT_SESSION not set (not running in a Gas Town session)")
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("could not find town root: %v", err)
	}

	state := polecat.HeartbeatState(heartbeatState)
	switch state {
	case polecat.HeartbeatWorking, polecat.HeartbeatIdle, polecat.HeartbeatExiting, polecat.HeartbeatStuck:
		// valid
	default:
		return fmt.Errorf("invalid state %q (must be working, idle, exiting, or stuck)", heartbeatState)
	}

	context := ""
	if len(args) > 0 {
		context = strings.Join(args, " ")
	}

	// Report the write instead of dropping it: every role reaches this store,
	// and "Heartbeat updated" printed over a failed write is what lets a live
	// agent look dead to the witness (hq-huln).
	if err := polecat.TouchSessionHeartbeatWithStateErr(townRoot, sessionName, state, context, ""); err != nil {
		return fmt.Errorf("updating session heartbeat: %w", err)
	}

	// Deacon liveness has extra stores beyond session heartbeat. Keep the
	// generic heartbeat command and `gt deacon heartbeat` on one shared path.
	detail := ""
	if os.Getenv("GT_ROLE") == "deacon" {
		res := syncDeaconHeartbeatStores(townRoot, context)
		if err := res.Err(); err != nil {
			return fmt.Errorf("updating deacon heartbeat: %w", err)
		}
		detail = " (" + res.Summary() + ")"
	}

	fmt.Printf("Heartbeat updated: state=%s%s\n", state, detail)
	return nil
}

// deaconBeadHeartbeatSyncThreshold throttles agent-bead label refreshes from
// gt heartbeat: each refresh is a Dolt commit, so only sync when the label is
// stale enough to matter to watchers. The throttle is deliberate and stays;
// what it must never do is pass for a completed heartbeat (hq-huln).
const deaconBeadHeartbeatSyncThreshold = deacon.HeartbeatStaleThreshold / 2

// beadHeartbeatResult reports what the agent-bead heartbeat store did on one
// invocation. That store can write, be throttled, or fail, and callers have to
// tell those apart: it is the heartbeat:EPOCH label on the Deacon's agent bead,
// the store Witness second-order monitoring reads to decide whether the Deacon
// is alive. Before hq-huln this function returned nothing, so a bd outage and a
// throttled skip were both indistinguishable from a completed write.
type beadHeartbeatResult struct {
	// Throttled reports that the stored label was younger than
	// deaconBeadHeartbeatSyncThreshold, so no write was attempted.
	Throttled bool
	// NextRefresh is how long until a throttled label may be refreshed again.
	NextRefresh time.Duration
	// Epoch is the heartbeat epoch written, or the stored one when throttled.
	Epoch int64
	// Err is a read, write, or read-back failure. When it is set the label is
	// NOT known to be fresh.
	Err error
}

// Summary describes the store's outcome in one clause, for command output.
func (r beadHeartbeatResult) Summary() string {
	switch {
	case r.Err != nil:
		return "bead label NOT written"
	case r.Throttled:
		return fmt.Sprintf("bead label fresh, next refresh in %s", r.NextRefresh.Round(time.Second))
	default:
		return fmt.Sprintf("bead label %d", r.Epoch)
	}
}

// deaconHeartbeatResult reports both Deacon heartbeat stores separately. They
// fail independently, so a single error return cannot say which one wrote —
// which is exactly how the bead store went silent while the file store carried
// a success message (hq-huln).
type deaconHeartbeatResult struct {
	// FileErr is the result of the heartbeat file stores (deacon.Touch).
	FileErr error
	// Bead is the result of the agent-bead heartbeat label store.
	Bead beadHeartbeatResult
}

// Err returns non-nil unless every store that was supposed to write did write.
// A throttled bead label is not a failure: the label is already fresh.
func (r deaconHeartbeatResult) Err() error {
	switch {
	case r.FileErr != nil && r.Bead.Err != nil:
		return fmt.Errorf("heartbeat file: %v; agent bead: %w", r.FileErr, r.Bead.Err)
	case r.FileErr != nil:
		return fmt.Errorf("heartbeat file: %w", r.FileErr)
	case r.Bead.Err != nil:
		return fmt.Errorf("agent bead: %w", r.Bead.Err)
	}
	return nil
}

// Summary describes what each store did, so the caller's message says which
// stores are actually behind it.
func (r deaconHeartbeatResult) Summary() string {
	file := "file"
	if r.FileErr != nil {
		file = "file NOT written"
	}
	return file + ", " + r.Bead.Summary()
}

var deaconAgentBeadHeartbeatSync = syncDeaconAgentBeadHeartbeat

func syncDeaconHeartbeatStores(townRoot, action string) deaconHeartbeatResult {
	var res deaconHeartbeatResult
	if action != "" {
		res.FileErr = deacon.TouchWithAction(townRoot, action, 0, 0)
	} else {
		res.FileErr = deacon.Touch(townRoot)
	}
	res.Bead = deaconAgentBeadHeartbeatSync(townRoot)
	return res
}

// syncDeaconAgentBeadHeartbeat refreshes the heartbeat:EPOCH label on the
// Deacon's agent bead — the third heartbeat store, read by Witness
// second-order monitoring. Normally await-signal maintains it, but a Deacon
// session that never reaches await-signal (handoffs, long patrols, session
// limits) leaves it stale for hours and triggers false stuck escalations
// (hq-qxl9).
//
// Every outcome is reported to the caller. This is not best-effort work: the
// other two stores are files nothing outside the Deacon's own host reads, so
// when this store is stale the Deacon looks dead no matter what they hold.
func syncDeaconAgentBeadHeartbeat(townRoot string) beadHeartbeatResult {
	agentBead := beads.DeaconBeadIDTown()
	beadsDir := beads.ResolveBeadsDir(townRoot)

	labels, err := getAllAgentLabels(agentBead, beadsDir)
	if err != nil {
		return beadHeartbeatResult{Err: fmt.Errorf("reading %s labels: %w", agentBead, err)}
	}

	if throttled, ok := throttleBeadHeartbeat(labels, time.Now()); ok {
		return throttled
	}

	epoch, err := updateAgentHeartbeat(agentBead, beadsDir)
	if err != nil {
		return beadHeartbeatResult{Epoch: epoch, Err: fmt.Errorf("writing %s heartbeat:%d: %w", agentBead, epoch, err)}
	}

	// Read back. `bd update` exiting 0 is not evidence that the label landed,
	// and this store is the one the Witness reads; a write this command cannot
	// confirm must not be reported as a heartbeat.
	stored, err := getAllAgentLabels(agentBead, beadsDir)
	if err != nil {
		return beadHeartbeatResult{Epoch: epoch, Err: fmt.Errorf("verifying %s heartbeat:%d: %w", agentBead, epoch, err)}
	}
	if err := verifyBeadHeartbeat(stored, epoch); err != nil {
		return beadHeartbeatResult{Epoch: epoch, Err: fmt.Errorf("verifying %s: %w", agentBead, err)}
	}
	return beadHeartbeatResult{Epoch: epoch}
}

// throttleBeadHeartbeat reports whether the stored heartbeat label is young
// enough that a refresh should be skipped, and how long the skip lasts.
func throttleBeadHeartbeat(labels []string, now time.Time) (beadHeartbeatResult, bool) {
	newest, ok := newestHeartbeatEpoch(labels)
	if !ok {
		return beadHeartbeatResult{}, false
	}
	age := now.Sub(time.Unix(newest, 0))
	if age >= deaconBeadHeartbeatSyncThreshold {
		return beadHeartbeatResult{}, false
	}
	return beadHeartbeatResult{
		Throttled:   true,
		NextRefresh: deaconBeadHeartbeatSyncThreshold - age,
		Epoch:       newest,
	}, true
}

// newestHeartbeatEpoch returns the largest parseable heartbeat:EPOCH label.
// Unparseable and duplicate heartbeat labels are tolerated: the bead has
// carried duplicates before (hq-cvk6) and the freshest one is what watchers
// act on.
func newestHeartbeatEpoch(labels []string) (int64, bool) {
	var newest int64
	found := false
	for _, label := range labels {
		epochStr, ok := strings.CutPrefix(label, "heartbeat:")
		if !ok {
			continue
		}
		epoch, err := strconv.ParseInt(epochStr, 10, 64)
		if err != nil {
			continue
		}
		if !found || epoch > newest {
			newest, found = epoch, true
		}
	}
	return newest, found
}

// verifyBeadHeartbeat checks that re-read labels carry the heartbeat just
// written. A label NEWER than want passes: another writer (await-signal) may
// legitimately refresh the same label between the write and the read-back, and
// the invariant watchers depend on is that the store advanced to at least the
// epoch this call wrote.
func verifyBeadHeartbeat(labels []string, want int64) error {
	newest, ok := newestHeartbeatEpoch(labels)
	if !ok {
		return fmt.Errorf("no heartbeat label after writing heartbeat:%d", want)
	}
	if newest < want {
		return fmt.Errorf("heartbeat label is heartbeat:%d after writing heartbeat:%d", newest, want)
	}
	return nil
}
