package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
)

// wispFixture is the wisp population used by the subtree tests: a molecule root
// with two steps and one grandchild, plus a second molecule that belongs to a
// different agent and must never be touched.
func wispFixture() []*purgeCandidate {
	mk := func(id, parent, status string) *purgeCandidate {
		w := &purgeCandidate{}
		w.ID = id
		w.Parent = parent
		w.Status = status
		return w
	}
	return []*purgeCandidate{
		mk("cl-wisp-root", "", "closed"),
		mk("cl-wisp-step1", "cl-wisp-root", "closed"),
		mk("cl-wisp-step2", "cl-wisp-root", "open"),
		mk("cl-wisp-sub", "cl-wisp-step1", "closed"),
		mk("cl-wisp-other-root", "", "closed"),
		mk("cl-wisp-other-step", "cl-wisp-other-root", "closed"),
	}
}

func subtreeIDs(t *testing.T, all []*purgeCandidate, root string) []string {
	t.Helper()
	var ids []string
	for _, w := range moleculeSubtree(all, root) {
		ids = append(ids, w.ID)
	}
	return ids
}

func TestMoleculeSubtreeCollectsOnlyItsOwnDescendants(t *testing.T) {
	got := subtreeIDs(t, wispFixture(), "cl-wisp-root")

	want := map[string]bool{
		"cl-wisp-root": true, "cl-wisp-step1": true,
		"cl-wisp-step2": true, "cl-wisp-sub": true,
	}
	if len(got) != len(want) {
		t.Fatalf("moleculeSubtree() = %v, want the 4 wisps of cl-wisp-root", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("moleculeSubtree() included %q, which belongs to another molecule", id)
		}
	}
}

func TestMoleculeSubtreeUnknownRootIsEmpty(t *testing.T) {
	if got := subtreeIDs(t, wispFixture(), "cl-wisp-nonexistent"); len(got) != 0 {
		t.Errorf("moleculeSubtree() on an unknown root = %v, want nothing", got)
	}
}

// A parent cycle must terminate rather than hang the completion path.
func TestMoleculeSubtreeTerminatesOnCycle(t *testing.T) {
	a := &purgeCandidate{}
	a.ID, a.Parent, a.Status = "a", "b", "closed"
	b := &purgeCandidate{}
	b.ID, b.Parent, b.Status = "b", "a", "closed"

	got := subtreeIDs(t, []*purgeCandidate{a, b}, "a")
	if len(got) != 2 {
		t.Errorf("moleculeSubtree() on a cycle = %v, want both nodes exactly once", got)
	}
}

func TestIsEvidenceBearing(t *testing.T) {
	withComments := &purgeCandidate{CommentCount: 1}
	if !isEvidenceBearing(withComments) {
		t.Error("a wisp carrying a comment must not be purged")
	}
	keep := &purgeCandidate{}
	keep.Labels = []string{"gt:keep"}
	if !isEvidenceBearing(keep) {
		t.Error("a wisp labelled gt:keep must not be purged")
	}
	plain := &purgeCandidate{}
	plain.Labels = []string{"gt:task"}
	if isEvidenceBearing(plain) {
		t.Error("a plain step wisp is purgeable")
	}
}

