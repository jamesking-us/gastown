package polecat

import (
	"os"
	"path/filepath"
)

// SandboxCheckouts returns the sandbox root for a polecat clone and every git
// checkout inside it. A nuke destroys the whole sandbox, so any predicate that
// authorizes destruction — or that claims no work is at risk — must be computed
// over this set, not over the rig clone alone.
//
// Layout (current): <rig>/polecats/<name>/<rigname>, so the sandbox root is the
// PARENT of the clone and can hold sibling checkouts. Cross-repo work uses
// exactly that shape: the rig clone plus a scratch clone such as gastown-fork.
// Layout (legacy): <rig>/polecats/<name>, where the clone is the sandbox root
// and there is nothing beside it.
//
// The scan is one level deep. Checkouts nested deeper are NOT returned; that
// limit is stated here, and callers state it in their output, because the
// failure it produces is a confident wrong answer about what may be destroyed
// (cl-hwl, lesson 318, and the cl-jkr layout trap).
//
// The rig clone is always first in the returned slice.
func SandboxCheckouts(clonePath string) (root string, checkouts []string) {
	root = clonePath
	parent := filepath.Dir(clonePath)
	// Only treat the parent as the sandbox root when the path really is
	// <...>/polecats/<name>/<clone>. Anything else keeps single-checkout
	// behaviour rather than scanning unrelated siblings.
	if filepath.Base(filepath.Dir(parent)) == "polecats" {
		root = parent
	}
	if root == clonePath {
		return root, []string{clonePath}
	}

	checkouts = append(checkouts, clonePath)
	entries, err := os.ReadDir(root)
	if err != nil {
		return root, checkouts
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if path == clonePath {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
			continue
		}
		checkouts = append(checkouts, path)
	}
	return root, checkouts
}
