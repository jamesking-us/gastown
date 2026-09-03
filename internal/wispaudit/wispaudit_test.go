package wispaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// townRoot makes cwd a Gas Town workspace so a record has somewhere to land,
// and returns the path of the events file it should land in.
func townRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	t.Chdir(root)
	return filepath.Join(root, ".events.jsonl")
}

type record struct {
	Timestamp string                 `json:"ts"`
	Type      string                 `json:"type"`
	Actor     string                 `json:"actor"`
	Payload   map[string]interface{} `json:"payload"`
}

func readRecords(t *testing.T, path string) []record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("parsing record %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// The record has to answer, on its own, the question the wisps table cannot:
// what went, what it was called, who took it, by which path, and when.
func TestPlanRecordsWhatWentAndWhoTookIt(t *testing.T) {
	path := townRoot(t)

	err := Plan("ccm/polecats/nuka", PathReaper, "closed_at<2026-08-26T00:00:00Z", "hq",
		[]Wisp{
			{ID: "hq-wisp-aaa", Title: "deacon patrol cycle"},
			{ID: "hq-wisp-bbb", Title: "witness patrol cycle"},
		}, map[string]interface{}{"predicted": true})
	if err != nil {
		t.Fatalf("Plan() = %v, want the record to land", err)
	}

	got := readRecords(t, path)
	if len(got) != 1 {
		t.Fatalf("wrote %d records, want exactly 1", len(got))
	}
	r := got[0]

	if r.Type != "wisp_purge" {
		t.Errorf("type = %q, want wisp_purge — one type keeps this one log", r.Type)
	}
	if r.Actor != "ccm/polecats/nuka" {
		t.Errorf("actor = %q, want the deleting agent", r.Actor)
	}
	if r.Timestamp == "" {
		t.Error("record carries no timestamp")
	}
	if r.Payload["phase"] != "planned" {
		t.Errorf("phase = %v, want planned", r.Payload["phase"])
	}
	if r.Payload["path"] != PathReaper {
		t.Errorf("path = %v, want %q — a record that cannot say which deleter acted is why the 09-01 loss had no actor", r.Payload["path"], PathReaper)
	}
	if r.Payload["db"] != "hq" {
		t.Errorf("db = %v, want hq", r.Payload["db"])
	}
	if r.Payload["scope"] != "closed_at<2026-08-26T00:00:00Z" {
		t.Errorf("scope = %v, want the cutoff the delete was bounded by", r.Payload["scope"])
	}
	if r.Payload["predicted"] != true {
		t.Errorf("extra fields were dropped: %v", r.Payload)
	}

	wisps, ok := r.Payload["wisps"].([]interface{})
	if !ok || len(wisps) != 2 {
		t.Fatalf("wisps = %v, want both deleted wisps named", r.Payload["wisps"])
	}
	first, _ := wisps[0].(map[string]interface{})
	if first["id"] != "hq-wisp-aaa" || first["title"] != "deacon patrol cycle" {
		t.Errorf("first wisp = %v, want id and title — an id alone names a row that no longer exists anywhere", first)
	}
}

// The whole point of the ordering rule: a caller that cannot record must be
// able to tell, so it can decline to delete.
func TestPlanFailsWhenThereIsNowhereDurableToWrite(t *testing.T) {
	// A directory that is not a Gas Town workspace, in a process whose
	// GT_TOWN_ROOT fallback has been stripped by TestMain.
	t.Chdir(t.TempDir())

	err := Plan("ccm/witness", PathCompaction, "ttl", "ccm", []Wisp{{ID: "cl-wisp-x"}}, nil)
	if err == nil {
		t.Fatal("Plan() = nil with no workspace in scope; a caller would delete believing it had recorded")
	}
}

// Completed is advisory. It must not be able to stop anything, but it must
// still say what actually went.
func TestCompletedRecordsTheOutcome(t *testing.T) {
	path := townRoot(t)

	if err := Completed("ccm/witness", PathDonePurge, "molecule:cl-wisp-root", "ccm",
		[]Wisp{{ID: "cl-wisp-a"}}, []string{"cl-wisp-b: boom"}, nil); err != nil {
		t.Fatalf("Completed() = %v", err)
	}

	got := readRecords(t, path)
	if len(got) != 1 {
		t.Fatalf("wrote %d records, want 1", len(got))
	}
	if got[0].Payload["phase"] != "completed" {
		t.Errorf("phase = %v, want completed", got[0].Payload["phase"])
	}
	failed, ok := got[0].Payload["failed"].([]interface{})
	if !ok || len(failed) != 1 {
		t.Errorf("failed = %v, want the delete that did not happen named", got[0].Payload["failed"])
	}
}

// A wisp with no known title is recorded by id rather than not recorded: the
// paths that can only predict their delete set still have to name it.
func TestWispsWithoutTitlesAreStillRecorded(t *testing.T) {
	path := townRoot(t)

	if err := Plan("gt", PathDoltSyncGC, "database", "ccm", WispsFromIDs([]string{"cl-wisp-1", "cl-wisp-2"}), nil); err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	got := readRecords(t, path)
	wisps, ok := got[0].Payload["wisps"].([]interface{})
	if !ok || len(wisps) != 2 {
		t.Fatalf("wisps = %v, want both ids named", got[0].Payload["wisps"])
	}
	first, _ := wisps[0].(map[string]interface{})
	if first["id"] != "cl-wisp-1" {
		t.Errorf("first wisp = %v, want the id", first)
	}
	if _, present := first["title"]; present {
		t.Errorf("first wisp = %v, want no title key rather than an empty one", first)
	}
	if got[0].Payload["count"] != float64(2) {
		t.Errorf("count = %v, want 2", got[0].Payload["count"])
	}
}

func TestIDsRoundTrip(t *testing.T) {
	ids := []string{"a", "b", "c"}
	if got := IDs(WispsFromIDs(ids)); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("IDs(WispsFromIDs(%v)) = %v", ids, got)
	}
	if got := IDs(nil); got != nil {
		t.Errorf("IDs(nil) = %v, want nil", got)
	}
}

// The packages below internal/cmd have no address detection, so the record
// would otherwise say "unknown" for every deletion the daemon makes.
func TestActorPrefersTheSessionRole(t *testing.T) {
	t.Setenv("GT_ROLE", "gastown/polecats/nux")
	if got := Actor("daemon/wisp_reaper"); got != "gastown/polecats/nux" {
		t.Errorf("Actor() = %q, want the session role", got)
	}
	t.Setenv("GT_ROLE", "")
	if got := Actor("daemon/wisp_reaper"); got != "daemon/wisp_reaper" {
		t.Errorf("Actor() = %q, want the fallback when there is no session role", got)
	}
}
