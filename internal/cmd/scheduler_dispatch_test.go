package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func installFakeBD(t *testing.T, script string) {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir fake bd bin: %v", err)
	}
	fakeBD := filepath.Join(binDir, "bd")
	if err := os.WriteFile(fakeBD, []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setupSchedulerScanFailureTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "rig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	installFakeBD(t, `#!/bin/sh
case "$BEADS_DIR" in
  */rig/.beads) echo "scan failed" >&2; exit 7 ;;
  *) printf '[]\n'; exit 0 ;;
esac
`)
	return townRoot
}

func TestDispatchScheduledWorkReportsHeldLock(t *testing.T) {
	townRoot := t.TempDir()
	runtimeDir := filepath.Join(townRoot, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	lockFile := filepath.Join(runtimeDir, "scheduler-dispatch.lock")
	lock := flock.New(lockFile)
	locked, err := lock.TryLock()
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !locked {
		t.Fatal("test could not acquire scheduler dispatch lock")
	}
	t.Cleanup(func() { _ = lock.Unlock() })

	_, err = dispatchScheduledWork(townRoot, "test", 1, false)
	if err == nil {
		t.Fatal("dispatchScheduledWork succeeded with held scheduler lock")
	}
	if !strings.Contains(err.Error(), "scheduler dispatch already in progress") || !strings.Contains(err.Error(), lockFile) {
		t.Fatalf("error = %q, want explicit held lock reason with path", err.Error())
	}
}

func TestValidateDryRunDispatchPlanMarksAllInvalidAsValidation(t *testing.T) {
	townRoot := t.TempDir()
	writeJSONFile(t, filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs: map[string]config.RigEntry{
			"testrig": {BeadsConfig: &config.BeadsConfig{Prefix: "gt"}},
		},
	})

	plan := validateDryRunDispatchPlan(townRoot, capacity.DispatchPlan{
		ToDispatch: []capacity.PendingBead{{ID: "ctx-1", WorkBeadID: "hq-one", TargetRig: "testrig"}},
		Reason:     "ready",
	})

	if len(plan.ToDispatch) != 0 || plan.Skipped != 1 || plan.Reason != "validation" {
		t.Fatalf("validated plan = %+v, want no dispatch, skipped=1, reason=validation", plan)
	}
}

func TestListAllSlingContextRecordsFailsOnPartialScanFailure(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)

	_, err := listAllSlingContextRecords(townRoot)
	if err == nil {
		t.Fatal("partial sling-context scan failure should fail closed")
	}
	if !strings.Contains(err.Error(), "listing sling contexts") || !strings.Contains(err.Error(), filepath.Join("rig", ".beads")) {
		t.Fatalf("error = %q, want explicit context scan failure", err.Error())
	}
}

func TestAreScheduledFailsClosedOnContextScanFailure(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	got := areScheduled([]string{"gt-one", "gt-two"})
	if !got["gt-one"] || !got["gt-two"] {
		t.Fatalf("areScheduled on scan failure = %+v, want all requested IDs marked scheduled", got)
	}
}

func TestRunSchedulerClearFailsOnContextScanFailure(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	oldClearBead := schedulerClearBead
	schedulerClearBead = ""
	t.Cleanup(func() { schedulerClearBead = oldClearBead })

	err = runSchedulerClear(nil, nil)
	if err == nil {
		t.Fatal("scheduler clear succeeded with incomplete context scan")
	}
	if !strings.Contains(err.Error(), "listing sling contexts") {
		t.Fatalf("error = %q, want sling context scan failure", err.Error())
	}
}

// setupSchedulerPartialScanTown builds a town where the rig context dir is
// unresolvable (mirroring a stub .beads naming a nonexistent Dolt database)
// while the town dir still holds a real sling context.
func setupSchedulerPartialScanTown(t *testing.T, workBeadID string) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "rig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	fields := &capacity.SlingContextFields{
		WorkBeadID: workBeadID,
		TargetRig:  "rig",
		EnqueuedAt: "2026-09-05T00:00:00Z",
	}
	contextJSON := fmt.Sprintf(`[{"id":"ctx-healthy","title":"sling-context: %s","status":"open","description":%s}]`,
		workBeadID, strconv.Quote(beads.FormatSlingContextDescription(fields)))
	// bd is invoked with --allow-stale prepended, so match on the whole
	// argument list, not $1. Only the context listing fails in the rig dir;
	// work-bead lookups stay healthy so the test isolates the walk's
	// tolerance rather than bd routing.
	installFakeBD(t, `#!/bin/sh
