package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsolateProcessEnv asserts the guarantees the TestMains rely on. It runs
// first by name ordering only incidentally; the assertions do not depend on
// other tests in this package, and the environment it establishes is the one
// the rest of the package would want anyway.
func TestIsolateProcessEnv(t *testing.T) {
	// Simulate the inherited environment of a live Gas Town agent shell: an
	// actor identity, beads routing state, and the production Dolt endpoint.
	t.Setenv("BD_ACTOR", "cloudcontentmanager/polecats/minuteman")
	t.Setenv("BEADS_DIR", "/gt/.beads")
	t.Setenv("GT_DOLT_DATA", "/home/agent/.dolt-data")
	t.Setenv("GT_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_PORT", "3307")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "3307")

	before := os.Getenv("HOME")

	cleanup := IsolateProcessEnv()
	t.Cleanup(cleanup)

	home := os.Getenv("HOME")
	if home == before {
		t.Fatalf("HOME unchanged (%q): tests would still write to the real home", home)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("isolated HOME %q is not a directory: %v", home, err)
	}

	for _, key := range []string{"BD_ACTOR", "BEADS_DIR", "GT_DOLT_DATA"} {
		if got := os.Getenv(key); got != "" {
			t.Errorf("%s = %q, want empty", key, got)
		}
	}

	// The important one: the endpoint must be redirected, not merely cleared.
	// Clearing it falls back to doltserver.DefaultPort (3307), which on a Gas
	// Town host is the production server.
	for _, key := range doltEndpointVars.ports {
		if got := os.Getenv(key); got != UnreachableDoltPort {
			t.Errorf("%s = %q, want %q", key, got, UnreachableDoltPort)
		}
	}
	for _, key := range doltEndpointVars.hosts {
		if got := os.Getenv(key); got != "127.0.0.1" {
			t.Errorf("%s = %q, want 127.0.0.1", key, got)
		}
	}
	if got := os.Getenv("BEADS_DOLT_AUTO_START"); got != "0" {
		t.Errorf("BEADS_DOLT_AUTO_START = %q, want 0", got)
	}

	// Tests in the isolated packages run `git commit` in scratch repos; without
	// an identity in the isolated HOME they fail with "empty ident name".
	cfg, err := os.ReadFile(home + "/.gitconfig")
	if err != nil {
		t.Fatalf("isolated HOME has no .gitconfig: %v", err)
	}
	if !strings.Contains(string(cfg), "email = ") || !strings.Contains(string(cfg), "name = ") {
		t.Errorf(".gitconfig lacks a git identity:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "credential") {
		t.Error(".gitconfig must not carry a credential helper — tests must not authenticate as the operator")
	}

	// The nudge transport is the third escape surface in this family: HOME and
	// the Dolt endpoint were closed by cl-69h and cl-qaj3, and neither touched
	// tmux or the nudge queue, so the suite went on delivering to live seats
	// (gt-8f3). Isolation has to arm the guards by default — an opt-in the
	// individual test has to remember is what produced the escape.
	if got := os.Getenv(IsolatedEnv); got != "1" {
		t.Errorf("%s = %q, want \"1\"", IsolatedEnv, got)
	}
	sink := os.Getenv(NudgeSinkEnv)
	if want := filepath.Join(home, NudgeSinkFile); sink != want {
		t.Errorf("%s = %q, want %q — an isolated process needs somewhere to record the nudges it refuses", NudgeSinkEnv, sink, want)
	}

	AssertProcessEnvIsolated(t)

	cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("cleanup left %q behind: %v", home, err)
	}
}

// TestSetDoltEndpointEnv covers the container helpers' override: they must be
// able to replace every endpoint variable, or a leftover from isolation sends
// some code path back to the default port.
func TestSetDoltEndpointEnv(t *testing.T) {
	for _, key := range append(append([]string{}, doltEndpointVars.hosts...), doltEndpointVars.ports...) {
		t.Setenv(key, "stale")
	}

	SetDoltEndpointEnv("10.0.0.1", "12345")

	for _, key := range doltEndpointVars.hosts {
		if got := os.Getenv(key); got != "10.0.0.1" {
			t.Errorf("%s = %q, want 10.0.0.1", key, got)
		}
	}
	for _, key := range doltEndpointVars.ports {
		if got := os.Getenv(key); got != "12345" {
			t.Errorf("%s = %q, want 12345", key, got)
		}
	}
}

