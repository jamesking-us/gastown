// Package testenv isolates a test process from the developer's real HOME and
// from any live Dolt server.
//
// It deliberately imports nothing but the standard library. Its predecessor
// lived in internal/testutil, which transitively imports internal/beads,
// internal/doltserver, internal/tmux and a dozen other packages — so the
// packages closest to bd and Dolt, exactly the ones that most need isolating,
// could not import it from an in-package test without an import cycle
// (cl-qaj3). Keep this package dependency-free or that trap comes back.
package testenv

import (
	"fmt"
	"os"
	"os/exec"
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

// TownRootVars are the variables the workspace lookup falls back to when the
// working directory is not inside a Gas Town workspace. An isolated process
// must have none of them set, or "no town here" silently becomes "the
// operator's town".
var TownRootVars = []string{"GT_TOWN_ROOT", "GT_ROOT"}

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
// one pointing at 3307. DoltEndpointVars exposes them for callers that must set
// them with a different mechanism (t.Setenv, say).
var doltEndpointVars = struct {
	hosts []string
	ports []string
}{
	hosts: []string{"GT_DOLT_HOST", "BEADS_DOLT_HOST", "BEADS_DOLT_SERVER_HOST"},
	ports: []string{"GT_DOLT_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_PORT"},
}

// DoltEndpointVars returns the Dolt endpoint host and port variable names.
// Callers that cannot use SetDoltEndpointEnv — a helper scoping its override to
// one test with t.Setenv, for instance — must still set the WHOLE set, or the
// variable they miss falls back to the default port.
func DoltEndpointVars() (hosts, ports []string) {
	return append([]string(nil), doltEndpointVars.hosts...),
		append([]string(nil), doltEndpointVars.ports...)
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
		fmt.Fprintf(os.Stderr, "testenv.IsolateProcessEnv: creating temp HOME: %v\n", err)
		os.Exit(1)
	}

	// Before HOME moves, pin anything that would otherwise silently follow it
	// somewhere useless.
	preserveGoToolchainEnv()

	setEnv("HOME", home)
	if runtime.GOOS == "windows" {
		setEnv("USERPROFILE", home)
	}
	writeTestGitConfig(home)
	writeTestDoltConfig(home)

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

	// Strip the town-root pointers. Every agent session carries GT_TOWN_ROOT,
	// and the workspace lookup falls back to it when the working directory is
	// not inside a town — which is precisely the state a test in a t.TempDir()
	// is in. Left set, a test that means "there is no town here" instead finds
	// the operator's real one and writes to it: the audit log, the events feed,
	// anything else keyed on the town root (hq-6ewp).
	for _, key := range TownRootVars {
		unsetEnv(key)
	}

	if os.Getenv(AllowRealDoltEnv) != "1" {
		SetDoltEndpointEnv("127.0.0.1", UnreachableDoltPort)
		// Nothing may spawn a server to satisfy the unreachable endpoint.
		setEnv("BEADS_DOLT_AUTO_START", "0")
	}

	isolationApplied = true
	return func() { _ = os.RemoveAll(home) }
}

