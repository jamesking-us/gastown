package doltserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-push GC removes closed ephemeral beads from every rig database before
// syncing to DoltHub. Those are wisps, and wisps are in dolt_ignore, so what
// this removes is in no Dolt commit and no AS OF reads it back (hq-6ewp). These
// tests hold the record it now leaves behind.

// gcTown builds a town with one rig database and makes it the working
// directory, so the audit record has somewhere durable to land. It returns the
// town root and the events file path.
func gcTown(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	beadsDir := filepath.Join(root, "ccm", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	t.Chdir(root)
	return root, filepath.Join(root, ".events.jsonl")
}

// fakeBD installs a `bd` on PATH that answers the wisp query with queryJSON,
// answers purge with a JSON count, and logs its argv. The returned func reads
// the log back.
func fakeBD(t *testing.T, queryJSON string) func() []string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "argv.log")
	dataPath := filepath.Join(binDir, "wisps.json")

	if err := os.WriteFile(dataPath, []byte(queryJSON), 0644); err != nil {
		t.Fatalf("write fake wisp data: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *query*) cat %q ;;
  *purge*) echo '{"purged_count": 2}' ;;
  *) : ;;
esac
`, logPath, dataPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		var calls []string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				calls = append(calls, line)
			}
		}
		return calls
	}
}

const gcWispJSON = `[
  {"id":"cl-wisp-aaa","title":"mol-polecat-work step 1","status":"closed","ephemeral":true},
  {"id":"cl-wisp-bbb","title":"mol-polecat-work step 2","status":"closed","ephemeral":true},
  {"id":"cl-wisp-live","title":"still working","status":"open","ephemeral":true}
]`

func TestPurgeClosedEphemeralsRecordsWhatItWillRemove(t *testing.T) {
	townRoot, eventsPath := gcTown(t)
	calls := fakeBD(t, gcWispJSON)

	purged, err := PurgeClosedEphemerals(townRoot, "ccm", "gt dolt sync --gc: pre-push purge", false)
	if err != nil {
		t.Fatalf("PurgeClosedEphemerals() = %v", err)
	}
	if purged != 2 {
		t.Fatalf("purged = %d, want the count bd reported", purged)
	}

	var sawPurge bool
	for _, c := range calls() {
		if strings.Contains(c, "purge") {
			sawPurge = true
		}
	}
	if !sawPurge {
		t.Fatal("no bd purge was issued")
	}

	planned := gcPlannedRecord(t, eventsPath)
	if planned == nil {
		t.Fatal("no planned wisp_purge record was written; bd purge reports a count and never the ids, so this is the only thing that names them")
	}
	if planned["path"] != "gt dolt sync --gc: pre-push purge" {
		t.Errorf("path = %v, want the GC named as the deleter", planned["path"])
	}
	if planned["db"] != "ccm" {
		t.Errorf("db = %v, want ccm", planned["db"])
	}
	if planned["predicted"] != true {
		t.Error("the record must say it is a prediction: bd applies its own definition of closed-ephemeral")
	}
	named := fmt.Sprint(planned["wisps"])
	for _, want := range []string{"cl-wisp-aaa", "mol-polecat-work step 1", "cl-wisp-bbb"} {
		if !strings.Contains(named, want) {
			t.Errorf("wisps = %v, want %q named", named, want)
		}
	}
	// An open wisp is not in the purge set, so naming it would misreport what
	// went — the record is read afterwards as the list of what was lost.
	if strings.Contains(named, "cl-wisp-live") {
		t.Errorf("wisps = %v names an open wisp that bd purge will not remove", named)
	}
}

func TestPurgeClosedEphemeralsDoesNotPurgeWhenItCannotRecord(t *testing.T) {
	// A town root that exists on disk for the beads-dir lookup, but a working
	// directory that is not inside any town — and TestMain has stripped the
	// GT_TOWN_ROOT fallback, so there is nowhere durable to record.
	townRoot, _ := gcTown(t)
	t.Chdir(t.TempDir())
	calls := fakeBD(t, gcWispJSON)

	_, err := PurgeClosedEphemerals(townRoot, "ccm", "gt maintain: pre-push purge", false)
	if err == nil {
		t.Fatal("PurgeClosedEphemerals() = nil; an unrecordable purge must fail rather than proceed silently")
	}
	if !strings.Contains(err.Error(), "could not be recorded") {
		t.Errorf("error = %v, want it to name the unwritable record", err)
	}
	for _, c := range calls() {
		if strings.Contains(c, "purge") {
			t.Errorf("ran %q with no durable record of what went", c)
		}
	}
}

// A purge that did not happen must not leave a record saying it did. The
// planned record stands either way — that is what writing it first buys — but a
// "completed" record naming wisps still sitting in the database would send a
// later investigation looking for rows that were never removed.
func TestPurgeClosedEphemeralsWritesNoCompletionWhenThePurgeFails(t *testing.T) {
	townRoot, eventsPath := gcTown(t)
	failingBD(t, gcWispJSON)

	if _, err := PurgeClosedEphemerals(townRoot, "ccm", "gt maintain: pre-push purge", false); err == nil {
		t.Fatal("PurgeClosedEphemerals() = nil, want the bd failure reported")
	}

	if planned := gcPlannedRecord(t, eventsPath); planned == nil {
		t.Error("no planned record; the set about to be purged must be named even when the purge then fails")
	}
	if completed := gcRecordWithPhase(t, eventsPath, "completed"); completed != nil {
		t.Errorf("wrote a completed record for a purge that failed: %v", completed)
	}
}

// A dry run decides nothing, so it records nothing.
func TestPurgeClosedEphemeralsDryRunRecordsNothing(t *testing.T) {
	townRoot, eventsPath := gcTown(t)
	fakeBD(t, gcWispJSON)

	if _, err := PurgeClosedEphemerals(townRoot, "ccm", "gt dolt sync --gc: pre-push purge", true); err != nil {
		t.Fatalf("PurgeClosedEphemerals(dryRun) = %v", err)
	}
	if _, err := os.Stat(eventsPath); err == nil {
		t.Error("dry run recorded a deletion that did not happen")
	}
}

// failingBD installs a `bd` that answers the wisp query but fails the purge.
func failingBD(t *testing.T, queryJSON string) {
	t.Helper()
	binDir := t.TempDir()
	dataPath := filepath.Join(binDir, "wisps.json")
	if err := os.WriteFile(dataPath, []byte(queryJSON), 0644); err != nil {
		t.Fatalf("write fake wisp data: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *query*) cat %q ;;
  *purge*) echo 'dolt: connection refused' >&2; exit 1 ;;
  *) : ;;
esac
`, dataPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gcPlannedRecord(t *testing.T, path string) map[string]interface{} {
	return gcRecordWithPhase(t, path, "planned")
}

func gcRecordWithPhase(t *testing.T, path, phase string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			Type    string                 `json:"type"`
			Payload map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parsing record %q: %v", line, err)
		}
		if e.Type == "wisp_purge" && e.Payload["phase"] == phase {
			return e.Payload
		}
	}
	return nil
}
