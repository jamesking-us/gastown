package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// modulePath is this repo's module path. The reach watch resolves import edges
// against it so that only first-party packages are followed: an import of
// something outside the module cannot lead back to the town-root lookup.
const modulePath = "github.com/steveyegge/gastown"

// workspacePackage is the town-root lookup's import path. A test binary that
// links it can execute workspace.Find; one that does not, cannot.
const workspacePackage = modulePath + "/internal/workspace"

// TestEveryTownRootReacherIsolatesItsTests is the transitive half of the watch
// TestEveryTownRootResolverIsolatesItsTests keeps over direct callers.
//
// THE GAP IT CLOSES (cl-az4x). The direct watch judges a package's own source
// for a call to workspace.Find*. That rule is tractable and it is not the rule
// that matches the danger: confinement (testsink.ConfineTownRoot, called from
// workspace.Find) keys on a process-wide sentinel that only IsolateProcessEnv
// sets, so what decides whether a test process is guarded is whether the LOOKUP
// CAN RUN IN IT — not who wrote the call. A package that calls session.*,
// events.Log or mayor.* resolves a town root just as surely as one that types
// workspace.Find itself, and before this watch its test binary carried no
// sentinel: its tests walked up from their own directory and, on a Gas Town
// host where the checkout sits inside /gt, arrived at the operator's live town.
//
// WHERE THE LINE GOES, AND WHY THERE. The line is drawn at LINKAGE: a test
// binary that links internal/workspace must isolate its process. Nothing weaker
// is honest — an import is exactly the precondition for the lookup executing,
// and any rule about which imported functions "really" resolve a root is a list
// maintained by memory, which is how this family escaped four times. Nothing
// stronger is available either: linkage is the last property visible without
// running the code.
//
// The objection recorded when this work was filed was that a transitive rule
// would name most of the repo and therefore be deleted rather than obeyed. That
// was a guess, and measuring it is most of this test's value. At the time of
// writing: 77 test packages under internal/ and cmd/, 37 of which link the
// lookup. 32 of the 37 were already isolated by the four preceding fixes, so
// the whole cost of moving from direct-call to linkage was five TestMains
// (internal/boot, internal/checkpoint, internal/feed, internal/krc,
// internal/quota — reaching through session, runtime and events). The other 40
// test packages do not link internal/workspace at all, which is the OTHER half
// of the coverage claim and the reason this watch can be strict: they are not
// unexamined, they are provably unable to resolve a town root, and this test
// re-proves it on every run rather than in a comment that ages.
//
// FAILS CLOSED. There is deliberately no allowlist. A package that grows an
// import reaching the lookup trips this the moment it does, and the two ways to
// clear it are the two acceptable states: isolate the test process, or do not
// link the lookup. A package that can do neither is a design conversation, not
// an entry in a skip list.
func TestEveryTownRootReacherIsolatesItsTests(t *testing.T) {
	pkgs := loadModulePackages(t, repoRoot(t), modulePath)

	var unguarded []string
	for _, pkg := range sortedPackages(pkgs) {
		// A package with no tests runs no test process, so it has nothing to
		// isolate; it becomes this test's business the day it grows one.
		if !pkg.hasTests || pkg.isolated {
			continue
		}
		chain := linkChainToWorkspace(pkgs, pkg)
		if chain == nil {
			continue
		}
		unguarded = append(unguarded, pkg.rel+"\n      "+strings.Join(chain, " -> "))
	}

	if len(unguarded) > 0 {
		t.Errorf("these test binaries link the town-root lookup but do not isolate their test process, "+
			"so the confinement in workspace.Find never engages for them:\n    %s\n"+
			"Each is shown with the import chain that reaches the lookup. Add a TestMain calling "+
			"testenv.IsolateProcessEnv() (see internal/keepalive/testmain_test.go) and a test calling "+
			"testenv.AssertProcessEnvIsolated — or drop the import, which is equally acceptable and is "+
			"what keeps the other packages off this list.",
			strings.Join(unguarded, "\n    "))
	}
}

// TestTownRootReachIsMeasuredNotAssumed pins the shape of the answer this watch
// found, so that a change which quietly widens the lookup's reach across the
// repo is visible as a number rather than as a diff nobody reads.
//
// It is a floor and a ceiling, not an exact count: packages come and go, and a
// test that has to be edited for every new package is a test that gets edited
// without being read. What it refuses is drift large enough to invalidate the
// judgement above — if linking the lookup stops being a minority property of
// the repo, "isolate every reacher" stops being a cheap rule and the line needs
// re-drawing deliberately.
func TestTownRootReachIsMeasuredNotAssumed(t *testing.T) {
	pkgs := loadModulePackages(t, repoRoot(t), modulePath)

	var withTests, reaching int
	for _, pkg := range pkgs {
		if !pkg.hasTests {
			continue
		}
		withTests++
		if linkChainToWorkspace(pkgs, pkg) != nil {
			reaching++
		}
	}

	if withTests == 0 {
		t.Fatal("found no test packages in the module — the walk below internal/ and cmd/ is broken, not the repo")
	}
	t.Logf("test packages: %d; linking %s: %d", withTests, workspacePackage, reaching)

	// Measured at 37/77 when this watch was written (cl-az4x).
	if reaching*2 > withTests {
		t.Errorf("%d of %d test packages now link the town-root lookup — a majority. "+
			"The linkage rule in TestEveryTownRootReacherIsolatesItsTests was adopted because reaching the "+
			"lookup was a minority property; re-examine where the line goes rather than isolating the world.",
			reaching, withTests)
	}
}

