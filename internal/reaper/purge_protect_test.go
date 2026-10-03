package reaper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// gt-12f / cl-kf00: the reaper's unconditional, age-based wisp purge must
// never remove a merge-request bead or a bead carrying a compliance-seat
// comment. These tests hold that property against the real SQL
// purgeClosedWisps sends — not just a static string copy of it — by failing
// the query outright if the protected-bead exclusion clause is missing, and
// by actually withholding protected rows from the delete.

func TestPurgeExcludesProtectedBeads(t *testing.T) {
	eventsPath := purgeTownRoot(t)
	state := &fakeProtectState{
		eventsPath: eventsPath,
		wisps: map[string]string{
			"cl-wisp-mr":    "Merge: cl-abc",
			"cl-wisp-cc":    "mol-witness-patrol step 3",
			"cl-wisp-plain": "mol-polecat-work step 1",
		},
		labelled:   map[string]bool{"cl-wisp-mr": true},
		compliance: map[string]bool{"cl-wisp-cc": true},
	}
	db := openFakeProtectDB(t, state)

	result, err := Purge(db, "ccm", 7*24*time.Hour, 7*24*time.Hour, false)
	if err != nil {
		t.Fatalf("Purge() = %v", err)
	}
	if result.WispsPurged != 1 {
		t.Fatalf("purged %d wisps, want 1 (only the unprotected one)", result.WispsPurged)
	}

	if _, stillThere := state.wisps["cl-wisp-mr"]; !stillThere {
		t.Error("a gt:merge-request labelled wisp was purged — cl-kf00 requires it survive")
	}
	if _, stillThere := state.wisps["cl-wisp-cc"]; !stillThere {
		t.Error("a compliance-seat-commented wisp was purged — cl-kf00 requires it survive")
	}
	if _, stillThere := state.wisps["cl-wisp-plain"]; stillThere {
		t.Error("an unprotected closed wisp was not purged")
	}
}

func TestPurgeDryRunExcludesProtectedBeadsFromTheCount(t *testing.T) {
	eventsPath := purgeTownRoot(t)
	state := &fakeProtectState{
		eventsPath: eventsPath,
		wisps: map[string]string{
			"cl-wisp-mr":    "Merge: cl-abc",
			"cl-wisp-plain": "mol-polecat-work step 1",
		},
		labelled: map[string]bool{"cl-wisp-mr": true},
	}
	db := openFakeProtectDB(t, state)

	result, err := Purge(db, "ccm", 7*24*time.Hour, 7*24*time.Hour, true)
	if err != nil {
		t.Fatalf("Purge(dryRun) = %v", err)
	}
	if result.WispsPurged != 1 {
		t.Fatalf("dry-run digest = %d, want 1 (the protected bead must not even be counted as a candidate)", result.WispsPurged)
	}
}

// --- a fake SQL driver that enforces the protected-bead exclusion clause ----

type fakeProtectState struct {
	wisps      map[string]string
	labelled   map[string]bool // id -> has gt:merge-request label
	compliance map[string]bool // id -> has a compliance-seat comment
	eventsPath string
}

func (s *fakeProtectState) unprotectedIDs() []string {
	var ids []string
	for id := range s.wisps {
		if s.labelled[id] || s.compliance[id] {
			continue
		}
		ids = append(ids, id)
	}
	// Deterministic order for a tiny, named population.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

func openFakeProtectDB(t *testing.T, state *fakeProtectState) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("fake-protect-%s", t.Name())
	sql.Register(driverName, &fakeProtectDriver{state: state})
	db, err := sql.Open(driverName, "fake")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type fakeProtectDriver struct{ state *fakeProtectState }

func (d *fakeProtectDriver) Open(string) (driver.Conn, error) {
	return &fakeProtectConn{state: d.state}, nil
}

type fakeProtectConn struct{ state *fakeProtectState }

func (c *fakeProtectConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}
func (c *fakeProtectConn) Close() error                             { return nil }
func (c *fakeProtectConn) Begin() (driver.Tx, error)                { return fakeProtectTx{}, nil }
func (c *fakeProtectConn) CheckNamedValue(*driver.NamedValue) error { return nil }

// requireExclusionClause fails the query outright when the production SQL did
// not carry the protected-bead exclusion — this is the regression guard: if
// the clause is ever dropped from purgeClosedWisps, these tests fail with a
// clear cause instead of quietly passing.
func requireExclusionClause(q string) error {
	if !strings.Contains(q, "wisp_labels") || !strings.Contains(q, "wisp_comments") {
		return fmt.Errorf("query is missing the gt-12f protected-bead exclusion (wisp_labels/wisp_comments): %s", q)
	}
	return nil
}

func (c *fakeProtectConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.Join(strings.Fields(query), " ")

	switch {
	case strings.Contains(q, "COALESCE(w.wisp_type, 'unknown')"):
		if err := requireExclusionClause(q); err != nil {
			return nil, err
		}
		return &fakeProtectRows{
			cols: []string{"wtype", "cnt"},
			rows: [][]driver.Value{{"patrol", int64(len(c.state.unprotectedIDs()))}},
		}, nil

	case strings.Contains(q, "SELECT w.id FROM wisps w"):
		if err := requireExclusionClause(q); err != nil {
			return nil, err
		}
		ids := c.state.unprotectedIDs()
		rows := make([][]driver.Value, len(ids))
		for i, id := range ids {
			rows[i] = []driver.Value{id}
		}
		return &fakeProtectRows{cols: []string{"id"}, rows: rows}, nil

	case strings.Contains(q, "SELECT id, title FROM wisps WHERE id IN"):
		ids := c.state.unprotectedIDs()
		rows := make([][]driver.Value, len(ids))
		for i, id := range ids {
			rows[i] = []driver.Value{id, c.state.wisps[id]}
		}
		return &fakeProtectRows{cols: []string{"id", "title"}, rows: rows}, nil

	case strings.Contains(q, "SELECT id FROM wisps WHERE id IN"):
		// gt-12f round 2 post-delete verification: by the time this runs, the
		// DELETE above has already removed the purged ids from state.wisps, so
		// nothing in the queried set should still be present.
		return &fakeProtectRows{cols: []string{"id"}}, nil

	case strings.Contains(q, "SELECT COUNT(*)"):
		return &fakeProtectRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", q)
}

func (c *fakeProtectConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	q := strings.Join(strings.Fields(query), " ")

	switch {
	case strings.HasPrefix(q, "DELETE FROM `wisps`"):
		deleted := 0
		for _, id := range c.state.unprotectedIDs() {
			delete(c.state.wisps, id)
			deleted++
		}
		return fakeProtectResult(deleted), nil

	case strings.HasPrefix(q, "DELETE FROM"):
		return fakeProtectResult(0), nil // aux and reverse-dependency cleanup

	case q == "SET @@autocommit = 0" || q == "SET @@autocommit = 1" ||
		q == "COMMIT" || q == "ROLLBACK" || strings.HasPrefix(q, "CALL DOLT_COMMIT"):
		return fakeProtectResult(0), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s", q)
}

type fakeProtectTx struct{}

func (fakeProtectTx) Commit() error   { return nil }
func (fakeProtectTx) Rollback() error { return nil }

type fakeProtectResult int64

func (r fakeProtectResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeProtectResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeProtectRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func (r *fakeProtectRows) Columns() []string { return r.cols }
func (r *fakeProtectRows) Close() error      { return nil }
func (r *fakeProtectRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}