// TestUnreachableDoltPortIsNotProduction pins the one property the constant
// exists for. 3307 is doltserver.DefaultPort; a refactor that "simplified" this
// back to the default would silently restore the bug.
func TestUnreachableDoltPortIsNotProduction(t *testing.T) {
	if UnreachableDoltPort == "3307" {
		t.Fatal("UnreachableDoltPort must not be the production Dolt port")
	}
}

// TestAssertProcessEnvIsolatedWithDoltServer covers the guard the
// container-backed packages use: it must accept the endpoint a test container
// publishes, and it must still reject the two values that mean "pointed at the
// production Dolt server" (cl-qaj3).
func TestAssertProcessEnvIsolatedWithDoltServer(t *testing.T) {
	cleanup := IsolateProcessEnv()
	t.Cleanup(cleanup)
	t.Cleanup(func() { SetDoltEndpointEnv("127.0.0.1", UnreachableDoltPort) })

	// The two endpoints it must accept: the unreachable address isolation set,
	// and the ephemeral port a test container publishes over it.
	for _, port := range []string{UnreachableDoltPort, "49711"} {
		SetDoltEndpointEnv("127.0.0.1", port)
		if problems := productionEndpointProblems(); len(problems) != 0 {
			t.Errorf("port %q rejected: %v", port, problems)
		}
		AssertProcessEnvIsolatedWithDoltServer(t)
	}

	// The two it must not: the production port, and unset — which falls back
	// to exactly that port, the mistake cl-69h was.
	for _, port := range []string{ProductionDoltPort, ""} {
		SetDoltEndpointEnv("127.0.0.1", port)
		if len(productionEndpointProblems()) != len(doltEndpointVars.ports) {
			t.Errorf("port %q accepted; it resolves to the production Dolt server", port)
		}
	}
}

// TestProductionDoltPortIsNotTheUnreachableOne pins the literal this package
// duplicates from doltserver.DefaultPort rather than importing (testenv must
// stay free of gastown dependencies — see the package comment).
func TestProductionDoltPortIsNotTheUnreachableOne(t *testing.T) {
	if ProductionDoltPort == UnreachableDoltPort {
		t.Fatal("ProductionDoltPort and UnreachableDoltPort must differ")
	}
}

// TestGoToolchainEnvSurvivesIsolation pins the fix for the knock-on effect of
// moving HOME: GOPATH, GOMODCACHE and GOCACHE default to paths under it, so a
// test that shells out to `go build` would otherwise get an empty module cache
// and an empty build cache — a full re-download and rebuild per run, and a
// hard failure with no network (cl-qaj3).
func TestGoToolchainEnvSurvivesIsolation(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		t.Setenv(key, "")
	}

	home := os.Getenv("HOME")

	cleanup := IsolateProcessEnv()
	t.Cleanup(cleanup)

	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		got := os.Getenv(key)
		if got == "" {
			t.Errorf("%s is unset after isolation: it now resolves under the temporary HOME", key)
			continue
		}
		if strings.HasPrefix(got, os.Getenv("HOME")) {
			t.Errorf("%s = %q, which is inside the isolated HOME — the cache is empty and every build starts from nothing", key, got)
		}
	}

	if os.Getenv("HOME") == home {
		t.Fatal("HOME unchanged; the rest of this test proves nothing")
	}
}

// TestIsolateProcessEnvKeepsACallerSuppliedSink covers the tests that set their
// own sink path and read it back. Isolation supplies a default, but it must not
// overwrite a path someone else chose — those tests set it before TestMain runs
// in exactly one case that matters, a subprocess launched with the variable
// already in its environment.
func TestIsolateProcessEnvKeepsACallerSuppliedSink(t *testing.T) {
	chosen := filepath.Join(t.TempDir(), "chosen.log")
	t.Setenv(NudgeSinkEnv, chosen)

	cleanup := IsolateProcessEnv()
	t.Cleanup(cleanup)

	if got := os.Getenv(NudgeSinkEnv); got != chosen {
		t.Errorf("%s = %q, want the caller's %q", NudgeSinkEnv, got, chosen)
	}
}
