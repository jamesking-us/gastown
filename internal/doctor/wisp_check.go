package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// WispGCCheck REPORTS orphaned wisps that are older than a threshold. Wisps are
// ephemeral issues (Wisp: true flag) used for patrol cycles and operational
// workflows that shouldn't accumulate.
//
// This check is REPORT-ONLY and deliberately not fixable. Its Fix used to shell
// out to the wisp GC command, which is prohibited town-wide by mayor-ratified
// standing order (hq-hazr, 2026-09-01; hq-gk8d), in force until hq-hazr ships
// its fix of record. That made `gt doctor --fix` a wisp-GC path wearing a repair
// verb: an operator or a dog running a routine repair reached the unscoped GC
// without ever naming it. The GC is unscoped and deletes closed wisps across the
// WHOLE database — the active patrol molecule's own step ledger, completed dog
// molecules the hq-z70b guard depends on, and rig merge-request beads, which
// permanently orphans cleanup wisps. Wisp deletes are also unrecoverable: the
// wisps tables are in dolt_ignore, so Dolt AS OF cannot bring one back.
//
// Do not re-arm Fix here when the ban lifts without also re-reading hq-hazr's
// fix of record: what lifts is the ban, not the unscoped blast radius.
// See gt-cdi, gt-9ab, gt-34h, hq-qi3a.
type WispGCCheck struct {
	FixableCheck
	threshold     time.Duration
	abandonedRigs map[string]int // rig -> count of abandoned wisps
}

// NewWispGCCheck creates a new wisp GC check with 1 hour threshold.
func NewWispGCCheck() *WispGCCheck {
	return &WispGCCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "wisp-gc",
				CheckDescription: "Detect orphaned wisps (>1h old) — report-only",
				CheckCategory:    CategoryCleanup,
			},
		},
		threshold:     1 * time.Hour,
		abandonedRigs: make(map[string]int),
	}
}

// Run checks for abandoned wisps in each rig.
func (c *WispGCCheck) Run(ctx *CheckContext) *CheckResult {
	c.abandonedRigs = make(map[string]int)

	rigs, err := discoverRigs(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Failed to discover rigs",
			Details: []string{err.Error()},
		}
	}

	if len(rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs configured",
		}
	}

	var details []string
	totalAbandoned := 0

	for _, rigName := range rigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)
		count := c.countAbandonedWisps(rigPath)
		if count > 0 {
			c.abandonedRigs[rigName] = count
			totalAbandoned += count
			details = append(details, fmt.Sprintf("%s: %d abandoned wisp(s)", rigName, count))
		}
	}

	if totalAbandoned > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d abandoned wisp(s) found (>1h old)", totalAbandoned),
			Details: details,
			FixHint: "Report only — wisp GC is prohibited town-wide until hq-hazr's fix of record ships (hq-gk8d). Do not run any wisp GC variant to clear these.",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No abandoned wisps found",
	}
}

// countAbandonedWisps counts wisps older than the threshold in a rig.
// Queries the wisps table via bd mol wisp list (Dolt server is required).
func (c *WispGCCheck) countAbandonedWisps(rigPath string) int {
	// Query wisps table via bd CLI
	cmd := exec.Command("bd", "mol", "wisp", "list", "--json")
	cmd.Dir = rigPath

	output, err := cmd.Output()
	if err != nil {
		// Dolt is the only supported backend — no wisps table means 0 abandoned wisps.
		return 0
	}

	var wisps []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Ephemeral bool   `json:"ephemeral"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(output, &wisps); err != nil {
		return 0
	}

	// Use UTC for cutoff: Dolt stores timestamps in UTC (gt-ty4).
	cutoff := time.Now().UTC().Add(-c.threshold)
	count := 0
	for _, w := range wisps {
		if w.Status == "closed" {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339, w.UpdatedAt)
		if err != nil {
			continue
		}
		if !updatedAt.IsZero() && updatedAt.Before(cutoff) {
			count++
		}
	}

	return count
}

// CanFix returns false: this check is report-only. The repair it used to perform
// is the prohibited wisp GC (see the type comment). Returning false keeps the
// check out of the --fix path entirely rather than relying on Fix's guard.
func (c *WispGCCheck) CanFix() bool {
	return false
}

// Fix performs no repair. It is retained only so that a direct caller that
// bypasses CanFix gets a loud, explicit refusal instead of silently running the
// banned GC. Do not reintroduce the exec here; see the type comment.
func (c *WispGCCheck) Fix(_ *CheckContext) error {
	return errors.New("wisp-gc: refusing to garbage collect wisps — " +
		"wisp GC of every variant is prohibited town-wide until hq-hazr ships " +
		"its fix of record (mayor-ratified 2026-09-01, hq-gk8d). This check is " +
		"report-only; clear abandoned wisps outside the ban, not through gt doctor --fix")
}
