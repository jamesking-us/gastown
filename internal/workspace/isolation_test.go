package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/testenv"
	"github.com/steveyegge/gastown/internal/testsink"
)

// TestFindFromCwdDoesNotEscapeToTheLiveTown is the cl-st8u reproduction.
//
// It runs where every Go test runs: the package's own directory. On a Gas Town
// host that directory is inside the gastown checkout, the checkout is inside
// /gt, and /gt carries mayor/town.json — so the upward walk that is supposed to
// report "this process is not in a town" reported the operator's real one, and
// every caller that joins a path onto the result (the events feed, the audit
// log, the town log, the nudge queue) wrote into it.
//
// Off such a host the walk finds nothing and the test passes trivially. That is
// the correct shape: the assertion is about what the lookup may return, not
// about the layout of the machine running it.
func TestFindFromCwdDoesNotEscapeToTheLiveTown(t *testing.T) {
	testenv.AssertProcessEnvIsolated(t)

	found, err := FindFromCwd()
	if err != nil {
		t.Fatalf("FindFromCwd: %v", err)
	}
	if found == "" {
		return
	}
	if !testsink.OwnsPath(found) {
		t.Errorf("FindFromCwd() = %q — an isolated test process resolved a town it does not own; "+
			"every town-root-keyed write in this process would land in it (cl-st8u)", found)
	}
}

// TestFindConfinesAForeignTownToTheFixture builds the escape deterministically
// instead of relying on the host being a Gas Town machine: a town marker in a
// directory that is neither under the OS temp directory nor under the isolated
// HOME is, by construction, a town this process does not own.
func TestFindConfinesAForeignTownToTheFixture(t *testing.T) {
	foreign := foreignTown(t)

	found, err := Find(foreign)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !testsink.OwnsPath(found) {
		t.Fatalf("Find(%q) = %q, which this process does not own", foreign, found)
	}
	if want := testsink.TownRoot(); found != want {
		t.Errorf("Find(%q) = %q, want the isolated fixture town %q", foreign, found, want)
	}
}

// TestFindStillReturnsATownTheTestOwns guards the other direction. Confinement
// that answered "no town" for a town built by t.TempDir would stop the lookup
// being exercised at all, which is how a guard quietly stops guarding.
func TestFindStillReturnsATownTheTestOwns(t *testing.T) {
	root := realPath(t, t.TempDir())
	writeTownMarker(t, root)

	nested := filepath.Join(root, "myrig", "polecats", "worker")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	found, err := Find(nested)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found != root {
		t.Errorf("Find = %q, want %q — a town the test built must survive confinement", found, root)
	}
}

// TestFindFromCwdOrErrorDoesNotEscapeThroughTheEnvFallback covers the second
// door into the same room: when the walk finds nothing, the lookup falls back
// to GT_TOWN_ROOT/GT_ROOT. Isolation strips both, but a test is free to set one
// with t.Setenv, and pointing it at a live town must not work either.
func TestFindFromCwdOrErrorDoesNotEscapeThroughTheEnvFallback(t *testing.T) {
	foreign := foreignTown(t)

	for _, key := range testenv.TownRootVars {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, foreign)

			found, err := FindFromCwdOrError()
			if err != nil {
				return
			}
			if !testsink.OwnsPath(found) {
				t.Errorf("FindFromCwdOrError() = %q with %s=%q — the env fallback handed an isolated process a foreign town", found, key, foreign)
			}
		})
	}
}

// foreignTown builds a town marker outside every directory this process owns.
//
// It is created next to the package sources rather than in t.TempDir precisely
// because t.TempDir is owned: the point of the fixture is to be a town the
// isolation layer must refuse.
func foreignTown(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp(".", "foreign-town-")
	if err != nil {
		t.Fatalf("creating foreign town: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolving foreign town: %v", err)
	}
	if testsink.OwnsPath(abs) {
		t.Skipf("package directory %q is itself test-owned; no foreign town can be built here", abs)
	}
	writeTownMarker(t, abs)
	return abs
}

func writeTownMarker(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, PrimaryMarker), []byte(`{"type":"town","name":"foreign"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
