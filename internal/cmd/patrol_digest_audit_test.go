package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A day's patrol digests are ephemeral, so they are wisps, so deleting them is
// unrecoverable and — until hq-6ewp — unattributable. What goes here is the
// closest thing the town keeps to a narrative of what its agents did that day.

// listingBD installs a `bd` on PATH answering `bd list` with the given JSON and
// logging its argv.
func listingBD(t *testing.T, listJSON string) func() []string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "argv.log")
	dataPath := filepath.Join(binDir, "list.json")

	if err := os.WriteFile(dataPath, []byte(listJSON), 0644); err != nil {
		t.Fatalf("write fake list data: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *list*) cat %q ;;
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

func digestFixture(day time.Time) string {
	ts := day.UTC().Format("2006-01-02T15:04:05Z")
	return fmt.Sprintf(`[
  {"id":"hq-wisp-d1","title":"Digest: mol-deacon-patrol","description":"cycle notes","status":"closed","ephemeral":true,"created_at":%q,"closed_at":%q},
  {"id":"hq-wisp-d2","title":"Digest: mol-witness-patrol","description":"cycle notes","status":"closed","ephemeral":true,"created_at":%q,"closed_at":%q}
]`, ts, ts, ts, ts)
}

func TestDeletePatrolDigestsRecordsThemFirst(t *testing.T) {
	eventsPath := townRootForEvents(t)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	calls := listingBD(t, digestFixture(day))

	deleted, err := deletePatrolDigests(day)
	if err != nil {
		t.Fatalf("deletePatrolDigests() = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want both digests", deleted)
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
		t.Fatal("no wisp_purge record was written; a day of patrol narrative went with no trace")
	}
	first := recorded[0]
	if first.Payload["phase"] != "planned" {
		t.Errorf("first record phase = %v, want planned", first.Payload["phase"])
	}
	if first.Payload["path"] != "gt patrol: digest cleanup" {
		t.Errorf("path = %v, want the digest cleanup named as the deleter", first.Payload["path"])
	}
	named := fmt.Sprint(first.Payload["wisps"])
	for _, want := range []string{"hq-wisp-d1", "Digest: mol-deacon-patrol", "hq-wisp-d2"} {
		if !strings.Contains(named, want) {
			t.Errorf("wisps = %v, want %q named", named, want)
		}
	}
}

func TestDeletePatrolDigestsSkipsWhenItCannotRecord(t *testing.T) {
	// Not a Gas Town workspace, and TestMain has stripped the GT_TOWN_ROOT
	// fallback: there is nowhere durable for the record to land.
	t.Chdir(t.TempDir())
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	calls := listingBD(t, digestFixture(day))

	if _, err := deletePatrolDigests(day); err == nil {
		t.Fatal("deletePatrolDigests() = nil; an unrecordable deletion must fail rather than proceed silently")
	}
	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			t.Errorf("deleted %q with no durable record of what went", c)
		}
	}
}