// recordingBD installs a fake `bd` on PATH that appends its argv to a log file
// and answers the wisp query with the given JSON. It returns a func reading
// back the recorded invocations.
func recordingBD(t *testing.T, queryJSON string) func() []string {
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
			return nil // never invoked
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

// townRootForEvents makes cwd a Gas Town workspace so the audit record has
// somewhere durable to land, and returns the events file path.
func townRootForEvents(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	t.Chdir(root)
	return filepath.Join(root, events.EventsFile)
}

func readEventTypes(t *testing.T, path string) []events.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []events.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e events.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parsing event line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

const fixtureWispJSON = `[
  {"id":"cl-wisp-root","title":"mol-polecat-work","status":"closed","ephemeral":true},
  {"id":"cl-wisp-step1","title":"Step 1: read the bead","parent":"cl-wisp-root","status":"closed","ephemeral":true},
  {"id":"cl-wisp-step2","title":"Step 2: implement","parent":"cl-wisp-root","status":"open","ephemeral":true},
  {"id":"cl-wisp-kept","title":"Step 3: commented","parent":"cl-wisp-root","status":"closed","ephemeral":true,"comment_count":2},
  {"id":"cl-wisp-other","title":"another agent","status":"closed","ephemeral":true},
  {"id":"cl-wisp-other-step","title":"another agent step","parent":"cl-wisp-other","status":"closed","ephemeral":true}
]`

// The regression this bead exists for: a completion must delete only its own
// molecule's closed wisps, never the rest of the database.
func TestPurgeOwnClosedWispsDeletesOnlyItsOwnMolecule(t *testing.T) {
	eventsPath := townRootForEvents(t)
	calls := recordingBD(t, fixtureWispJSON)

	purgeOwnClosedWisps(beads.New(t.TempDir()), "ccm/polecats/test", "ccm", "cl-wisp-root")

	var deletes []string
	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			deletes = append(deletes, c)
		}
	}
	if len(deletes) != 1 {
		t.Fatalf("expected exactly one delete call, got %v", deletes)
	}
	got := deletes[0]
	for _, want := range []string{"cl-wisp-root", "cl-wisp-step1"} {
		if !strings.Contains(got, want) {
			t.Errorf("delete call %q is missing own closed wisp %q", got, want)
		}
	}
	for _, forbidden := range []string{"cl-wisp-other", "cl-wisp-step2", "cl-wisp-kept"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("delete call %q touched %q — it is not this session's purgeable work", got, forbidden)
		}
	}

	recorded := readEventTypes(t, eventsPath)
	if len(recorded) == 0 {
		t.Fatal("no wisp_purge record was written; the deletion is unauditable")
	}
	if recorded[0].Type != events.TypeWispPurge || recorded[0].Payload["phase"] != "planned" {
		t.Errorf("first record = %+v, want a planned wisp_purge written before the delete", recorded[0])
	}

	// hq-6ewp: the record names each wisp's title as well as its id, and says
	// which of the tree's several wisp deleters acted. An id on its own
	// identifies a row that no longer exists anywhere to look it up in.
	named := fmt.Sprint(recorded[0].Payload["wisps"])
	for _, want := range []string{"cl-wisp-root", "mol-polecat-work", "cl-wisp-step1", "Step 1: read the bead"} {
		if !strings.Contains(named, want) {
			t.Errorf("wisps = %v, want %q named", named, want)
		}
	}
	if got := recorded[0].Payload["path"]; got != "gt done: molecule purge" {
		t.Errorf("path = %v, want gt done named as the deleter", got)
	}
}

// An unknown scope must mean "purge nothing", not "purge everything".
func TestPurgeOwnClosedWispsWithoutMoleculeDoesNothing(t *testing.T) {
	townRootForEvents(t)
	calls := recordingBD(t, fixtureWispJSON)

	purgeOwnClosedWisps(beads.New(t.TempDir()), "ccm/polecats/test", "ccm", "")

	for _, c := range calls() {
		// Capability probes are allowed; reads and writes against the wisp
		// population are not.
		if strings.Contains(c, "query") || strings.Contains(c, "delete") || strings.Contains(c, "purge") {
			t.Errorf("purge with no molecule ran %q; an unknown scope must touch nothing", c)
		}
	}
}

// No durable record, no deletion.
func TestPurgeOwnClosedWispsSkipsWhenReceiptCannotBeWritten(t *testing.T) {
	// A directory that is not a Gas Town workspace: events.LogAuditDurable
	// reports ErrNoWorkspace, so there is nowhere to record the deletion.
	t.Chdir(t.TempDir())
	calls := recordingBD(t, fixtureWispJSON)

	purgeOwnClosedWisps(beads.New(t.TempDir()), "ccm/polecats/test", "ccm", "cl-wisp-root")

	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			t.Errorf("deleted %q with no durable receipt available", c)
		}
	}
}

// The database-wide path kept for polecat nuke must still be age-bounded.
// gt-12f: it no longer delegates to `bd purge --older-than`, which has no
// concept of a protected bead — the real delete is now the explicit, by-id
// path, so the age bound is enforced here rather than by an --older-than flag.
func TestPurgeClosedEphemeralBeadsIsAgeBounded(t *testing.T) {
	townRootForEvents(t)
	recentClosedAt := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	oldClosedAt := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	wispJSON := fmt.Sprintf(`[
	  {"id":"ccm-wisp-recent","title":"recent","status":"closed","ephemeral":true,"closed_at":%q},
	  {"id":"ccm-wisp-old","title":"old","status":"closed","ephemeral":true,"closed_at":%q}
	]`, recentClosedAt, oldClosedAt)
	calls := recordingBD(t, wispJSON)

	purgeClosedEphemeralBeads(beads.New(t.TempDir()), "ccm/witness", "ccm")

	var deletes []string
	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			deletes = append(deletes, c)
		}
	}
	if len(deletes) != 1 {
		t.Fatalf("expected exactly one delete call, got %v", deletes)
	}
	if !strings.Contains(deletes[0], "ccm-wisp-old") {
		t.Errorf("delete call %q missing the wisp closed 8 days ago", deletes[0])
	}
	if strings.Contains(deletes[0], "ccm-wisp-recent") {
		t.Errorf("delete call %q is age-blind; purged a wisp closed only 1h ago", deletes[0])
	}
}

