package testutil

import (
	"os"
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
