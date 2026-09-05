package nudge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func enqueueForTest(t *testing.T, townRoot, session string, n QueuedNudge) {
	t.Helper()
	if err := Enqueue(townRoot, session, n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

// TestRequeueHookOnlyMarksBatch is the core of the deliver-once-or-fail rule
// (gt-sve): a batch whose tmux injection failed goes back to the queue marked,
// so no injector types it at a pane a second time.
func TestRequeueHookOnlyMarksBatch(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	batch := []QueuedNudge{
		{Sender: "gastown/witness", Message: "first", Priority: PriorityNormal, Timestamp: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
		{Sender: "gastown/mayor", Message: "second", Priority: PriorityUrgent, Timestamp: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := RequeueHookOnly(townRoot, session, batch); err != nil {
		t.Fatalf("RequeueHookOnly: %v", err)
	}

	// The hook drain still sees everything — an undelivered nudge is not
	// noise, and dropping it is what silently deletes the reply channel.
	drained, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(drained) != len(batch) {
		t.Fatalf("Drain returned %d nudges, want %d", len(drained), len(batch))
	}
	for _, n := range drained {
		if !n.InjectionFailed {
			t.Errorf("nudge %q: InjectionFailed = false, want true", n.Message)
		}
	}

	// RequeueHookOnly must not mutate the caller's slice.
	for _, n := range batch {
		if n.InjectionFailed {
			t.Error("RequeueHookOnly mutated the caller's batch")
		}
	}
}

// TestDrainInjectableSkipsFailedInjections is the property that stops the
// restaging loop: the poller re-reads the queue every interval, and must not
// pick the batch back up.
func TestDrainInjectableSkipsFailedInjections(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	enqueueForTest(t, townRoot, session, QueuedNudge{Sender: "a", Message: "fresh"})
	if err := RequeueHookOnly(townRoot, session, []QueuedNudge{
		{Sender: "b", Message: "already tried", Timestamp: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
	}); err != nil {
		t.Fatalf("RequeueHookOnly: %v", err)
	}

	drained, err := DrainInjectable(townRoot, session)
	if err != nil {
		t.Fatalf("DrainInjectable: %v", err)
	}
	if len(drained) != 1 || drained[0].Message != "fresh" {
		t.Fatalf("DrainInjectable = %+v, want only the fresh nudge", drained)
	}

	// The skipped entry is unclaimed, not consumed: still there for the hook.
	rest, err := Drain(townRoot, session)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(rest) != 1 || rest[0].Message != "already tried" {
		t.Fatalf("Drain after DrainInjectable = %+v, want the marked nudge", rest)
	}
}

// TestDrainInjectableLeavesNoClaimedFiles guards the unclaim path: a filtered
// nudge that stayed renamed to .claimed would be invisible to the hook drain
// for staleClaimThreshold, turning a skip into a delay.
func TestDrainInjectableLeavesNoClaimedFiles(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	if err := RequeueHookOnly(townRoot, session, []QueuedNudge{
		{Sender: "b", Message: "marked", Timestamp: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
	}); err != nil {
		t.Fatalf("RequeueHookOnly: %v", err)
	}
	if _, err := DrainInjectable(townRoot, session); err != nil {
		t.Fatalf("DrainInjectable: %v", err)
	}

	entries, err := os.ReadDir(queueDir(townRoot, session))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".claimed") {
			t.Errorf("filtered nudge left a claimed file: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("queue has %d entries, want 1", len(entries))
	}
}

// TestPendingInjectableExcludesFailed keeps the poller from spinning: with
// Pending, a queue holding only marked entries reads as work, so every cycle
// waits for idle and drains nothing until the TTL runs out.
func TestPendingInjectableExcludesFailed(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	if err := RequeueHookOnly(townRoot, session, []QueuedNudge{
		{Sender: "b", Message: "marked", Timestamp: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
	}); err != nil {
		t.Fatalf("RequeueHookOnly: %v", err)
	}

	if n, err := Pending(townRoot, session); err != nil || n != 1 {
		t.Fatalf("Pending = %d, %v; want 1, nil", n, err)
	}
	if n, err := PendingInjectable(townRoot, session); err != nil || n != 0 {
		t.Fatalf("PendingInjectable = %d, %v; want 0, nil", n, err)
	}

	enqueueForTest(t, townRoot, session, QueuedNudge{Sender: "a", Message: "fresh"})
	if n, err := PendingInjectable(townRoot, session); err != nil || n != 1 {
		t.Fatalf("PendingInjectable after fresh enqueue = %d, %v; want 1, nil", n, err)
	}
}

func TestPendingInjectableMissingQueue(t *testing.T) {
	townRoot := t.TempDir()
	n, err := PendingInjectable(townRoot, "never-used")
	if err != nil || n != 0 {
		t.Fatalf("PendingInjectable on missing queue = %d, %v; want 0, nil", n, err)
	}
}

// TestInjectionFailedSurvivesRoundTrip pins the field to the on-disk JSON.
// The mark has to outlive the writing process — the poller that failed the
// injection is not necessarily the one that reads the entry next.
func TestInjectionFailedSurvivesRoundTrip(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	if err := RequeueHookOnly(townRoot, session, []QueuedNudge{
		{Sender: "b", Message: "marked", Timestamp: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
	}); err != nil {
		t.Fatalf("RequeueHookOnly: %v", err)
	}

	entries, err := os.ReadDir(queueDir(townRoot, session))
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(queueDir(townRoot, session), entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"injection_failed": true`) {
		t.Errorf("queued JSON missing injection_failed marker:\n%s", data)
	}
}

// TestRequeueHookOnlyDropsExpired inherits Requeue's expiry rule: a nudge past
// its TTL is stale by the time anything would read it.
func TestRequeueHookOnlyDropsExpired(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-witness"

	if err := RequeueHookOnly(townRoot, session, []QueuedNudge{
		{Sender: "b", Message: "expired", Timestamp: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("RequeueHookOnly: %v", err)
	}
	if n, err := Pending(townRoot, session); err != nil || n != 0 {
		t.Fatalf("Pending = %d, %v; want 0, nil", n, err)
	}
}