// packageInfo is one first-party package: what it imports, whether it has
// tests, and whether those tests isolate the process.
//
// prodImports and testImports are kept apart because a test binary links them
// asymmetrically: the package under test contributes both, every dependency
// contributes only its production imports. Folding them together would report a
// package as a reacher because some dependency's own tests import the lookup,
// which is a different test binary's problem.
type packageInfo struct {
	importPath  string
	rel         string
	prodImports []string
	testImports []string
	hasTests    bool
	isolated    bool
}

// loadModulePackages reads every first-party package under root, keyed by
// import path.
//
// It parses the sources rather than shelling out to `go list` for two reasons.
// The toolchain's answer is the authority on what links, but a watch that needs
// the toolchain on PATH fails OPEN in the one environment where it is missing,
// and this watch's whole value is failing closed. And ignoring build
// constraints, as this does, over-approximates the import graph: a file that
// only builds on darwin still contributes its edges here, so a platform-gated
// reacher is caught on every platform rather than on one.
//
// Nested modules are skipped: their packages are not linked into this module's
// test binaries, so their imports are not this watch's business.
func loadModulePackages(t *testing.T, root, module string) map[string]*packageInfo {
	t.Helper()

	pkgs := map[string]*packageInfo{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		pkg := readPackageDir(t, root, module, path)
		if pkg != nil {
			pkgs[pkg.importPath] = pkg
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return pkgs
}

// readPackageDir reads one directory's Go files, or returns nil when it holds
// none. Files that do not parse are skipped rather than fatal: a directory of
// deliberately broken fixtures is not a reason for the watch to stop watching.
func readPackageDir(t *testing.T, root, module, dir string) *packageInfo {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("relativizing %s against %s: %v", dir, root, err)
	}
	rel = filepath.ToSlash(rel)

	pkg := &packageInfo{rel: rel, importPath: module}
	if rel != "." {
		pkg.importPath = module + "/" + rel
	}

	var any bool
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		any = true
		path := filepath.Join(dir, name)
		body, err := os.ReadFile(path) //nolint:gosec // reading this repo's own sources
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		imports := firstPartyImports(file, module)

		if strings.HasSuffix(name, "_test.go") {
			pkg.hasTests = true
			pkg.testImports = append(pkg.testImports, imports...)
			if strings.Contains(string(body), "IsolateProcessEnv") {
				pkg.isolated = true
			}
			continue
		}
		pkg.prodImports = append(pkg.prodImports, imports...)
	}
	if !any {
		return nil
	}
	return pkg
}

// firstPartyImports returns the module-internal import paths of one file.
// Imports outside the module are dropped: nothing out there imports back into
// this module, so they cannot be a route to the lookup.
func firstPartyImports(file *ast.File, module string) []string {
	var out []string
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if path == module || strings.HasPrefix(path, module+"/") {
			out = append(out, path)
		}
	}
	return out
}

// linkChainToWorkspace returns the shortest import chain from a package's TEST
// BINARY to the town-root lookup, or nil when the binary does not link it.
//
// The search starts from the package under test and follows its production and
// test imports; from every package after that it follows production imports
// only, which is how the linker builds the binary. Breadth-first, so the chain
// in a failure message is the shortest explanation of why the package is on the
// list rather than an arbitrary walk through the graph.
func linkChainToWorkspace(pkgs map[string]*packageInfo, start *packageInfo) []string {
	return linkChain(pkgs, start, workspacePackage, modulePath)
}

// linkChain is linkChainToWorkspace with the module and the target named, so
// the negative controls can run the identical walk over a synthetic module
// instead of over a copy of it that could drift.
func linkChain(pkgs map[string]*packageInfo, start *packageInfo, target, module string) []string {
	if start.importPath == target {
		return []string{start.rel}
	}

	type step struct {
		path  string
		chain []string
	}
	seen := map[string]bool{start.importPath: true}
	queue := []step{{path: start.importPath, chain: []string{start.rel}}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		pkg := pkgs[current.path]
		if pkg == nil {
			continue
		}
		next := pkg.prodImports
		if current.path == start.importPath {
			next = append(append([]string(nil), pkg.prodImports...), pkg.testImports...)
		}
		for _, imported := range sortedUnique(next) {
			if seen[imported] {
				continue
			}
			seen[imported] = true
			label := strings.TrimPrefix(imported, module+"/")
			chain := append(append([]string(nil), current.chain...), label)
			if imported == target {
				return chain
			}
			queue = append(queue, step{path: imported, chain: chain})
		}
	}
	return nil
}

