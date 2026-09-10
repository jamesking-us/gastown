package daemon

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/refinery"
)

// refineryQueueGate decides whether a non-empty merge queue should spawn a
// refinery for a rig that has NO pending refinery event and NO running
// refinery session.
//
// Why a second trigger exists (gt-tyu): the refinery event channel is an edge
// signal, and a lossy one — `gt mq submit` has been observed to create the MR
// bead while emitting no refinery event at all. The daemon gated spawning on
// that edge alone, so a ready P1 merge request sat unmerged for 3+ hours with
// its source bead already closed and no refinery ever spawned. Queue depth is
// the durable, level-triggered fact: the MR bead exists until it is merged or
// rejected. An edge signal cannot be made reliable by retrying it, so the
// queue is consulted whenever no event is pending.
//
// The gate is a pure function of already-collected inputs so the trigger can
// be tested without starting a refinery:
//
//   - forkGuardErr is the result of refinery.Manager.ForkRigStartError(). It
//     MUST be the same guard the event-driven path ends up applying inside
//     Manager.Start(); a queue-depth trigger that skipped it would auto-start
//     refineries on fork-backed rigs every heartbeat, by design, re-creating
//     gt-kx4 (guard failed open on a missing rig config, gastown's refinery
//     auto-started with no --force). Any non-nil error — including
//     ErrForkRigUndetermined from an unreadable config — blocks the spawn,
//     matching the fail-closed rule of gt-9gv.
//   - queueErr is the error from reading the merge queue. It also blocks:
//     a refinery cannot merge anything while beads is unreadable, so spawning
//     one would burn a session for nothing. The reason string carries the
//     error so the skip is diagnosable in daemon.log rather than silent.
//
// The returned reason is always non-empty and is logged by the caller.
func refineryQueueGate(forkGuardErr, queueErr error, queueDepth int) (spawn bool, reason string) {
	switch {
	case forkGuardErr != nil:
		return false, fmt.Sprintf("refinery start guard: %v", forkGuardErr)
	case queueErr != nil:
		return false, fmt.Sprintf("merge queue unreadable: %v", queueErr)
	case queueDepth <= 0:
		return false, "merge queue empty"
	default:
		return true, fmt.Sprintf("merge queue has %d ready merge request(s)", queueDepth)
	}
}

// refineryQueueDepth reports how many merge requests are waiting in the rig's
// merge queue, along with the fork-rig guard verdict for that rig.
//
// Both are returned together because the gate needs both and neither is worth
// computing without the other: the guard is checked first so a fork-backed rig
// costs no beads query at all on this path.
func refineryQueueDepth(mgr *refinery.Manager) (forkGuardErr error, depth int, queueErr error) {
	if err := mgr.ForkRigStartError(); err != nil {
		return err, 0, nil
	}
	items, err := mgr.Queue()
	if err != nil {
		return nil, 0, err
	}
	return nil, len(items), nil
}
