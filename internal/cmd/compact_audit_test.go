package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// compact.go:393 used to carry the comment "safe: Dolt AS OF preserves
// history". It was false — wisps and wisp_% are in dolt_ignore, so the one
// table compaction is authorised to delete from is the one table with no
// history — and the patrol formula reproduced the assurance verbatim to every
// deacon cycle (hq-6ewp). These tests hold the replacement: a durable record
// before the delete, and no delete without one.

func compactWisp(id, title string) *compactIssue {
	w := &compactIssue{}
	w.ID = id
	w.Title = title
	w.WispType = "patrol"
	return w
}

func TestDeleteWispRecordsTheDeletionFirst(t *testing.T) {
	eventsPath := townRootForEvents(t)
	calls := recordingBD(t, "[]")

	result := &compactResult{}
	deleteWisp(beads.New(t.TempDir()), compactWisp("hq-wisp-aaa", "deacon patrol cycle 4"),
		"TTL expired", result, compactAudit{actor: "hq/deacon", db: "hq"}, compactOptions{})

	if len(result.Deleted) != 1 {
		t.Fatalf("Deleted = %v, want the wisp deleted", result.Deleted)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("Errors = %v", result.Errors)
	}

	var sawDelete bool
	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatal("no bd delete was issued")
	}

	recorded := readEventTypes(t, eventsPath)
	if len(recorded) == 0 {
		t.Fatal("no wisp_purge record was written; the deletion is unauditable")
	}
	first := recorded[0]
	if first.Payload["phase"] != "planned" {
		t.Errorf("first record phase = %v, want planned — the record has to precede the delete", first.Payload["phase"])
	}
	if first.Payload["path"] != "gt compact: ttl delete" {
		t.Errorf("path = %v, want compaction named as the deleter", first.Payload["path"])
	}
	if first.Actor != "hq/deacon" {
		t.Errorf("actor = %q, want the compacting agent", first.Actor)
	}
	// The title is the point: an id names a row that no longer exists anywhere.
	named := fmt.Sprint(first.Payload["wisps"])
	for _, want := range []string{"hq-wisp-aaa", "deacon patrol cycle 4"} {
		if !strings.Contains(named, want) {
			t.Errorf("wisps = %v, want %q named", named, want)
		}
	}
	if first.Payload["reason"] != "TTL expired" {
		t.Errorf("reason = %v, want why the wisp was eligible", first.Payload["reason"])
	}
}

func TestDeleteWispSkipsWhenTheDeletionCannotBeRecorded(t *testing.T) {
	// Not a Gas Town workspace, and TestMain has stripped the GT_TOWN_ROOT
	// fallback: there is nowhere durable for the record to land.
	t.Chdir(t.TempDir())
	calls := recordingBD(t, "[]")

	result := &compactResult{}
	deleteWisp(beads.New(t.TempDir()), compactWisp("hq-wisp-aaa", "deacon patrol cycle 4"),
		"TTL expired", result, compactAudit{actor: "hq/deacon", db: "hq"}, compactOptions{})

	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			t.Errorf("deleted %q with no durable record of what went", c)
		}
	}
	if len(result.Deleted) != 0 {
		t.Errorf("Deleted = %v, want nothing deleted", result.Deleted)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "could not record") {
		t.Errorf("Errors = %v, want the skip reported rather than swallowed", result.Errors)
	}
}

// A dry run reports what it would do and touches nothing, including the log.
func TestDeleteWispDryRunRecordsNothing(t *testing.T) {
	eventsPath := townRootForEvents(t)
	calls := recordingBD(t, "[]")

	result := &compactResult{}
	deleteWisp(beads.New(t.TempDir()), compactWisp("hq-wisp-aaa", "cycle"),
		"TTL expired", result, compactAudit{actor: "hq/deacon", db: "hq"}, compactOptions{DryRun: true})

	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			t.Errorf("dry run issued %q", c)
		}
	}
	if len(readEventTypes(t, eventsPath)) != 0 {
		t.Error("dry run recorded a deletion that did not happen")
	}
}