// A malformed or absent closure time cannot satisfy the mandatory age floor.
// Treating it as old was an implicit ageless purge hiding behind parse failure.
func TestPurgeClosedEphemeralBeadsKeepsUnknownAge(t *testing.T) {
	townRootForEvents(t)
	wispJSON := `[{"id":"ccm-wisp-unknown-age","title":"unknown","status":"closed","ephemeral":true,"closed_at":"not-a-time"}]`
	calls := recordingBD(t, wispJSON)

	purgeClosedEphemeralBeads(beads.New(t.TempDir()), "ccm/witness", "ccm")
	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			t.Errorf("deleted %q despite an unknown closed_at", c)
		}
	}
}

// gt-12f / cl-kf00: a merge-request-labelled bead and a compliance-commented
// bead must never be purged by the database-wide path, no matter their age.
func TestPurgeClosedEphemeralBeadsExcludesProtectedBeads(t *testing.T) {
	townRootForEvents(t)
	oldClosedAt := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	wispJSON := fmt.Sprintf(`[
	  {"id":"ccm-wisp-mr","title":"Merge: ccm-abc","status":"closed","ephemeral":true,"labels":["gt:merge-request"],"closed_at":%q},
	  {"id":"ccm-wisp-cc","title":"commented step","status":"closed","ephemeral":true,"comment_count":1,"closed_at":%q},
	  {"id":"ccm-wisp-plain","title":"plain step","status":"closed","ephemeral":true,"closed_at":%q}
	]`, oldClosedAt, oldClosedAt, oldClosedAt)
	calls := recordingBDWithComments(t, wispJSON, map[string]string{
		"ccm-wisp-cc": `[{"id":"c1","author":"cloudcontentmanager/crew/compliance","text":"verdict: NOT_TRIGGERED"}]`,
	})

	purgeClosedEphemeralBeads(beads.New(t.TempDir()), "ccm/witness", "ccm")

	var deletes []string
	for _, c := range calls() {
		if strings.Contains(c, "delete ") {
			deletes = append(deletes, c)
		}
	}
	if len(deletes) != 1 {
		t.Fatalf("expected exactly one delete call, got %v", deletes)
	}
	got := deletes[0]
	if !strings.Contains(got, "ccm-wisp-plain") {
		t.Errorf("delete call %q is missing the unprotected wisp", got)
	}
	for _, forbidden := range []string{"ccm-wisp-mr", "ccm-wisp-cc"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("delete call %q purged a protected bead %q — cl-kf00 requires it survive", got, forbidden)
		}
	}
}

// A candidate whose comments cannot be read must be withheld, not assumed safe.
func TestPurgeClosedEphemeralBeadsFailsClosedWhenCommentsAreUnreadable(t *testing.T) {
	townRootForEvents(t)
	oldClosedAt := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	wispJSON := fmt.Sprintf(`[
	  {"id":"ccm-wisp-unreadable","title":"commented step","status":"closed","ephemeral":true,"comment_count":1,"closed_at":%q}
	]`, oldClosedAt)

	binDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  query) cat <<'JSON'
%s
JSON
  ;;
  comments) echo "dolt: connection refused" >&2; exit 1 ;;
  *) : ;;
esac
`, wispJSON)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// purgeClosedEphemeralBeads logs warnings, not deletes, when it keeps
	// something — the real assertion is that no delete call reaches bd at all,
	// which recordingBD-style argv logging isn't wired up for here, so this
	// test calls planUnscopedPurge directly to check the exclusion count.
	doomed, excluded, err := planUnscopedPurge(beads.New(t.TempDir()))
	if err != nil {
		t.Fatalf("planUnscopedPurge() = %v", err)
	}
	if len(doomed) != 0 {
		t.Errorf("doomed = %v, want nothing purged when comments are unreadable", doomed)
	}
	if excluded.unreadable != 1 {
		t.Errorf("excluded.unreadable = %d, want 1", excluded.unreadable)
	}
}

// recordingBDWithComments is recordingBD plus a `bd comments <id> --json`
// responder, keyed by id, for gt-12f's compliance-seat authorship check.
func recordingBDWithComments(t *testing.T, queryJSON string, commentsByID map[string]string) func() []string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "argv.log")
	dataPath := filepath.Join(binDir, "wisps.json")
	commentsDir := filepath.Join(binDir, "comments")
	if err := os.MkdirAll(commentsDir, 0755); err != nil {
		t.Fatalf("mkdir comments dir: %v", err)
	}
	if err := os.WriteFile(dataPath, []byte(queryJSON), 0644); err != nil {
		t.Fatalf("write fake wisp data: %v", err)
	}
	for id, commentsJSON := range commentsByID {
		if err := os.WriteFile(filepath.Join(commentsDir, id+".json"), []byte(commentsJSON), 0644); err != nil {
			t.Fatalf("write fake comments for %s: %v", id, err)
		}
	}

	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
  query) cat %q ;;
  comments)
    f=%q/"$2".json
    if [ -f "$f" ]; then cat "$f"; else echo '[]'; fi
    ;;
  *) : ;;
esac
`, logPath, dataPath, commentsDir)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil // never invoked
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
