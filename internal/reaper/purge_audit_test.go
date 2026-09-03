package reaper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The reaper is the largest wisp deleter in the tree: it removes rows from the
// wisps family by direct SQL, and that family is in dolt_ignore, so nothing it
// deletes is in any Dolt commit and no AS OF reads it back. These tests hold the
// two properties that make that survivable — the record is written BEFORE the
// delete, and a delete whose record will not write does not happen (hq-6ewp).

// purgeTownRoot makes cwd a Gas Town workspace and returns the events file path.
func purgeTownRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	t.Chdir(root)
	return filepath.Join(root, ".events.jsonl")
}

func TestPurgeRecordsEveryWispBeforeDeletingIt(t *testing.T) {
	eventsPath := purgeTownRoot(t)
	state := &fakePurgeState{
		eventsPath: eventsPath,
		wisps: map[string]string{
			"hq-wisp-aaa": "deacon patrol cycle 4",
			"hq-wisp-bbb": "witness patrol cycle 9",
		},
	}
	db := openFakePurgeDB(t, state)

	result, err := Purge(db, "hq", 7*24*time.Hour, 7*24*time.Hour, false)
	if err != nil {
		t.Fatalf("Purge() = %v", err)
	}
	if result.WispsPurged != 2 {
		t.Fatalf("purged %d wisps, want 2", result.WispsPurged)
	}

	// The delete must have found the record already on disk. Anything written
	// afterwards is lost exactly when it matters most — a crash mid-purge.
	if !state.sawDelete {
		t.Fatal("no delete was observed")
	}
	for _, id := range []string{"hq-wisp-aaa", "hq-wisp-bbb"} {
		if !strings.Contains(state.eventsAtDelete, id) {
			t.Errorf("%s was deleted before it was recorded; the deletion is unrecoverable and now also unauditable", id)
		}
	}

	planned := findPlannedRecord(t, eventsPath)
	if planned == nil {
		t.Fatal("no planned wisp_purge record was written")
	}
	if planned["path"] != "reaper: purge closed wisps" {
		t.Errorf("path = %v, want the reaper named as the deleter", planned["path"])
	}
	if planned["db"] != "hq" {
		t.Errorf("db = %v, want hq", planned["db"])
	}
	// Titles, not just ids: an id names a row that no longer exists anywhere.
	if !strings.Contains(fmt.Sprint(planned["wisps"]), "deacon patrol cycle 4") {
		t.Errorf("wisps = %v, want each wisp's title alongside its id", planned["wisps"])
	}
}

func TestPurgeDeletesNothingWhenTheRecordCannotBeWritten(t *testing.T) {
	// Not a Gas Town workspace, and TestMain has stripped the GT_TOWN_ROOT
	// fallback: there is nowhere durable for the record to land.
	t.Chdir(t.TempDir())

	state := &fakePurgeState{wisps: map[string]string{"hq-wisp-aaa": "deacon patrol cycle 4"}}
	db := openFakePurgeDB(t, state)

	_, err := Purge(db, "hq", 7*24*time.Hour, 7*24*time.Hour, false)
	if err == nil {
		t.Fatal("Purge() = nil; an unrecordable deletion must fail rather than proceed silently")
	}
	if !strings.Contains(err.Error(), "could not be recorded") {
		t.Errorf("Purge() = %v, want an error naming the unwritable record", err)
	}
	if state.deleted > 0 {
		t.Errorf("deleted %d wisps with no durable record of what went", state.deleted)
	}
}

// A dry run decides nothing and deletes nothing, so it writes no record either.
func TestPurgeDryRunWritesNoRecordAndDeletesNothing(t *testing.T) {
	eventsPath := purgeTownRoot(t)
	state := &fakePurgeState{eventsPath: eventsPath, wisps: map[string]string{"hq-wisp-aaa": "cycle"}}
	db := openFakePurgeDB(t, state)

	if _, err := Purge(db, "hq", 7*24*time.Hour, 7*24*time.Hour, true); err != nil {
		t.Fatalf("Purge(dryRun) = %v", err)
	}
	if state.deleted != 0 {
		t.Errorf("dry run deleted %d wisps", state.deleted)
	}
	if _, err := os.Stat(eventsPath); err == nil {
		t.Error("dry run wrote a deletion record for a deletion that did not happen")
	}
}

func findPlannedRecord(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			Type    string                 `json:"type"`
			Payload map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parsing record %q: %v", line, err)
		}
		if e.Type == "wisp_purge" && e.Payload["phase"] == "planned" {
			return e.Payload
		}
	}
	return nil
}

