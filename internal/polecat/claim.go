package polecat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// polecatClaimTTL bounds how long a claim marker blocks reuse if the claiming
// process dies before clearing it (crash, kill -9, host loss). It only needs
// to cover the reuse-to-hook-attach window, not a whole polecat lifetime.
const polecatClaimTTL = 10 * time.Minute

// polecatClaim is a durable, file-based reservation for a polecat slot,
// written while the per-polecat lock is held (see lockPolecat). It exists
// because FindIdlePolecat's "is this slot idle" read and ReuseIdlePolecat's
// own reusability read both go through beads (Dolt), and there is a window —
// between a winner finishing ReuseIdlePolecat/AllocateAndAdd and the caller
// (gt sling) marking the work bead itself as hooked — where the slot still
// reads back as idle to anyone re-querying beads. Without a local claim
// marker, a second concurrent sling can pass the same reusability check
// again and silently clobber the winner's branch/hook_bead (cl-30m).
type polecatClaim struct {
	HookBead  string    `json:"hook_bead"`
	ClaimedAt time.Time `json:"claimed_at"`
	PID       int       `json:"pid"`
}

func (m *Manager) claimPath(name string) string {
	return filepath.Join(m.rig.Path, ".runtime", "locks", fmt.Sprintf("polecat-%s.claim", name))
}

// writeClaim durably marks name as claimed for hookBead. The caller MUST
// hold the per-polecat lock (lockPolecat) so the write is serialized against
// any concurrent reader/writer racing on the same name.
func (m *Manager) writeClaim(name, hookBead string) error {
	path := m.claimPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating claim dir: %w", err)
	}
	data, err := json.Marshal(polecatClaim{
		HookBead:  hookBead,
		ClaimedAt: time.Now().UTC(),
		PID:       os.Getpid(),
	})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing claim: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publishing claim: %w", err)
	}
	return nil
}

// clearClaim releases a claim early (e.g. once the work bead is confirmed
// hooked, or the polecat genuinely returns to idle). Best-effort: a missing
// claim file is not an error.
func (m *Manager) clearClaim(name string) {
	_ = os.Remove(m.claimPath(name))
}

// activeClaim returns the current non-stale claim for name, or nil if there
// is none. A claim older than polecatClaimTTL is treated as abandoned (the
// claiming process died before clearing it) and reported as absent so the
// slot is not stranded forever.
func (m *Manager) activeClaim(name string) *polecatClaim {
	data, err := os.ReadFile(m.claimPath(name))
	if err != nil {
		return nil
	}
	var c polecatClaim
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if c.ClaimedAt.IsZero() || time.Since(c.ClaimedAt) > polecatClaimTTL {
		return nil
	}
	return &c
}