// preserveGoToolchainEnv pins the Go toolchain's caches to the ones the real
// HOME resolves to, while it still is the real HOME.
//
// Several packages shell out to `go build` from a test — internal/cmd builds
// the gt binary its subprocess tests drive, internal/proxy builds the proxy
// client, cmd/gt cross-compiles for six platforms. GOPATH, GOMODCACHE and
// GOCACHE all default to paths under HOME, so redirecting HOME and doing
// nothing else hands those subprocesses an empty module cache and an empty
// build cache: the build re-downloads the module graph and recompiles the
// world on every run, which is slow enough to read as a hang, and fails
// outright on a box with no network.
//
// These are build artifacts. They hold none of the state isolation exists to
// protect — no beads routing, no Dolt endpoint, no agent identity — so keeping
// them shared is safe, and is what the un-isolated process was already doing.
// Anything already set in the environment is left alone.
func preserveGoToolchainEnv() {
	vars := []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"}

	var want []string
	for _, key := range vars {
		if os.Getenv(key) == "" {
			want = append(want, key)
		}
	}
	if len(want) == 0 {
		return
	}
	if _, err := exec.LookPath("go"); err != nil {
		// No toolchain on PATH, so nothing in this process can shell out to it.
		return
	}

	out, err := exec.Command("go", append([]string{"env"}, want...)...).Output()
	if err != nil {
		// Not fatal: a test that shells out to `go` will be slow rather than
		// wrong, and one that does not is unaffected.
		fmt.Fprintf(os.Stderr, "testenv: resolving Go toolchain paths (builds from tests will not be cached): %v\n", err)
		return
	}

	values := strings.Split(strings.ReplaceAll(strings.TrimRight(string(out), "\n"), "\r", ""), "\n")
	if len(values) != len(want) {
		fmt.Fprintf(os.Stderr, "testenv: `go env` returned %d values for %d variables; leaving the toolchain unpinned\n", len(values), len(want))
		return
	}
	for i, key := range want {
		if values[i] != "" {
			setEnv(key, values[i])
		}
	}
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
		fmt.Fprintf(os.Stderr, "testenv.IsolateProcessEnv: writing .gitconfig: %v\n", err)
		os.Exit(1)
	}
}

// writeTestDoltConfig disables dolt's update check inside the isolated HOME.
//
// With no global dolt config, `dolt version` phones home to ask whether a newer
// release exists, prints a warning, and can block: internal/doctor's
// TestDoltBinaryCheck_DoltInstalled failed at exactly its 10s context deadline
// once HOME moved, having passed for years against a developer HOME where the
// check was already answered (cl-qaj3).
//
// A test process has no business making a network call to a release server, so
// this is the same kind of entry as the git identity above — the minimum config
// the isolated HOME needs for the tools the tests shell out to.
func writeTestDoltConfig(home string) {
	doltDir := filepath.Join(home, ".dolt")
	if err := os.MkdirAll(doltDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "testenv.IsolateProcessEnv: creating %s: %v\n", doltDir, err)
		os.Exit(1)
	}
	// The on-disk shape of `dolt config --global --add versioncheck.disabled true`.
	const doltConfig = `{"versioncheck.disabled":"true"}`
	if err := os.WriteFile(filepath.Join(doltDir, "config_global.json"), []byte(doltConfig), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "testenv.IsolateProcessEnv: writing dolt global config: %v\n", err)
		os.Exit(1)
	}
}

