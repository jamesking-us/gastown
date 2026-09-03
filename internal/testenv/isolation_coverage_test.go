package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryTownRootResolverIsolatesItsTests is the standing watch on the escape
// surface, rather than one more fix to the surface that was in view.
//
// The cl-69h family has now escaped four times — HOME, the Dolt endpoint, the
// nudge transport, the town root — and each escape was found in production
// because the previous fix closed the callers someone had thought of. The
// pattern is not that the fixes were wrong; it is that the set of packages the
// fix applies to was maintained by memory.
//
// Confinement (testsink.ConfineTownRoot, called from workspace.Find) keys on a
// sentinel that only IsolateProcessEnv sets. A package that resolves a town
// root but never isolates its test process is therefore unguarded no matter how
// good the guard is: its tests walk up from their own directory, and on a Gas
// Town host that walk arrives at the operator's live town. This test fails when
// such a package appears, at the moment it appears.
//
// It judges a DIRECT call to workspace.Find*, which is the tractable rule:
// transitive reach would name nearly every package in the repo and the test
// would be deleted rather than obeyed. A package that reaches the lookup only
// through a dependency is not covered here — that is a real gap, and a narrower
// one than the gap of having no watch at all.
//
// The check reads the AST rather than the file text, so a package that only
// DISCUSSES the lookup in a comment — internal/testsink, which documents the
// confinement it provides and cannot import the caller without a cycle — is not
// mistaken for one that performs it.
func TestEveryTownRootResolverIsolatesItsTests(t *testing.T) {
	root := repoRoot(t)

	var unguarded []string
	for _, parent := range []string{"internal", "cmd"} {
		entries, err := os.ReadDir(filepath.Join(root, parent))
		if err != nil {
			t.Fatalf("reading %s: %v", parent, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pkg := filepath.Join(parent, entry.Name())
			resolves, hasTests, isolated := inspectPackage(t, filepath.Join(root, pkg))
			// A package with no tests runs no test process, so it has nothing
			// to isolate; it becomes this test's business the day it grows one.
			if resolves && hasTests && !isolated {
				unguarded = append(unguarded, pkg)
			}
		}
	}

	if len(unguarded) > 0 {
		t.Errorf("these packages resolve a town root but do not isolate their test process, so the confinement in workspace.Find never engages for them:\n  %s\n"+
			"Add a TestMain calling testenv.IsolateProcessEnv() (see internal/workspace/testmain_test.go) and a test calling testenv.AssertProcessEnvIsolated.",
			strings.Join(unguarded, "\n  "))
	}
}

// inspectPackage reports whether a package's production code resolves a town
// root, whether it has tests at all, and whether those tests isolate the
// process.
func inspectPackage(t *testing.T, dir string) (resolves, hasTests, isolated bool) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false, false
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // reading this repo's own sources
		if err != nil {
			t.Fatalf("reading %s: %v", filepath.Join(dir, name), err)
		}
		source := string(body)

		if strings.HasSuffix(name, "_test.go") {
			hasTests = true
			if strings.Contains(source, "IsolateProcessEnv") {
				isolated = true
			}
			continue
		}
		if callsTownRootLookup(t, filepath.Join(dir, name), source) {
			resolves = true
		}
	}
	return resolves, hasTests, isolated
}

// callsTownRootLookup reports whether a source file selects workspace.Find*.
//
// Parsed rather than grepped: the text "workspace.Find" appears in comments
// that explain the confinement, and a watch that cries wolf over prose is a
// watch someone switches off.
func callsTownRootLookup(t *testing.T, path, source string) bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name == "workspace" && strings.HasPrefix(sel.Sel.Name, "Find") {
			found = true
			return false
		}
		return true
	})
	return found
}

// repoRoot walks up from the working directory to the module root. It is the
// same upward walk the town lookup does, and it is safe for the same reason the
// town lookup is not: go.mod is a property of the checkout, so finding one
// belonging to something else is not a way to write into it.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if body, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if strings.Contains(string(body), "module github.com/steveyegge/gastown") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no gastown go.mod above %q", dir)
		}
		dir = parent
	}
}
