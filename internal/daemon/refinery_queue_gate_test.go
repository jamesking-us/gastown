package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
)

// The queue-depth trigger is tested as a predicate, never by starting a
// refinery: on a fork-backed rig starting one is exactly the failure this
// guards against (gt-kx4).

func TestRefineryQueueGate_SpawnsOnNonEmptyQueue(t *testing.T) {
	spawn, reason := refineryQueueGate(nil, nil, 1)
	if !spawn {
		t.Fatalf("expected spawn with a ready MR queued, got skip (%s)", reason)
	}
	if !strings.Contains(reason, "1 ready merge request") {
		t.Errorf("reason should report queue depth, got %q", reason)
	}
}

func TestRefineryQueueGate_SkipsOnEmptyQueue(t *testing.T) {
	spawn, reason := refineryQueueGate(nil, nil, 0)
	if spawn {
		t.Fatal("expected skip with an empty queue")
	}
	if reason != "merge queue empty" {
		t.Errorf("unexpected reason: %q", reason)
	}
}

// A fork-backed rig has its refinery disabled by ruling; the new trigger must
// consult the same guard the event-driven path applies inside Manager.Start(),
// otherwise it would auto-start that refinery on every heartbeat by design.
func TestRefineryQueueGate_ForkRigBlocksSpawnDespiteQueuedWork(t *testing.T) {
	forkErr := refinery.NewForkRigError("gastown", "https://github.com/gastownhall/gastown.git")

	spawn, reason := refineryQueueGate(forkErr, nil, 5)
	if spawn {
		t.Fatal("expected skip for a fork-backed rig even with MRs queued")
	}
	if !strings.Contains(reason, "fork-backed rig") {
		t.Errorf("reason should name the fork-rig guard, got %q", reason)
	}
}

// Fail closed (gt-9gv): an unreadable rig config cannot be read as "not a fork
// rig", so it blocks the spawn just like a confirmed fork rig.
func TestRefineryQueueGate_UndeterminedForkStatusBlocksSpawn(t *testing.T) {
	undetermined := refinery.NewForkRigConfigError("gastown", errors.New("config.json missing"))

	spawn, reason := refineryQueueGate(undetermined, nil, 3)
	if spawn {
		t.Fatal("expected skip when fork-rig status cannot be determined")
	}
	if !errors.Is(undetermined, refinery.ErrForkRigUndetermined) {
		t.Fatalf("test fixture is not an undetermined-fork error: %v", undetermined)
	}
	if !strings.Contains(reason, "start guard") {
		t.Errorf("reason should name the guard, got %q", reason)
	}
}

// A refinery cannot merge anything while beads is unreadable, so an unreadable
// queue skips the spawn — but says why, so the skip is not silent.
func TestRefineryQueueGate_UnreadableQueueBlocksSpawn(t *testing.T) {
	spawn, reason := refineryQueueGate(nil, errors.New("dial tcp 127.0.0.1:3307: connection refused"), 0)
	if spawn {
		t.Fatal("expected skip when the merge queue cannot be read")
	}
	if !strings.Contains(reason, "connection refused") {
		t.Errorf("reason should carry the underlying error, got %q", reason)
	}
}

// refineryQueueDepth must reach the fork guard before it reaches beads: a
// fork-backed rig costs no queue query and reports the guard error.
func TestRefineryQueueDepth_ForkRigShortCircuitsBeforeQueueQuery(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"name":"gastown","upstream_url":"https://github.com/gastownhall/gastown.git"}`
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := refinery.NewManager(&rig.Rig{Name: "gastown", Path: rigPath})

	forkGuardErr, depth, queueErr := refineryQueueDepth(mgr)
	if forkGuardErr == nil {
		t.Fatal("expected the fork-rig guard to reject a rig config with upstream_url")
	}
	if !errors.Is(forkGuardErr, refinery.ErrForkRig) {
		t.Errorf("expected ErrForkRig, got %v", forkGuardErr)
	}
	if queueErr != nil || depth != 0 {
		t.Errorf("expected no queue query for a fork rig, got depth=%d err=%v", depth, queueErr)
	}

	if spawn, _ := refineryQueueGate(forkGuardErr, queueErr, depth); spawn {
		t.Fatal("fork-backed rig must never spawn a refinery from queue depth")
	}
}

// A rig whose config.json is missing entirely is undetermined, not "local".
func TestRefineryQueueDepth_MissingRigConfigIsUndetermined(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "norig")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := refinery.NewManager(&rig.Rig{Name: "norig", Path: rigPath})

	forkGuardErr, depth, queueErr := refineryQueueDepth(mgr)
	if !errors.Is(forkGuardErr, refinery.ErrForkRigUndetermined) {
		t.Fatalf("expected ErrForkRigUndetermined for a missing rig config, got %v", forkGuardErr)
	}
	if spawn, _ := refineryQueueGate(forkGuardErr, queueErr, depth); spawn {
		t.Fatal("undetermined fork status must fail closed")
	}
}