// --- a fake SQL driver covering just the purge path -------------------------
//
// Separate from reaper_test.go's Reap fake on purpose: that one answers the
// eligibility queries and would have to grow a second personality to answer
// these. Two small fakes read better than one that does everything.

type fakePurgeState struct {
	mu sync.Mutex

	// wisps is the closed-and-expired population, id -> title.
	wisps map[string]string
	// deleted counts rows removed from the wisps table.
	deleted int

	// eventsPath, when set, is snapshotted at the moment of the primary
	// DELETE, so the test can assert the record was already there.
	eventsPath     string
	sawDelete      bool
	eventsAtDelete string
}

func (s *fakePurgeState) idsLocked() []string {
	ids := make([]string, 0, len(s.wisps))
	for id := range s.wisps {
		ids = append(ids, id)
	}
	// Deterministic order: the population is tiny and the test names both.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

func openFakePurgeDB(t *testing.T, state *fakePurgeState) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("fake-purge-%s", t.Name())
	sql.Register(driverName, &fakePurgeDriver{state: state})
	db, err := sql.Open(driverName, "fake")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type fakePurgeDriver struct{ state *fakePurgeState }

func (d *fakePurgeDriver) Open(string) (driver.Conn, error) {
	return &fakePurgeConn{state: d.state}, nil
}

type fakePurgeConn struct{ state *fakePurgeState }

func (c *fakePurgeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}
func (c *fakePurgeConn) Close() error                             { return nil }
func (c *fakePurgeConn) Begin() (driver.Tx, error)                { return fakePurgeTx{}, nil }
func (c *fakePurgeConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fakePurgeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.Join(strings.Fields(query), " ")
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	switch {
	// The digest: how many closed wisps are past the cutoff.
	case strings.Contains(q, "COALESCE(w.wisp_type, 'unknown')"):
		return &fakePurgeRows{
			cols: []string{"wtype", "cnt"},
			rows: [][]driver.Value{{"patrol", int64(len(c.state.wisps))}},
		}, nil

	// The batch id query. The population empties as it is deleted, which ends
	// batchDeleteRows' loop.
	case strings.Contains(q, "SELECT w.id FROM wisps w"):
		ids := c.state.idsLocked()
		rows := make([][]driver.Value, len(ids))
		for i, id := range ids {
			rows[i] = []driver.Value{id}
		}
		return &fakePurgeRows{cols: []string{"id"}, rows: rows}, nil

	// The title lookup the deletion record uses.
	case strings.Contains(q, "SELECT id, title FROM wisps WHERE id IN"):
		ids := c.state.idsLocked()
		rows := make([][]driver.Value, len(ids))
		for i, id := range ids {
			rows[i] = []driver.Value{id, c.state.wisps[id]}
		}
		return &fakePurgeRows{cols: []string{"id", "title"}, rows: rows}, nil

	// Mail purge: nothing to do, and not this test's subject.
	case strings.Contains(q, "SELECT COUNT(*)"):
		return &fakePurgeRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", q)
}

func (c *fakePurgeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	q := strings.Join(strings.Fields(query), " ")
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	switch {
	case strings.HasPrefix(q, "DELETE FROM `wisps`"):
		// Snapshot the log as the delete happens. This is the ordering
		// assertion: what is on disk at this instant is all that survives.
		c.state.sawDelete = true
		if c.state.eventsPath != "" {
			data, _ := os.ReadFile(c.state.eventsPath)
			c.state.eventsAtDelete = string(data)
		}
		n := len(c.state.wisps)
		c.state.deleted += n
		c.state.wisps = map[string]string{}
		return fakePurgeResult(n), nil

	case strings.HasPrefix(q, "DELETE FROM"):
		return fakePurgeResult(0), nil // aux and reverse-dependency cleanup

	case q == "SET @@autocommit = 0" || q == "SET @@autocommit = 1" ||
		q == "COMMIT" || q == "ROLLBACK" || strings.HasPrefix(q, "CALL DOLT_COMMIT"):
		return fakePurgeResult(0), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s", q)
}

type fakePurgeTx struct{}

func (fakePurgeTx) Commit() error   { return nil }
func (fakePurgeTx) Rollback() error { return nil }

type fakePurgeResult int64

func (r fakePurgeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakePurgeResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakePurgeRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func (r *fakePurgeRows) Columns() []string { return r.cols }
func (r *fakePurgeRows) Close() error      { return nil }
func (r *fakePurgeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}