case "$*" in
  *query*)
    case "$BEADS_DIR" in
      */rig/.beads) echo "issue not found" >&2; exit 1 ;;
    esac
    printf '%s\n' '`+contextJSON+`'
    ;;
  *show*) printf '%s\n' '[{"id":"`+workBeadID+`","title":"work","status":"open","labels":[]}]' ;;
  *)      printf '[]\n' ;;
esac
exit 0
`)
	return townRoot
}

func TestScanAllSlingContextRecordsSkipsUnresolvableContext(t *testing.T) {
	townRoot := setupSchedulerPartialScanTown(t, "gt-work")

	records, failures, err := scanAllSlingContextRecords(townRoot)
	if err != nil {
		t.Fatalf("scanAllSlingContextRecords aborted on one unresolvable context: %v", err)
	}
	if len(records) != 1 || records[0].issue.ID != "ctx-healthy" {
		t.Fatalf("records = %+v, want the one resolvable context still listed", records)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %+v, want exactly the unresolvable context reported", failures)
	}
	if !strings.Contains(failures[0].beadsDir, filepath.Join("rig", ".beads")) {
		t.Fatalf("failure beadsDir = %q, want the unresolvable context named", failures[0].beadsDir)
	}
	if !strings.Contains(failures[0].Error(), "issue not found") {
		t.Fatalf("failure = %q, want the skip reason carried", failures[0].Error())
	}
}

func TestScanAllSlingContextRecordsFailsWhenEveryContextUnresolvable(t *testing.T) {
	townRoot := t.TempDir()
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "rig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	installFakeBD(t, "#!/bin/sh\necho 'issue not found' >&2\nexit 1\n")

	_, failures, err := scanAllSlingContextRecords(townRoot)
	if err == nil {
		t.Fatal("total context scan failure should be fatal, not an empty listing")
	}
	if len(failures) != 2 {
		t.Fatalf("failures = %+v, want every scanned context reported", failures)
	}
}

func TestListScheduledBeadsSkipsUnresolvableContextAndListsRest(t *testing.T) {
	townRoot := setupSchedulerPartialScanTown(t, "gt-work")

	scheduled, failures, err := listScheduledBeads(townRoot)
	if err != nil {
		t.Fatalf("listScheduledBeads aborted on one unresolvable context: %v", err)
	}
	if len(scheduled) != 1 || scheduled[0].ID != "gt-work" {
		t.Fatalf("scheduled = %+v, want the resolvable context's work bead listed", scheduled)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].beadsDir, filepath.Join("rig", ".beads")) {
		t.Fatalf("failures = %+v, want the unresolvable context named and skipped", failures)
	}
}

func TestSkippedContextInfosNamesDirAndReason(t *testing.T) {
	infos := skippedContextInfos([]slingContextScanFailure{
		{beadsDir: "/gt/forkrig/.beads", err: errors.New("issue not found")},
	})
	if len(infos) != 1 || infos[0].BeadsDir != "/gt/forkrig/.beads" || infos[0].Error != "issue not found" {
		t.Fatalf("skippedContextInfos = %+v, want dir and reason preserved for --json consumers", infos)
	}
}

// TestRunSchedulerStatusSkipsUnresolvableContextWithZeroExit is the regression
// the witness saw: `gt scheduler status` is the first step of every SLOT_OPEN
// dispatch trigger, and one unresolvable context used to exit it 1 town-wide.
func TestRunSchedulerStatusSkipsUnresolvableContextWithZeroExit(t *testing.T) {
	townRoot := setupSchedulerPartialScanTown(t, "gt-work")
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	oldJSON := schedulerStatusJSON
	schedulerStatusJSON = false
	t.Cleanup(func() { schedulerStatusJSON = oldJSON })

	var statusErr error
	out := captureStdout(t, func() { statusErr = runSchedulerStatus(nil, nil) })
	if statusErr != nil {
		t.Fatalf("scheduler status failed on one unresolvable context: %v", statusErr)
	}
	if !strings.Contains(out, filepath.Join("rig", ".beads")) {
		t.Fatalf("status output = %q, want the skipped context named", out)
	}
	if !strings.Contains(out, "unresolvable sling context") {
		t.Fatalf("status output = %q, want the skip reported", out)
	}
	if !strings.Contains(out, "Scheduled: 1 total") {
		t.Fatalf("status output = %q, want the resolvable context still counted", out)
	}
}