// sortedUnique orders and de-duplicates import paths so the breadth-first walk
// visits siblings in a stable order and the chain it reports does not change
// between runs.
func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedPackages(pkgs map[string]*packageInfo) []*packageInfo {
	out := make([]*packageInfo, 0, len(pkgs))
	for _, pkg := range pkgs {
		out = append(out, pkg)
	}
	slices.SortFunc(out, func(a, b *packageInfo) int { return strings.Compare(a.rel, b.rel) })
	return out
}

// TestLinkChainToWorkspaceNegativeControls exercises the reach rule against a
// synthetic module, in both directions.
//
// A watch is only worth its failure message if it fails when it should AND
// passes when it should, and neither half is provable against the real repo:
// the repo currently has no unguarded reacher (that is the point of this
// change), so a broken detector and a clean repo produce the same green run.
// The fixture below supplies the missing half permanently, so a later
// refactor of the graph walk cannot quietly turn the watch into a no-op.
//
// The cases are the four states that matter, including the two asymmetries
// that are easy to get wrong: a dependency's TEST imports must not count
// toward its dependents' binaries, and the package under test's OWN test
// imports must.
func TestLinkChainToWorkspaceNegativeControls(t *testing.T) {
	const module = "example.test/fixture"

	root := t.TempDir()
	writeFixturePackage(t, root, "internal/workspace", `package workspace

func Find() string { return "" }
`)
	writeFixturePackage(t, root, "internal/events", `package events

import "example.test/fixture/internal/workspace"

func Log() string { return workspace.Find() }
`)
	// Reaches through internal/events, has tests, does not isolate: the exact
	// shape of the five packages this change found in the real repo.
	writeFixturePackage(t, root, "internal/reacher", `package reacher

import "example.test/fixture/internal/events"

func Do() string { return events.Log() }
`, `package reacher

import "testing"

func TestDo(t *testing.T) { _ = Do() }
`)
	// The same reach, isolated. Must NOT be reported.
	writeFixturePackage(t, root, "internal/isolated", `package isolated

import "example.test/fixture/internal/events"

func Do() string { return events.Log() }
`, `package isolated

import "testing"

func TestMain(m *testing.M) { _ = IsolateProcessEnv; _ = m }
`)
	// No route to the lookup at all: the "provably cannot resolve a town root"
	// half of the coverage claim.
	writeFixturePackage(t, root, "internal/unrelated", `package unrelated

import "strings"

func Do() string { return strings.TrimSpace(" ") }
`, `package unrelated

import "testing"

func TestDo(t *testing.T) { _ = Do() }
`)
	// Reaches ONLY from its own _test.go. The test binary links the lookup, so
	// it must be reported even though the production package does not.
	writeFixturePackage(t, root, "internal/testonly", `package testonly

func Do() string { return "" }
`, `package testonly

import (
	"testing"

	"example.test/fixture/internal/events"
)

func TestDo(t *testing.T) { _ = events.Log() }
`)
	// Imports a package whose only route to the lookup is through that
	// package's own tests. Those are a different binary, so this one is clean —
	// the asymmetry prodImports/testImports exists to preserve.
	writeFixturePackage(t, root, "internal/viatestonly", `package viatestonly

import "example.test/fixture/internal/testonly"

func Do() string { return testonly.Do() }
`, `package viatestonly

import "testing"

func TestDo(t *testing.T) { _ = Do() }
`)

	pkgs := loadModulePackages(t, root, module)
	target := module + "/internal/workspace"

	reported := map[string]bool{}
	for _, pkg := range sortedPackages(pkgs) {
		if !pkg.hasTests || pkg.isolated {
			continue
		}
		if linkChain(pkgs, pkg, target, module) != nil {
			reported[pkg.rel] = true
		}
	}

	for _, want := range []string{"internal/reacher", "internal/testonly"} {
		if !reported[want] {
			t.Errorf("%s links the lookup and does not isolate, but the watch did not report it — "+
				"the watch would pass over a real escape", want)
		}
	}
	for _, wantClean := range []string{"internal/isolated", "internal/unrelated", "internal/viatestonly"} {
		if reported[wantClean] {
			t.Errorf("%s was reported, but it is one of the states the watch must accept — "+
				"a watch that fires on clean packages gets switched off", wantClean)
		}
	}

	chain := linkChain(pkgs, pkgs[module+"/internal/reacher"], target, module)
	want := "internal/reacher -> internal/events -> internal/workspace"
	if got := strings.Join(chain, " -> "); got != want {
		t.Errorf("reported chain = %q, want %q — the chain is the failure message's whole explanation", got, want)
	}
}

// writeFixturePackage writes one package directory: a production file and, when
// given, a test file.
func writeFixturePackage(t *testing.T, root, rel string, sources ...string) {
	t.Helper()

	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating fixture package %s: %v", rel, err)
	}
	names := []string{"pkg.go", "pkg_test.go"}
	for i, source := range sources {
		if err := os.WriteFile(filepath.Join(dir, names[i]), []byte(source), 0o600); err != nil {
			t.Fatalf("writing fixture %s/%s: %v", rel, names[i], err)
		}
	}
}
