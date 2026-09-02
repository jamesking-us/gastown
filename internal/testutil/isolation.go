package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Test-process isolation for packages whose tests spawn `bd`, `gt`, or
// otherwise scaffold rigs.
//
// Two escapes motivated this (cl-69h):
//
//  1. HOME leaked into bd subprocesses, so a failed routed lookup made bd
//     materialize a stray embedded Dolt store under the developer's real
//     ~/.beads-planning. On a live Gas Town host that manufactures exactly the
//     artifact the town's cleanup docs treat as an incident.
//
//  2. The Dolt endpoint leaked. Clearing the endpoint variables is NOT enough:
//     the default port is doltserver.DefaultPort (3307), which on a Gas Town
//     host is the PRODUCTION server. Tests that scaffold a rig therefore
//     created real databases there (forkrig/testrig/testrip), and none of them
//     match the reaper's test-pollution prefixes, so `gt dolt cleanup` is a
//     no-op against them.
//
// The fix for (2) is to point tests at an address where nothing listens rather
// than to unset the variables and fall back to the default.

// UnreachableDoltPort is the port isolated test processes are pointed at. It is
// deliberately outside the Linux default ephemeral range (32768-60999) so no
// unrelated local service can be sitting on it, and it is not 3307, so a test
// that reaches for Dolt without opting in gets a connection refusal instead of
// the production server.
const UnreachableDoltPort = "63307"

// AllowRealDoltEnv opts a test process out of endpoint redirection. Set it to
// "1" only when the caller has a real server it intends to use; the Dolt
// container helpers in this package set the endpoint themselves and do not
// need it.
const AllowRealDoltEnv = "GT_TEST_ALLOW_REAL_DOLT"

// isolationApplied records whether IsolateProcessEnv has run in this process,
// so AssertProcessEnvIsolated can tell "isolated" from "never set up". A plain
// HOME comparison cannot: with no TestMain at all, HOME is simply the real one
// and there is nothing to compare it against.
var isolationApplied bool

// doltEndpointVars are every variable the beads/gt code paths consult to find a
// Dolt server. All of them are set together so a partial override cannot leave
// one pointing at 3307.
var doltEndpointVars = struct {
	hosts []string
	ports []string
}{
	hosts: []string{"GT_DOLT_HOST", "BEADS_DOLT_HOST", "BEADS_DOLT_SERVER_HOST"},
	ports: []string{"GT_DOLT_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT"},
}

// IsolateProcessEnv points the test process at a private HOME and an
// unreachable Dolt endpoint, and strips the inherited beads routing variables
// (BD_*, BEADS_*, GT_DOLT_DATA) that would otherwise route bd subprocesses to
// the operator's live databases.
//
// Call it as the FIRST statement of TestMain, before starting any Dolt
// container: the container helpers set the endpoint afterwards and must win.
//
// It returns a cleanup function that removes the temporary HOME; call it after
// m.Run(). The environment itself is process-wide and intentionally not
// restored — the process is about to exit.
func IsolateProcessEnv() func() {
	home, err := os.MkdirTemp("", "gt-test-home-*")
	if err != nil {
		// Without a private HOME the run would pollute the real one, which is
		// the whole failure this guards against. Fail loudly instead.
		fmt.Fprintf(os.Stderr, "testutil.IsolateProcessEnv: creating temp HOME: %v\n", err)
		os.Exit(1)
	}

	setEnv("HOME", home)
	if runtime.GOOS == "windows" {
		setEnv("USERPROFILE", home)
	}
	writeTestGitConfig(home)

	// Strip inherited beads routing/actor state. BD_ACTOR is the visible one:
	// it stamped a live agent's name onto rows the tests created on the
	// production server.
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "BD_") || strings.HasPrefix(key, "BEADS_") || key == "GT_DOLT_DATA" {
			unsetEnv(key)
		}
	}

	if os.Getenv(AllowRealDoltEnv) != "1" {
		SetDoltEndpointEnv("127.0.0.1", UnreachableDoltPort)
		// Nothing may spawn a server to satisfy the unreachable endpoint.
		setEnv("BEADS_DOLT_AUTO_START", "0")
	}

	isolationApplied = true
	return func() { _ = os.RemoveAll(home) }
}

// writeTestGitConfig gives the isolated HOME the minimum git identity the tests
// need. Plenty of them run `git commit` in a scratch repo and, before
// isolation, silently borrowed the developer's ~/.gitconfig; without this they
// fail with "empty ident name".
//
// Deliberately NOT copied from the real config: the credential helper. Tests
// have no business holding the operator's GitHub token, and a test that tries
// to reach a remote should fail rather than authenticate as them.
func writeTestGitConfig(home string) {
	const gitConfig = `[user]
	name = gt-test
	email = gt-test@example.invalid
[init]
	defaultBranch = main
`
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitConfig), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "testutil.IsolateProcessEnv: writing .gitconfig: %v\n", err)
		os.Exit(1)
	}
}

// SetDoltEndpointEnv points every Dolt endpoint variable at one host and port.
// Used both to redirect isolated tests at an unreachable address and, by the
// container helpers, to publish a real ephemeral server — setting the whole set
// at once is what keeps a stale variable from pointing at 3307.
func SetDoltEndpointEnv(host, port string) {
	for _, key := range doltEndpointVars.hosts {
		setEnv(key, host)
	}
	for _, key := range doltEndpointVars.ports {
		setEnv(key, port)
	}
}

// AssertProcessEnvIsolated fails the test if the process is not isolated. Put
// one call in each package that relies on IsolateProcessEnv so deleting the
// TestMain fails a test instead of silently resuming writes to the real HOME
// and the production Dolt server.
func AssertProcessEnvIsolated(t *testing.T) {
	t.Helper()

	if !isolationApplied {
		t.Fatal("test process is not isolated: this package's TestMain must call testutil.IsolateProcessEnv() (see cl-69h)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolving HOME: %v", err)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("isolated HOME %q is not a usable directory: %v", home, err)
	}

	if os.Getenv(AllowRealDoltEnv) == "1" {
		return
	}
	for _, key := range doltEndpointVars.ports {
		if got := os.Getenv(key); got != UnreachableDoltPort {
			t.Errorf("%s = %q, want %q — tests must not be able to reach the production Dolt server", key, got, UnreachableDoltPort)
		}
	}
	if got := os.Getenv("BD_ACTOR"); got != "" {
		t.Errorf("BD_ACTOR = %q, want empty — a live agent identity must not reach test-created rows", got)
	}
}

func setEnv(key, value string) {
	if err := os.Setenv(key, value); err != nil { //nolint:tenv // intentional process-wide env: TestMain runs before any test
		fmt.Fprintf(os.Stderr, "testutil: setting %s: %v\n", key, err)
		os.Exit(1)
	}
}

func unsetEnv(key string) {
	if err := os.Unsetenv(key); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: unsetting %s: %v\n", key, err)
		os.Exit(1)
	}
}
