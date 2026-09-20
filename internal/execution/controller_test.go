package execution

import (
	"errors"
	"os"
	"testing"
	"time"
)

func controllerCommand(operation, id, key string, epoch uint64, at, until time.Time) ControllerCommand {
	return ControllerCommand{
		Operation: operation, ControllerID: id, ExpectedEpoch: epoch,
		IdempotencyKey: key, Actor: "test", At: at, LeaseExpiresAt: until,
	}
}

func TestControllerLeaseFencesConcurrentAndStaleOwners(t *testing.T) {
	store := NewStore(t.TempDir())
	now := fixedTime(1)
	first, err := store.ApplyController(controllerCommand("acquire", "controller-a", "acquire-a", 0, now, now.Add(time.Minute)))
	if err != nil || first.Epoch != 1 || !first.Active(now) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	_, err = store.ApplyController(controllerCommand("acquire", "controller-b", "acquire-b-early", 0, now.Add(30*time.Second), now.Add(2*time.Minute)))
	if !errors.Is(err, ErrControllerLeaseHeld) {
		t.Fatalf("concurrent acquire error=%v", err)
	}
	second, err := store.ApplyController(controllerCommand("acquire", "controller-b", "acquire-b", 0, now.Add(2*time.Minute), now.Add(3*time.Minute)))
	if err != nil || second.Epoch != 2 || second.ControllerID != "controller-b" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	_, err = store.ApplyController(controllerCommand("renew", "controller-a", "stale-renew", 1, now.Add(2*time.Minute), now.Add(4*time.Minute)))
	if !errors.Is(err, ErrFenceMismatch) {
		t.Fatalf("stale renew error=%v", err)
	}
}

func TestControllerLeaseRenewReleaseAndIdempotency(t *testing.T) {
	store := NewStore(t.TempDir())
	now := fixedTime(1)
	acquire := controllerCommand("acquire", "controller-a", "acquire", 0, now, now.Add(time.Minute))
	first, err := store.ApplyController(acquire)
	if err != nil {
		t.Fatal(err)
	}
	acquire.At = now.Add(10 * time.Second)
	retry, err := store.ApplyController(acquire)
	if err != nil || retry.Revision != first.Revision {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	renewed, err := store.ApplyController(controllerCommand("renew", "controller-a", "renew", 1, now.Add(30*time.Second), now.Add(2*time.Minute)))
	if err != nil || renewed.Revision != 2 || !renewed.LeaseExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("renewed=%+v err=%v", renewed, err)
	}
	released, err := store.ApplyController(controllerCommand("release", "controller-a", "release", 1, now.Add(40*time.Second), time.Time{}))
	if err != nil || released.ControllerID != "" || released.Active(now.Add(40*time.Second)) {
		t.Fatalf("released=%+v err=%v", released, err)
	}
	verification, err := store.VerifyController()
	if err != nil || verification.Events != 3 || verification.Epoch != 1 || verification.Revision != 3 {
		t.Fatalf("verification=%+v err=%v", verification, err)
	}
}

func TestControllerJournalRepairsSnapshotAndDetectsTampering(t *testing.T) {
	town := t.TempDir()
	store := NewStore(town)
	now := fixedTime(1)
	if _, err := store.ApplyController(controllerCommand("acquire", "controller-a", "acquire", 0, now, now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.controllerSnapshotPath()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadController()
	if err != nil || loaded.ControllerID != "controller-a" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	file, err := os.OpenFile(store.controllerJournalPath(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := store.LoadController(); err == nil {
		t.Fatal("tampered controller journal was accepted")
	}
}

func TestControllerLeaseRejectsNonExtendingRenewalAndKeyReuse(t *testing.T) {
	store := NewStore(t.TempDir())
	now := fixedTime(1)
	if _, err := store.ApplyController(controllerCommand("acquire", "controller-a", "shared", 0, now, now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	_, err := store.ApplyController(controllerCommand("renew", "controller-a", "renew-short", 1, now.Add(10*time.Second), now.Add(30*time.Second)))
	if err == nil {
		t.Fatal("non-extending renewal was accepted")
	}
	_, err = store.ApplyController(controllerCommand("renew", "controller-a", "shared", 1, now.Add(10*time.Second), now.Add(2*time.Minute)))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("key reuse error=%v", err)
	}
}

func TestControllerIdempotentResultCannotResurrectSupersededEpoch(t *testing.T) {
	store := NewStore(t.TempDir())
	now := fixedTime(1)
	oldAcquire := controllerCommand("acquire", "controller-a", "acquire-a", 0, now, now.Add(time.Minute))
	if _, err := store.ApplyController(oldAcquire); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyController(controllerCommand("acquire", "controller-b", "acquire-b", 0, now.Add(2*time.Minute), now.Add(3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	oldAcquire.At = now.Add(2 * time.Minute)
	if _, err := store.ApplyController(oldAcquire); !errors.Is(err, ErrFenceMismatch) {
		t.Fatalf("superseded idempotent acquire error=%v", err)
	}
}
