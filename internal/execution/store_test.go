package execution

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreReplayRepairsSnapshot(t *testing.T) {
	town := t.TempDir()
	store := NewStore(town)
	created, err := store.Create(CreateRequest{WorkID: "work/with/slashes", IdempotencyKey: "create-1", At: fixedTime(1)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Apply(created.WorkID, Command{
		Operation: "claim", IdempotencyKey: "claim-1", ExecutionID: "exec-1", At: fixedTime(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.snapshotPath(created.WorkID)); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(created.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != claimed.Revision || loaded.State != StateClaimed {
		t.Fatalf("replayed record revision=%d state=%s", loaded.Revision, loaded.State)
	}
	data, err := os.ReadFile(store.snapshotPath(created.WorkID))
	if err != nil {
		t.Fatalf("snapshot was not repaired: %v", err)
	}
	var snapshot Record
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Revision != claimed.Revision {
		t.Fatalf("repaired snapshot invalid: revision=%d err=%v", snapshot.Revision, err)
	}
}

func TestStoreCreateRetryAndConcurrentSerialization(t *testing.T) {
	store := NewStore(t.TempDir())
	req := CreateRequest{WorkID: "work-1", Rig: "rig", IdempotencyKey: "create-1", At: fixedTime(1)}
	first, err := store.Create(req)
	if err != nil {
		t.Fatal(err)
	}
	req.At = fixedTime(8)
	retry, err := store.Create(req)
	if err != nil || retry.Revision != first.Revision {
		t.Fatalf("create retry revision=%d err=%v", retry.Revision, err)
	}

	if _, err := store.Create(CreateRequest{WorkID: "work-1", Rig: "different", IdempotencyKey: "create-2"}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create error=%v, want ErrAlreadyExists", err)
	}

	if _, err := store.Apply("work-1", Command{Operation: "claim", IdempotencyKey: "claim", ExecutionID: "exec", At: fixedTime(2)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Apply("work-1", Command{
				Operation: "heartbeat", IdempotencyKey: "same-heartbeat", ExecutionID: "exec",
				Generation: 1, At: fixedTime(3),
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	verified, err := store.Verify("work-1")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Events != 3 { // create, claim, one idempotent heartbeat
		t.Fatalf("events=%d, want 3", verified.Events)
	}
}

func TestStoreDetectsJournalTampering(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Create(CreateRequest{WorkID: "work-1", IdempotencyKey: "create-1", At: fixedTime(1)}); err != nil {
		t.Fatal(err)
	}
	path := store.journalPath("work-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	event.Record.Rig = "tampered"
	tampered, _ := json.Marshal(event)
	if err := os.WriteFile(path, append(tampered, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("work-1"); err == nil {
		t.Fatal("Load() succeeded for tampered journal")
	}
}

func TestStoreListUsesJournals(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, id := range []string{"b", "a"} {
		if _, err := store.Create(CreateRequest{WorkID: id, IdempotencyKey: "create-" + id, At: fixedTime(1)}); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(store.root, "records", storageName(id)+".json"))
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].WorkID != "a" || records[1].WorkID != "b" {
		t.Fatalf("records=%v", records)
	}
}
