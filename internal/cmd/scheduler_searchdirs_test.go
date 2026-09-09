package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// writeSearchDirsRigsJSON registers the named rigs in mayor/rigs.json.
func writeSearchDirsRigsJSON(t *testing.T, townRoot string, names ...string) {
	t.Helper()
	rigs := make(map[string]config.RigEntry, len(names))
	for _, name := range names {
		rigs[name] = config.RigEntry{BeadsConfig: &config.BeadsConfig{Prefix: name}}
	}
	path := filepath.Join(townRoot, "mayor", "rigs.json")
	if err := config.SaveRigsConfig(path, &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    rigs,
	}); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}
}

func mkBeadsDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir %s/.beads: %v", dir, err)
	}
}

// TestBeadsSearchDirsExcludesUnregisteredBeadsDirs pins the fix for the outage
// where an unrouted, unregistered /gt/beads directory was treated as a rig and
// failed town-wide dispatch 165 times. Registration, not the filesystem, decides
// what the scheduler scans.
func TestBeadsSearchDirsExcludesUnregisteredBeadsDirs(t *testing.T) {
	townRoot := t.TempDir()
	mkBeadsDir(t, townRoot)
	writeSearchDirsRigsJSON(t, townRoot, "testrig")
	mkBeadsDir(t, filepath.Join(townRoot, "testrig"))
	mkBeadsDir(t, filepath.Join(townRoot, "testrig", "mayor", "rig"))
	// Strays: an unregistered checkout, and a quarantine rename that stayed
	// inside the town root (renaming in place did not quarantine anything —
	// the old glob simply followed the new name).
	mkBeadsDir(t, filepath.Join(townRoot, "beads"))
	mkBeadsDir(t, filepath.Join(townRoot, "beads.quarantined-20260907T010611Z"))
	if err := beads.WriteRoutes(filepath.Join(townRoot, ".beads"), []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "gt-", Path: "testrig/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	dirs, err := beadsSearchDirs(townRoot)
	if err != nil {
		t.Fatalf("beadsSearchDirs: %v", err)
	}

	want := []string{
		townRoot,
		filepath.Join(townRoot, "testrig"),
		filepath.Join(townRoot, "testrig", "mayor", "rig"),
	}
	assertSameDirs(t, dirs, want)
}

// TestBeadsSearchDirsIncludesRoutedDirWithoutRigsEntry covers the other half of
// the union: a dir bd routes to is searched even when rigs.json has no entry for
// it, so route-first setups keep their contexts visible.
func TestBeadsSearchDirsIncludesRoutedDirWithoutRigsEntry(t *testing.T) {
	townRoot := t.TempDir()
	mkBeadsDir(t, townRoot)
	writeSearchDirsRigsJSON(t, townRoot)
	mkBeadsDir(t, filepath.Join(townRoot, "routedrig", "mayor", "rig"))
	mkBeadsDir(t, filepath.Join(townRoot, "stray"))
	if err := beads.WriteRoutes(filepath.Join(townRoot, ".beads"), []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "rt-", Path: "routedrig/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	dirs, err := beadsSearchDirs(townRoot)
	if err != nil {
		t.Fatalf("beadsSearchDirs: %v", err)
	}

	assertSameDirs(t, dirs, []string{
		townRoot,
		filepath.Join(townRoot, "routedrig", "mayor", "rig"),
	})
}

// TestBeadsSearchDirsFallsBackToTownRootRigsJSON mirrors the session prefix
// registry, which also accepts a town-root rigs.json.
func TestBeadsSearchDirsFallsBackToTownRootRigsJSON(t *testing.T) {
	townRoot := t.TempDir()
	mkBeadsDir(t, townRoot)
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "rigs.json"), &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    map[string]config.RigEntry{"testrig": {}},
	}); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}
	mkBeadsDir(t, filepath.Join(townRoot, "testrig"))

	dirs, err := beadsSearchDirs(townRoot)
	if err != nil {
		t.Fatalf("beadsSearchDirs: %v", err)
	}

	assertSameDirs(t, dirs, []string{townRoot, filepath.Join(townRoot, "testrig")})
}

// TestBeadsSearchDirsErrorsWithoutAnyRegistry keeps the failure loud: with both
// registries gone the scheduler must not report a town-root-only view, which
// would read as "nothing scheduled".
func TestBeadsSearchDirsErrorsWithoutAnyRegistry(t *testing.T) {
	townRoot := t.TempDir()
	mkBeadsDir(t, townRoot)
	mkBeadsDir(t, filepath.Join(townRoot, "testrig"))

	_, err := beadsSearchDirs(townRoot)
	if err == nil {
		t.Fatal("beadsSearchDirs succeeded with no rigs.json and no routes.jsonl")
	}
	if !strings.Contains(err.Error(), "rigs.json") || !strings.Contains(err.Error(), "routes.jsonl") {
		t.Fatalf("error = %q, want both missing registries named", err.Error())
	}
}

// TestScanAllSlingContextRecordsIgnoresUnregisteredBrokenDir is the end-to-end
// shape of the original outage: a stray .beads dir whose database cannot be
// opened at all. It must not even be scanned, so it produces no scan failure.
func TestScanAllSlingContextRecordsIgnoresUnregisteredBrokenDir(t *testing.T) {
	townRoot := t.TempDir()
	mkBeadsDir(t, townRoot)
	writeSearchDirsRigsJSON(t, townRoot, "testrig")
	mkBeadsDir(t, filepath.Join(townRoot, "testrig"))
	mkBeadsDir(t, filepath.Join(townRoot, "beads"))
	installFakeBD(t, `#!/bin/sh
case "$BEADS_DIR" in
  */beads/.beads) echo "Error: failed to open database: schema migration blocked" >&2; exit 1 ;;
  *) printf '[]\n'; exit 0 ;;
esac
`)

	records, failures, err := scanAllSlingContextRecords(townRoot)
	if err != nil {
		t.Fatalf("scanAllSlingContextRecords: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("scan failures = %+v, want none: the stray dir should not be scanned at all", failures)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}
}

func assertSameDirs(t *testing.T, got, want []string) {
	t.Helper()
	gotSet := make(map[string]bool, len(got))
	for _, d := range got {
		gotSet[d] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, d := range want {
		wantSet[d] = true
	}
	for _, d := range want {
		if !gotSet[d] {
			t.Errorf("search dirs missing %s (got %v)", d, got)
		}
	}
	for _, d := range got {
		if !wantSet[d] {
			t.Errorf("search dirs include unregistered %s (got %v)", d, got)
		}
	}
}