// WithoutDoltEndpoint clears the Dolt endpoint variables for one test and
// restores them when it ends.
//
// Isolation sets those variables process-wide, which is what keeps an
// un-opted-in test off the production server. But a handful of tests assert
// what the code computes when NO endpoint is configured — the default-port
// fallback in GetConnectionString, EnsureMetadata and the doctor's
// getServerAddr. Those tests were reading the ambient environment and passed
// only because a developer shell usually has none of these set; under isolation
// they read the isolation port instead and fail (cl-qaj3).
//
// Use it ONLY in a test that computes an address and does not dial it. A
// cleared endpoint resolves to the production default, so a test that opens a
// connection under this helper is back to the escape cl-69h closed.
func WithoutDoltEndpoint(t *testing.T) {
	t.Helper()
	for _, key := range doltEndpointVars.hosts {
		t.Setenv(key, "")
	}
	for _, key := range doltEndpointVars.ports {
		t.Setenv(key, "")
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

// ProductionDoltPort is doltserver.DefaultPort, repeated here as a literal so
// testenv does not have to import the package. It is the port an unset
// endpoint falls back to — on a Gas Town host, the operator's live server — and
// is therefore the value no isolated test process may ever be pointed at.
const ProductionDoltPort = "3307"

// AssertProcessEnvIsolated fails the test if the process is not isolated. Put
// one call in each package that relies on IsolateProcessEnv so deleting the
// TestMain fails a test instead of silently resuming writes to the real HOME
// and the production Dolt server.
//
// Use it in packages that never stand up a Dolt server of their own; packages
// whose tests start an ephemeral container want
// AssertProcessEnvIsolatedWithDoltServer instead.
func AssertProcessEnvIsolated(t *testing.T) {
	t.Helper()

	assertHomeAndActorIsolated(t)

	if os.Getenv(AllowRealDoltEnv) == "1" {
		return
	}
	for _, key := range doltEndpointVars.ports {
		if got := os.Getenv(key); got != UnreachableDoltPort {
			t.Errorf("%s = %q, want %q — tests must not be able to reach the production Dolt server", key, got, UnreachableDoltPort)
		}
	}
}

// AssertProcessEnvIsolatedWithDoltServer is AssertProcessEnvIsolated for the
// packages whose tests deliberately stand up an ephemeral Dolt container and
// then point the process at it — internal/convoy and internal/daemon do it in
// TestMain, and RequireDoltContainer does it process-wide from inside a test,
// so by the time this guard runs the endpoint may legitimately be either the
// unreachable address or the container's.
//
// Those packages had no guard at all before, which is the weaker state: an
// endpoint that is neither unreachable nor a container — empty, or the default
// port — is exactly the escape cl-69h fixed, and this still catches it.
func AssertProcessEnvIsolatedWithDoltServer(t *testing.T) {
	t.Helper()

	assertHomeAndActorIsolated(t)

	if os.Getenv(AllowRealDoltEnv) == "1" {
		return
	}
	for _, problem := range productionEndpointProblems() {
		t.Error(problem)
	}
}

// productionEndpointProblems reports the endpoint variables that resolve to the
// production Dolt server. It tolerates a test container's ephemeral port, which
// is why it names the two values that do not survive — an explicit default port
// and an unset variable, which falls back to it — rather than requiring one
// specific value.
func productionEndpointProblems() []string {
	var problems []string
	for _, key := range doltEndpointVars.ports {
		switch got := os.Getenv(key); got {
		case "":
			problems = append(problems, fmt.Sprintf("%s is unset — an unset endpoint falls back to port %s, the production Dolt server", key, ProductionDoltPort))
		case ProductionDoltPort:
			problems = append(problems, fmt.Sprintf("%s = %q — tests must not be pointed at the production Dolt server", key, got))
		}
	}
	return problems
}

// assertHomeAndActorIsolated covers what every isolated package must hold
// regardless of which Dolt endpoint it ends up on: the TestMain ran, HOME is a
// usable private directory, and no live agent identity survived into the
// process.
func assertHomeAndActorIsolated(t *testing.T) {
	t.Helper()

	if !isolationApplied {
		t.Fatal("test process is not isolated: this package's TestMain must call testenv.IsolateProcessEnv() (see cl-69h)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolving HOME: %v", err)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("isolated HOME %q is not a usable directory: %v", home, err)
	}

	if got := os.Getenv("BD_ACTOR"); got != "" {
		t.Errorf("BD_ACTOR = %q, want empty — a live agent identity must not reach test-created rows", got)
	}
	for _, key := range TownRootVars {
		if got := os.Getenv(key); got != "" {
			t.Errorf("%s = %q, want empty — a test outside a town must not fall back to the operator's real one", key, got)
		}
	}
}

func setEnv(key, value string) {
	if err := os.Setenv(key, value); err != nil { //nolint:tenv // intentional process-wide env: TestMain runs before any test
		fmt.Fprintf(os.Stderr, "testenv: setting %s: %v\n", key, err)
		os.Exit(1)
	}
}

func unsetEnv(key string) {
	if err := os.Unsetenv(key); err != nil {
		fmt.Fprintf(os.Stderr, "testenv: unsetting %s: %v\n", key, err)
		os.Exit(1)
	}
}
