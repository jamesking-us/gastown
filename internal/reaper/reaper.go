// Package reaper provides wisp and issue cleanup operations for Dolt databases.
//
// These functions are the "callable helper functions" for the Dog-driven
// mol-dog-reaper formula. They execute SQL operations but do not make
// eligibility decisions — the Dog (or daemon orchestrator) decides what
// to reap, purge, and auto-close based on the formula.
package reaper

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/wispaudit"
)

// validDBName matches safe database names (alphanumeric, underscore, hyphen).
var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// DefaultDatabases is the static fallback list of known production databases.
// Used only when SHOW DATABASES fails (server unreachable).
// GH#2385: Removed legacy "gt" and "bd" names — modern towns use "hq" (town
// beads) and rig-specific names. Those databases no longer exist in most
// installations and their presence in the fallback caused phantom DB errors.
var DefaultDatabases = []string{"hq"}

// testPollutionPrefixes are database name prefixes created by tests.
var testPollutionPrefixes = []string{"testdb_", "beads_t", "beads_pt", "doctest_"}

// isNothingToCommit returns true if the error is a Dolt "nothing to commit" error.
func isNothingToCommit(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "nothing to commit")
}

// isTableNotFound returns true if the error indicates a missing table.
// This happens when beads stores its data on a separate Dolt instance from
// the gt Dolt server, so tables like issues/labels/dependencies don't exist
// on the server the reaper connects to.
func isTableNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "table not found") || strings.Contains(msg, "doesn't exist")
}

// DiscoverDatabases queries SHOW DATABASES on the Dolt server and returns
// all production databases, filtering out system databases and test pollution.
// Falls back to DefaultDatabases on any error.
func DiscoverDatabases(host string, port int) []string {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/?parseTime=true&timeout=5s", host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return DefaultDatabases
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return DefaultDatabases
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if name == "information_schema" || name == "mysql" {
			continue
		}
		lower := strings.ToLower(name)
		skip := false
		for _, prefix := range testPollutionPrefixes {
			if strings.HasPrefix(lower, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		databases = append(databases, name)
	}

	if len(databases) == 0 {
		return DefaultDatabases
	}
	return databases
}

// ScanResult holds the results of scanning a database for reaper candidates.
type ScanResult struct {
	Database               string    `json:"database"`
	ReapCandidates         int       `json:"reap_candidates"`
	MoleculeStepCandidates int       `json:"molecule_step_candidates,omitempty"`
	PurgeCandidates        int       `json:"purge_candidates"`
	MailCandidates         int       `json:"mail_candidates"`
	StaleCandidates        int       `json:"stale_candidates"`
	OpenWisps              int       `json:"open_wisps"`
	Anomalies              []Anomaly `json:"anomalies,omitempty"`
}

// ReapResult holds the results of a reap operation.
type ReapResult struct {
	Database            string    `json:"database"`
	Reaped              int       `json:"reaped"`
	MoleculeStepsClosed int       `json:"molecule_steps_closed,omitempty"`
	OpenRemain          int       `json:"open_remain"`
	DryRun              bool      `json:"dry_run,omitempty"`
	Anomalies           []Anomaly `json:"anomalies,omitempty"`
}

// PurgeResult holds the results of a purge operation.
type PurgeResult struct {
	Database    string    `json:"database"`
	WispsPurged int       `json:"wisps_purged"`
	MailPurged  int       `json:"mail_purged"`
	DryRun      bool      `json:"dry_run,omitempty"`
	Anomalies   []Anomaly `json:"anomalies,omitempty"`

	// WispsPurgedIDs and MailPurgedIDs name the exact candidates on a dry run,
	// capped at maxPurgePreviewIDs. gt-12f: a count alone is not "--dry-run
	// shows the purge" — only an id list lets a caller tell what was found
	// deletable before anything acts on it. Empty on a real (non-dry-run) purge.
	WispsPurgedIDs []string `json:"wisps_purged_ids,omitempty"`
	MailPurgedIDs  []string `json:"mail_purged_ids,omitempty"`
}

// ClosedEntry records an individual issue closure with details for logging.
type ClosedEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	AgeDays  int    `json:"age_days"`
	Database string `json:"database"`
}

// AutoCloseResult holds the results of an auto-close operation.
type AutoCloseResult struct {
	Database      string        `json:"database"`
	Closed        int           `json:"closed"`
	ClosedEntries []ClosedEntry `json:"closed_entries,omitempty"`
	DryRun        bool          `json:"dry_run,omitempty"`
	Anomalies     []Anomaly     `json:"anomalies,omitempty"`
}

// Anomaly represents an unexpected condition found during reaper operations.
type Anomaly struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

const (
	// DefaultQueryTimeout is the timeout for individual reaper SQL queries.
	DefaultQueryTimeout = 30 * time.Second
	// DefaultBatchSize is the number of rows per batch DELETE operation.
	DefaultBatchSize = 100
	// DefaultAlertThreshold is the open-wisp count above which callers should
	// surface a warning. This must fire on genuine runaway accumulation, NOT on
	// normal operation. The open-wisp count is dominated by healthy, recent
	// wisps (observed steady-state ~1966 open in a busy town); actionable wisps
	// are limited to stale open-parent-free wisps past max-age plus closed
	// molecule-step wisps (typically ~15-25). The previous value of 800 sat below
	// the healthy open count, so it false-alarmed HIGH every scan despite nothing
	// being wrong. Raised to 3000 so the alert tracks runaway growth rather than
	// the normal working set. See hq-57jr8.
	DefaultAlertThreshold = 3000
)

// ValidateDBName returns an error if the database name is unsafe.
func ValidateDBName(dbName string) error {
	if !validDBName.MatchString(dbName) {
		return fmt.Errorf("invalid database name: %q", dbName)
	}
	return nil
}

// OpenDB opens a connection to the Dolt server for a given database.
func OpenDB(host string, port int, dbName string, readTimeout, writeTimeout time.Duration) (*sql.DB, error) {
	if err := ValidateDBName(dbName); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=%s&writeTimeout=%s",
		host, port, dbName,
		fmt.Sprintf("%ds", int(readTimeout.Seconds())),
		fmt.Sprintf("%ds", int(writeTimeout.Seconds())))
	return sql.Open("mysql", dsn)
}

// parentExcludeJoin returns a LEFT JOIN clause and WHERE condition that restricts
// results to wisps whose parent molecule is closed, missing, or nonexistent.
//
// This replaces the previous parentCheckWhere() which used 3 correlated EXISTS
// subqueries per row, causing O(n*m) query cost on large wisp tables (gt-jd1z).
// The LEFT JOIN approach runs the subquery once and hash-joins: O(n+m).
//
// Semantics (unchanged from parentCheckWhere):
//   - No parent-child dependency → eligible (orphan wisps)
//   - Parent status is 'closed' → eligible (parent already reaped)
//   - Parent row missing (dangling ref) → eligible (parent already purged)
//
// The inverse is simpler: exclude wisps that have an OPEN parent.
//
// Usage:
//
//	join, where := parentExcludeJoin(dbName)
//	query := fmt.Sprintf("SELECT ... FROM wisps w %s WHERE ... AND %s", dbName, join, where)
func parentExcludeJoin(dbName string) (joinClause, whereCondition string) {
	joinClause = `LEFT JOIN (
		SELECT DISTINCT wd.issue_id
		FROM wisp_dependencies wd
		LEFT JOIN wisps pw ON pw.id = wd.depends_on_wisp_id LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
		WHERE wd.type = 'parent-child'
		AND (pw.status IN ('open', 'hooked', 'in_progress') OR pi.status IN ('open', 'hooked', 'in_progress') OR wd.depends_on_external IS NOT NULL)
	) open_parent ON open_parent.issue_id = w.id`
	whereCondition = "open_parent.issue_id IS NULL"
	return
}

const openWispStatusWhere = "w.status IN ('open', 'hooked', 'in_progress')"

// closedMoleculeStepSubquery selects step-wisps whose parent molecule has already closed.
// wisp_dependencies.issue_id is the child; depends_on_wisp_id is the parent molecule.
const closedMoleculeStepSubquery = `
	SELECT DISTINCT wd.issue_id
	FROM wisp_dependencies wd
	INNER JOIN wisps pm ON pm.id = wd.depends_on_wisp_id
	WHERE wd.type = 'parent-child'
	AND pm.issue_type = 'molecule'
	AND pm.status = 'closed'
	AND NOT EXISTS (
		SELECT 1 FROM wisp_dependencies open_dep
		LEFT JOIN wisps open_pw ON open_pw.id = open_dep.depends_on_wisp_id
		LEFT JOIN issues open_pi ON open_pi.id = open_dep.depends_on_issue_id
		WHERE open_dep.issue_id = wd.issue_id
		AND open_dep.type = 'parent-child'
		AND (open_pw.status IN ('open', 'hooked', 'in_progress') OR open_pi.status IN ('open', 'hooked', 'in_progress') OR open_dep.depends_on_external IS NOT NULL)
	)`

func closedMoleculeStepJoin(alias string) string {
	return fmt.Sprintf("INNER JOIN (%s) %s ON %s.issue_id = w.id", closedMoleculeStepSubquery, alias, alias)
}

func closedMoleculeStepExcludeJoin(alias string) string {
	return fmt.Sprintf("LEFT JOIN (%s) %s ON %s.issue_id = w.id", closedMoleculeStepSubquery, alias, alias)
}

type sqlRunner interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// HasReaperSchema checks whether the database has the tables required for reaper
// operations (wisps and issues). Returns false (no error) when tables are missing
// — callers use this to skip databases that have incomplete beads schema (e.g.
// partially initialized databases on the central Dolt server).
func HasReaperSchema(db *sql.DB) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name IN ('wisps', 'issues', 'wisp_dependencies') AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check reaper schema: %w", err)
	}
	if count < 3 {
		return false, nil
	}

	hasWispDependencyColumns, err := hasColumns(ctx, db, "wisp_dependencies", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external")
	if err != nil || !hasWispDependencyColumns {
		return hasWispDependencyColumns, err
	}
	dependenciesExists, err := tableExists(ctx, db, "dependencies")
	if err != nil || !dependenciesExists {
		return !dependenciesExists, err
	}
	return hasColumns(ctx, db, "dependencies", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external")
}

func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ? AND table_schema = DATABASE()", table).Scan(&count)
	return count > 0, err
}

func hasColumns(ctx context.Context, db *sql.DB, table string, columns ...string) (bool, error) {
	if len(columns) == 0 {
		return true, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	args := make([]interface{}, 0, len(columns)+1)
	args = append(args, table)
	for _, column := range columns {
		args = append(args, column)
	}
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name IN (%s)", placeholders)
	err := db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count == len(columns), err
}

// Scan counts reaper candidates in a database without modifying anything.
func Scan(db *sql.DB, dbName string, maxAge, purgeAge, mailDeleteAge, staleIssueAge time.Duration) (*ScanResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	result := &ScanResult{Database: dbName}
	now := time.Now().UTC()
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	moleculeStepJoin := closedMoleculeStepJoin("closed_molecule_step")
	moleculeStepExcludeJoin := closedMoleculeStepExcludeJoin("closed_molecule_step")

	moleculeStepQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisps w %s WHERE %s AND w.issue_type != 'agent'",
		moleculeStepJoin, openWispStatusWhere)
	if err := db.QueryRowContext(ctx, moleculeStepQuery).Scan(&result.MoleculeStepCandidates); err != nil {
		return nil, fmt.Errorf("count molecule step candidates: %w", err)
	}

	// Count reap candidates: open wisps past max_age with eligible parent status.
	// Must match Reap() eligibility semantics exactly, including the exclusion of
	// agent beads, otherwise scan can report candidates that reap will never close.
	// Uses LEFT JOIN anti-pattern instead of correlated EXISTS to avoid O(n*m) cost (gt-jd1z).
	// Closed-molecule steps are counted separately above and excluded here so counts stay disjoint.
	reapQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM wisps w %s %s WHERE %s AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND closed_molecule_step.issue_id IS NULL",
		parentJoin, moleculeStepExcludeJoin, openWispStatusWhere, parentWhere)
	if err := db.QueryRowContext(ctx, reapQuery, now.Add(-maxAge)).Scan(&result.ReapCandidates); err != nil {
		return nil, fmt.Errorf("count reap candidates: %w", err)
	}

	// Count purge candidates: closed wisps past purge_age.
	// No parent check needed — closed wisps past the delete age are unconditionally purgeable.
	// The parent check (correlated subqueries on wisp_dependencies) was causing O(n*m) query
	// cost with 1800+ closed wisps, leading to CPU spikes and connection timeouts (gt-wvd2).
	purgeQuery := "SELECT COUNT(*) FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ?"
	if err := db.QueryRowContext(ctx, purgeQuery, now.Add(-purgeAge)).Scan(&result.PurgeCandidates); err != nil {
		return nil, fmt.Errorf("count purge candidates: %w", err)
	}

	// Count mail candidates.
	// The issues/labels tables may not exist on the gt Dolt server if beads
	// stores its data on a separate Dolt instance. Skip gracefully.
	mailQuery := "SELECT COUNT(*) FROM issues WHERE status = 'closed' AND closed_at < ? AND id IN (SELECT issue_id FROM labels WHERE label = 'gt:message')"
	if err := db.QueryRowContext(ctx, mailQuery, now.Add(-mailDeleteAge)).Scan(&result.MailCandidates); err != nil {
		if !isTableNotFound(err) {
			return nil, fmt.Errorf("count mail candidates: %w", err)
		}
		// issues/labels table not on this server — skip mail count
	}

	// Count stale issue candidates.
	// Same caveat: issues/dependencies tables may live on a separate Dolt instance.
	// Convoys excluded to mirror AutoClose (hq-jnap): convoy lifecycle is
	// tracked-bead-status driven, never staleness driven.
	staleQuery := `
		SELECT COUNT(*) FROM issues i
		WHERE i.status IN ('open', 'in_progress')
		AND i.updated_at < ?
		AND i.priority > 1
		AND i.issue_type NOT IN ('epic', 'convoy')
		AND i.id NOT IN (
			SELECT DISTINCT d.issue_id FROM dependencies d
			INNER JOIN issues dep ON d.depends_on_issue_id = dep.id
			WHERE dep.status IN ('open', 'in_progress')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.depends_on_issue_id FROM dependencies d
			INNER JOIN issues blocker ON d.issue_id = blocker.id
			WHERE d.depends_on_issue_id IS NOT NULL
			AND blocker.status IN ('open', 'in_progress')
		)`
	if err := db.QueryRowContext(ctx, staleQuery, now.Add(-staleIssueAge)).Scan(&result.StaleCandidates); err != nil {
		if !isTableNotFound(err) {
			return nil, fmt.Errorf("count stale candidates: %w", err)
		}
		// issues/dependencies table not on this server — skip stale count
	}

	// Total open wisps.
	openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
	if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenWisps); err != nil {
		return nil, fmt.Errorf("count open wisps: %w", err)
	}

	// Anomaly detection: dangling parent references.
	danglingQuery := `
		SELECT COUNT(*) FROM wisp_dependencies wd
		LEFT JOIN wisps pw ON pw.id = wd.depends_on_wisp_id LEFT JOIN issues pi ON pi.id = wd.depends_on_issue_id
		WHERE wd.type = 'parent-child' AND wd.depends_on_external IS NULL AND (wd.depends_on_wisp_id IS NOT NULL OR wd.depends_on_issue_id IS NOT NULL) AND pw.id IS NULL AND pi.id IS NULL`
	var danglingCount int
	if err := db.QueryRowContext(ctx, danglingQuery).Scan(&danglingCount); err == nil && danglingCount > 0 {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "dangling_parent_ref",
			Message: fmt.Sprintf("%d wisp(s) have parent dependency records pointing to purged/missing parents", danglingCount),
			Count:   danglingCount,
		})
	}

	return result, nil
}

// Reap closes stale wisps in a database whose parent molecule is already closed.
// UPDATEs are batched to avoid holding a write lock for extended periods on large tables.
func Reap(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ReapResult, error) {
	// Use a longer timeout to accommodate batched processing across large tables.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	parentJoin, parentWhere := parentExcludeJoin(dbName)
	moleculeStepJoin := closedMoleculeStepJoin("closed_molecule_step")
	moleculeStepExcludeJoin := closedMoleculeStepExcludeJoin("closed_molecule_step")
	// Exclude agent beads (issue_type='agent') from reaping — they have persistent
	// identity and should not be closed by the wisp reaper regardless of age.
	// Closed-molecule steps are closed immediately through a separate path, so stale
	// max-age counts exclude them to keep dry-run and scan counts disjoint.
	whereClause := fmt.Sprintf(
		"%s AND w.created_at < ? AND w.issue_type != 'agent' AND %s AND closed_molecule_step.issue_id IS NULL", openWispStatusWhere, parentWhere)

	result := &ReapResult{Database: dbName, DryRun: dryRun}

	if dryRun {
		moleculeStepCountQuery := fmt.Sprintf(
			"SELECT COUNT(*) FROM wisps w %s WHERE %s AND w.issue_type != 'agent'",
			moleculeStepJoin, openWispStatusWhere)
		if err := db.QueryRowContext(ctx, moleculeStepCountQuery).Scan(&result.MoleculeStepsClosed); err != nil {
			return nil, fmt.Errorf("dry-run molecule step count: %w", err)
		}
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM wisps w %s %s WHERE %s", parentJoin, moleculeStepExcludeJoin, whereClause)
		if err := db.QueryRowContext(ctx, countQuery, cutoff).Scan(&result.Reaped); err != nil {
			return nil, fmt.Errorf("dry-run count: %w", err)
		}
		openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
		if err := db.QueryRowContext(ctx, openQuery).Scan(&result.OpenRemain); err != nil {
			return nil, fmt.Errorf("count open: %w", err)
		}
		return result, nil
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	sqlCommitted := false
	defer func() {
		if !sqlCommitted {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
		_, _ = conn.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	moleculeStepIDQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s AND w.issue_type != 'agent' LIMIT %d",
		moleculeStepJoin, openWispStatusWhere, DefaultBatchSize)
	moleculeStepsClosed, err := closeWispsInBatches(ctx, conn, moleculeStepIDQuery, nil, "closed molecule steps")
	if err != nil {
		return nil, err
	}
	result.MoleculeStepsClosed = moleculeStepsClosed

	// Batch UPDATE: select IDs in chunks, update each chunk.
	// This avoids holding a write lock on the entire table for minutes.
	// Uses LEFT JOIN anti-pattern instead of correlated EXISTS to avoid O(n*m) cost (gt-jd1z).
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s %s WHERE %s LIMIT %d",
		parentJoin, moleculeStepExcludeJoin, whereClause, DefaultBatchSize)

	totalReaped, err := closeWispsInBatches(ctx, conn, idQuery, []interface{}{cutoff}, "stale wisps")
	if err != nil {
		return nil, err
	}

	result.Reaped = totalReaped
	totalClosed := totalReaped + moleculeStepsClosed

	if totalClosed > 0 {
		// Flush the SQL transaction to the Dolt working set before DOLT_COMMIT.
		// With autocommit=0, UPDATE changes are in the SQL transaction buffer,
		// not the Dolt working set. DOLT_COMMIT operates on the working set,
		// so without this COMMIT it sees "nothing to commit".
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return result, fmt.Errorf("sql commit: %w", err)
		}
		sqlCommitted = true
		commitMsg := fmt.Sprintf("reaper: close %d wisps in %s", totalClosed, dbName)
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// "nothing to commit" is expected when the reaper reverts dirty working
			// set changes back to match HEAD. The wisps were set to "open" in the
			// server's in-memory working set without being committed; closing them
			// makes the working set match HEAD again, so DOLT_COMMIT sees no diff.
			if !isNothingToCommit(err) {
				return result, fmt.Errorf("dolt commit: %w", err)
			}
		}
	}

	openQuery := "SELECT COUNT(*) FROM wisps WHERE status IN ('open', 'hooked', 'in_progress')"
	if err := conn.QueryRowContext(ctx, openQuery).Scan(&result.OpenRemain); err != nil {
		return result, fmt.Errorf("count open: %w", err)
	}

	return result, nil
}

func closeWispsInBatches(ctx context.Context, runner sqlRunner, idQuery string, queryArgs []interface{}, description string) (int, error) {
	total := 0
	for {
		rows, err := runner.QueryContext(ctx, idQuery, queryArgs...)
		if err != nil {
			return total, fmt.Errorf("select %s batch: %w", description, err)
		}

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return total, fmt.Errorf("scan %s id: %w", description, err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return total, fmt.Errorf("read %s ids: %w", description, err)
		}
		rows.Close()

		if len(ids) == 0 {
			return total, nil
		}

		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := strings.Join(placeholders, ",")

		updateQuery := fmt.Sprintf(
			"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s) AND status IN ('open', 'hooked', 'in_progress') AND issue_type != 'agent'",
			inClause)
		sqlResult, err := runner.ExecContext(ctx, updateQuery, args...)
		if err != nil {
			return total, fmt.Errorf("close %s batch: %w", description, err)
		}

		affected, _ := sqlResult.RowsAffected()
		total += int(affected)
	}
}

// Purge deletes old closed wisps and mail from a database.
func Purge(db *sql.DB, dbName string, purgeAge, mailDeleteAge time.Duration, dryRun bool) (*PurgeResult, error) {
	const minimumWispPurgeAge = 7 * 24 * time.Hour
	if purgeAge < minimumWispPurgeAge {
		return nil, fmt.Errorf("purge age %s is below the mandatory minimum %s", purgeAge, minimumWispPurgeAge)
	}
	if mailDeleteAge < minimumWispPurgeAge {
		return nil, fmt.Errorf("mail delete age %s is below the mandatory minimum %s", mailDeleteAge, minimumWispPurgeAge)
	}
	result := &PurgeResult{Database: dbName, DryRun: dryRun}

	// Purge closed wisps.
	purged, ids, anomalies, err := purgeClosedWisps(db, dbName, purgeAge, dryRun)
	if err != nil {
		return nil, fmt.Errorf("purge wisps: %w", err)
	}
	result.WispsPurged = purged
	result.WispsPurgedIDs = ids
	result.Anomalies = append(result.Anomalies, anomalies...)

	// Purge old mail.
	mailPurged, mailIDs, err := purgeOldMail(db, dbName, mailDeleteAge, dryRun)
	if err != nil {
		return result, fmt.Errorf("purge mail: %w", err)
	}
	result.MailPurged = mailPurged
	result.MailPurgedIDs = mailIDs

	return result, nil
}

// protectedWispExclusionSQL excludes gt-12f/cl-kf00 protected beads from an
// unconditional, age-based wisp purge: a bead labelled gt:merge-request, or
// any bead carrying a comment from a compliance seat. Matched on the label
// and on the comment author column — never on a marker string in title or
// description text, which a rephrase can miss. If wisp_labels or
// wisp_comments cannot be queried (e.g. missing on a not-yet-migrated
// database), the surrounding SELECT errors and purgeClosedWisps aborts rather
// than purging unprotected — failing closed, not open.
var protectedWispExclusionSQL = wispaudit.ProtectedWispExclusionSQL("w.id", "wisp_labels", "wisp_comments")

// closedWispsIDQuery builds the batch-delete candidate query for
// purgeClosedWisps. It is a plain concatenation, never fmt.Sprintf, because
// protectedWispExclusionSQL's LIKE patterns ('%/crew/compliance') contain
// literal '%' characters that fmt.Sprintf consumes as format verbs — the bug
// that produced "%!/(int=1000)" and "LIMIT %!d(MISSING)" in the query actually
// sent to Dolt (gt-12f round 2: go vet caught it as an invalid format string;
// the test for this query must build it the same way, not re-embed the bug).
func closedWispsIDQuery(limit int) string {
	return "SELECT w.id FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ?" +
		protectedWispExclusionSQL +
		" LIMIT " + strconv.Itoa(limit)
}

// maxPurgePreviewIDs caps how many candidate ids a dry run names explicitly.
// gt-12f requires the list, not just a count, but an unbounded list on a
// database with thousands of closed wisps is not legible output.
const maxPurgePreviewIDs = 500

// previewClosedWispIDs returns up to maxPurgePreviewIDs ids a real purge would
// delete, for dry-run callers. It shares closedWispsIDQuery with the real
// delete path so a preview cannot name a different set than the one that
// would actually go.
func previewClosedWispIDs(ctx context.Context, db *sql.DB, cutoff time.Time) ([]string, error) {
	rows, err := db.QueryContext(ctx, closedWispsIDQuery(maxPurgePreviewIDs), cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// verifyWispsGone re-queries ids after a purge and reports which are still
// present. A reported RowsAffected count is not the same as a row being gone
// (the cl-wisp-0u30 shape: a wisp named in 202 "completed" purge events that
// was never actually removed) — this is the check that would have caught it.
// sqlReadWriter is satisfied by both *sql.DB and a pinned *sql.Conn. The
// latter is required when a purge changes connection-local transaction state:
// planning, deletion, and verification must observe one connection.
type sqlReadWriter interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

func verifyWispsGone(ctx context.Context, db sqlReadWriter, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT id FROM wisps WHERE id IN (" + strings.Join(placeholders, ",") + ")" //nolint:gosec // G201: placeholders only
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var survivors []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return survivors, err
		}
		survivors = append(survivors, id)
	}
	return survivors, rows.Err()
}

func purgeClosedWisps(db *sql.DB, dbName string, purgeAge time.Duration, dryRun bool) (int, []string, []Anomaly, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	deleteCutoff := time.Now().UTC().Add(-purgeAge)
	var anomalies []Anomaly

	// Digest: count by wisp_type.
	// No parent check — closed wisps past the delete age are unconditionally purgeable
	// (apart from the protected-bead exclusion below).
	// The parent check (correlated subqueries on wisp_dependencies) was causing O(n*m)
	// query cost with 1800+ closed wisps, leading to CPU spikes and timeouts (gt-wvd2).
	digestQuery := "SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ?" +
		protectedWispExclusionSQL + " GROUP BY wtype"
	rows, err := db.QueryContext(ctx, digestQuery, deleteCutoff)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("digest query: %w", err)
	}
	digestTotal := 0
	for rows.Next() {
		var wtype string
		var cnt int
		if err := rows.Scan(&wtype, &cnt); err != nil {
			rows.Close()
			return 0, nil, nil, fmt.Errorf("digest scan: %w", err)
		}
		digestTotal += cnt
	}
	rows.Close()

	if digestTotal == 0 {
		return 0, nil, anomalies, nil
	}

	if dryRun {
		previewIDs, previewErr := previewClosedWispIDs(ctx, db, deleteCutoff)
		if previewErr != nil {
			anomalies = append(anomalies, Anomaly{
				Type:    "dry_run_preview_failed",
				Message: fmt.Sprintf("could not list candidate ids: %v", previewErr),
			})
		}
		return digestTotal, previewIDs, anomalies, nil
	}

	// @@autocommit is connection-local. Pin the entire mutating sequence so
	// the planned IDs, DELETEs, verification read, and COMMIT cannot hop
	// between pooled connections and falsely certify rows from another view.
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("acquire purge connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return 0, nil, nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	// Batch delete — simple status+age filter plus the protected-bead
	// exclusion above; no parent check needed for purge.
	idQuery := closedWispsIDQuery(DefaultBatchSize)
	auxTables := []string{"wisp_labels", "wisp_comments", "wisp_events", "wisp_dependencies"}

	// This is the largest wisp deleter in the tree and the one behind the 1449
	// wisps lost on 2026-09-01. It deletes by direct SQL from a table in
	// dolt_ignore, so nothing it removes is in any Dolt commit and no AS OF can
	// read it back. The log below is the only record that outlives the delete,
	// which is why an unwritable log stops the purge instead of warning about it.
	deletionLog := &wispDeletionLog{
		// "reaper" covers both callers — the daemon's wisp_reaper patrol and
		// `gt reaper purge` at a terminal — and GT_ROLE names the agent
		// session when there is one. Neither may be labelled as the other.
		actor: wispaudit.Actor("reaper"),
		path:  wispaudit.PathReaper,
		scope: "closed_at<" + deleteCutoff.Format(time.RFC3339),
		db:    dbName,
	}

	totalDeleted, err := batchDeleteRows(ctx, conn, idQuery, deleteCutoff, "wisps", auxTables, deletionLog.planBatch)
	if err != nil {
		return totalDeleted, nil, anomalies, err
	}

	if totalDeleted > 0 {
		// Flush SQL transaction to working set before DOLT_COMMIT.
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			anomalies = append(anomalies, Anomaly{
				Type:    "sql_commit_failed",
				Message: fmt.Sprintf("sql commit after purge failed: %v", err),
			})
			return totalDeleted, nil, anomalies, nil
		}
		// A completion receipt is evidence of a committed delete, not of a
		// transaction that may still roll back. Verify after COMMIT, on this
		// same pinned connection, before closing the record.
		verifiedGone := deletionLog.planned
		var verificationFailures []string
		if survivors, verifyErr := verifyWispsGone(ctx, conn, wispaudit.IDs(deletionLog.planned)); verifyErr != nil {
			anomalies = append(anomalies, Anomaly{Type: "purge_verify_failed", Message: fmt.Sprintf("could not confirm deletion: %v", verifyErr)})
			verifiedGone = nil
			verificationFailures = append(verificationFailures, "post-commit verification: "+verifyErr.Error())
		} else if len(survivors) > 0 {
			anomalies = append(anomalies, Anomaly{Type: "purge_survivors", Message: fmt.Sprintf("still present after purge: %s", strings.Join(survivors, ", ")), Count: len(survivors)})
			verifiedGone = wispsWithoutIDs(verifiedGone, survivors)
			for _, id := range survivors {
				verificationFailures = append(verificationFailures, id+": still present after purge")
			}
		}
		deletionLog.completed(verifiedGone, verificationFailures, map[string]interface{}{"deleted_rows": totalDeleted})
		commitMsg := fmt.Sprintf("reaper: purge %d closed wisps from %s", totalDeleted, dbName)
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// Non-fatal — log but continue.
			anomalies = append(anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after purge failed: %v", err),
			})
		}
	}

	return totalDeleted, nil, anomalies, nil
}

// wispDeletionLog is the reaper's half of the one durable wisp deletion record
// (hq-6ewp). Every batch is named in <town>/.events.jsonl before it is deleted,
// and a batch whose record will not write is not deleted.
//
// It accumulates what it planned so verification can distinguish the rows it
// proved absent from survivors. The closing record names only the former.
type wispDeletionLog struct {
	actor string
	// path names the deleter for the audit record (wispaudit.Path*). Explicit
	// per log rather than hardcoded, because this type now backs more than one
	// deletion path (wisps and, separately, old mail — gt-12f round 2 item 4:
	// "reaper purgeOldMail: at least an audit record").
	path  string
	scope string
	db    string
	// titleLookup resolves ids to titles for the record. Defaults to
	// lookupWispTitles (the `wisps` table) when nil; purgeOldMail supplies one
	// that reads `issues` instead.
	titleLookup func(context.Context, sqlReadWriter, []string) []wispaudit.Wisp
	planned     []wispaudit.Wisp
}

// planBatch records one batch and reports whether the delete may proceed.
func (l *wispDeletionLog) planBatch(ctx context.Context, db sqlReadWriter, ids []string) error {
	lookup := l.titleLookup
	if lookup == nil {
		lookup = lookupWispTitles
	}
	batch := lookup(ctx, db, ids)
	if err := wispaudit.Plan(l.actor, l.path, l.scope, l.db, batch, nil); err != nil {
		return fmt.Errorf("refusing to delete %d wisps: the deletion could not be recorded first: %w", len(ids), err)
	}
	l.planned = append(l.planned, batch...)
	return nil
}

// completed writes the closing record. Best-effort: the delete has happened and
// the planned records already name every id, so a failure here loses the
// outcome, not the evidence.
func (l *wispDeletionLog) completed(verified []wispaudit.Wisp, failures []string, extra map[string]interface{}) {
	if len(failures) > 0 {
		_ = wispaudit.Partial(l.actor, l.path, l.scope, l.db, verified, failures, extra)
		return
	}
	_ = wispaudit.Completed(l.actor, l.path, l.scope, l.db, verified, nil, extra)
}

func wispsWithoutIDs(wisps []wispaudit.Wisp, excluded []string) []wispaudit.Wisp {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	verified := make([]wispaudit.Wisp, 0, len(wisps))
	for _, w := range wisps {
		if _, found := excludedSet[w.ID]; !found {
			verified = append(verified, w)
		}
	}
	return verified
}

// lookupWispTitles fills in the titles for a batch of ids. A title is what makes
// a deletion record legible to a human afterwards, but it is not what makes it
// valid: if the lookup fails the ids are recorded without titles rather than the
// batch going unrecorded, because an id-only record still names what was lost.
func lookupWispTitles(ctx context.Context, db sqlReadWriter, ids []string) []wispaudit.Wisp {
	fallback := wispaudit.WispsFromIDs(ids)
	if len(ids) == 0 {
		return fallback
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("SELECT id, title FROM wisps WHERE id IN (%s)", strings.Join(placeholders, ",")) //nolint:gosec // G201: placeholders only
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fallback
	}
	defer rows.Close()

	titles := make(map[string]string, len(ids))
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return fallback
		}
		titles[id] = title
	}
	if err := rows.Err(); err != nil {
		return fallback
	}

	out := make([]wispaudit.Wisp, 0, len(ids))
	for _, id := range ids {
		out = append(out, wispaudit.Wisp{ID: id, Title: titles[id]})
	}
	return out
}

// lookupMailTitles is purgeOldMail's titleLookup: mail lives in the
// db-qualified `issues` table, not the bare `wisps` table lookupWispTitles
// reads, so it needs its own query rather than reusing that one.
func lookupMailTitles(dbName string) func(context.Context, sqlReadWriter, []string) []wispaudit.Wisp {
	return func(ctx context.Context, db sqlReadWriter, ids []string) []wispaudit.Wisp {
		fallback := wispaudit.WispsFromIDs(ids)
		if len(ids) == 0 {
			return fallback
		}
		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf("SELECT id, title FROM `%s`.issues WHERE id IN (%s)", dbName, strings.Join(placeholders, ",")) //nolint:gosec // G201: placeholders only; dbName validated by caller
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return fallback
		}
		defer rows.Close()

		titles := make(map[string]string, len(ids))
		for rows.Next() {
			var id, title string
			if err := rows.Scan(&id, &title); err != nil {
				return fallback
			}
			titles[id] = title
		}
		if err := rows.Err(); err != nil {
			return fallback
		}
		out := make([]wispaudit.Wisp, 0, len(ids))
		for _, id := range ids {
			out = append(out, wispaudit.Wisp{ID: id, Title: titles[id]})
		}
		return out
	}
}

func purgeOldMail(db *sql.DB, dbName string, mailDeleteAge time.Duration, dryRun bool) (int, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const minimumMailDeleteAge = 7 * 24 * time.Hour
	if mailDeleteAge < minimumMailDeleteAge {
		return 0, nil, fmt.Errorf("mail delete age %s is below the mandatory minimum %s", mailDeleteAge, minimumMailDeleteAge)
	}
	mailCutoff := time.Now().UTC().Add(-mailDeleteAge)
	mailProtection := fmt.Sprintf(" AND i.id NOT IN (SELECT issue_id FROM `%s`.labels WHERE label = '%s' OR %s)",
		dbName, wispaudit.ProtectedLabel, wispaudit.ComplianceMailLabelSQL("label"))

	countQuery := fmt.Sprintf(
		"SELECT COUNT(*) FROM `%s`.issues i WHERE i.status = 'closed' AND i.closed_at < ? AND i.id IN (SELECT issue_id FROM `%s`.labels WHERE label = 'gt:message')",
		dbName, dbName) + mailProtection
	var count int
	if err := db.QueryRowContext(ctx, countQuery, mailCutoff).Scan(&count); err != nil {
		if isTableNotFound(err) {
			return 0, nil, nil // issues/labels not on this server
		}
		return 0, nil, fmt.Errorf("count mail: %w", err)
	}
	if count == 0 {
		return 0, nil, nil
	}

	idQuery := fmt.Sprintf(
		"SELECT i.id FROM `%s`.issues i INNER JOIN `%s`.labels l ON i.id = l.issue_id WHERE i.status = 'closed' AND i.closed_at < ? AND l.label = 'gt:message'",
		dbName, dbName) + mailProtection + fmt.Sprintf(" LIMIT %d", DefaultBatchSize)

	if dryRun {
		previewQuery := fmt.Sprintf(
			"SELECT i.id FROM `%s`.issues i INNER JOIN `%s`.labels l ON i.id = l.issue_id WHERE i.status = 'closed' AND i.closed_at < ? AND l.label = 'gt:message'",
			dbName, dbName) + mailProtection + fmt.Sprintf(" LIMIT %d", maxPurgePreviewIDs)
		rows, err := db.QueryContext(ctx, previewQuery, mailCutoff)
		if err != nil {
			return count, nil, fmt.Errorf("preview old mail: %w", err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return count, ids, fmt.Errorf("scan old mail preview: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return count, ids, fmt.Errorf("preview old mail: %w", err)
		}
		return count, ids, nil
	}

	// autocommit and a transaction view are connection-local. Pin planning,
	// delete, COMMIT and post-commit verification to one connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("acquire mail purge connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return 0, nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	auxTables := []string{"labels", "comments", "events", "dependencies"}

	// gt-12f round 2: "at least an audit record" even though mail lives in
	// `issues` (not dolt_ignore) and so has Dolt history behind it — every
	// deleter in the tree now writes the same record, for consistency with the
	// ones whose deletes are unrecoverable without it.
	mailLog := &wispDeletionLog{
		actor:       wispaudit.Actor("reaper"),
		path:        wispaudit.PathReaperMail,
		scope:       "closed_at<" + mailCutoff.Format(time.RFC3339),
		db:          dbName,
		titleLookup: lookupMailTitles(dbName),
	}

	totalDeleted, err := batchDeleteRows(ctx, conn, idQuery, mailCutoff, "issues", auxTables, mailLog.planBatch)
	if err != nil {
		return totalDeleted, nil, err
	}

	if totalDeleted > 0 {
		// Flush SQL transaction to working set before DOLT_COMMIT.
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return totalDeleted, nil, fmt.Errorf("sql commit: %w", err)
		}
		verified, survivors, verifyErr := verifyMailGone(ctx, conn, dbName, wispaudit.IDs(mailLog.planned))
		extra := map[string]interface{}{"deleted_rows": totalDeleted}
		if verifyErr != nil {
			mailLog.completed(nil, []string{"post-commit verification: " + verifyErr.Error()}, extra)
		} else if len(survivors) > 0 {
			extra["survived_purge"] = survivors
			failures := make([]string, 0, len(survivors))
			for _, id := range survivors {
				failures = append(failures, id+": still present after purge")
			}
			mailLog.completed(wispsWithoutIDs(mailLog.planned, survivors), failures, extra)
		} else {
			mailLog.completed(verified, nil, extra)
		}
		commitMsg := fmt.Sprintf("reaper: purge %d old mail from %s", totalDeleted, dbName)
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('--allow-empty', '-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// Non-fatal.
		}
	}

	return totalDeleted, nil, nil
}

func verifyMailGone(ctx context.Context, db sqlReadWriter, dbName string, ids []string) ([]wispaudit.Wisp, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT id FROM `%s`.issues WHERE id IN (%s)", dbName, strings.Join(placeholders, ",")), args...) //nolint:gosec // G201: dbName is validated by caller
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var survivors []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, survivors, err
		}
		survivors = append(survivors, id)
	}
	if err := rows.Err(); err != nil {
		return nil, survivors, err
	}
	return wispsWithoutIDs(wispaudit.WispsFromIDs(ids), survivors), survivors, nil
}

// AutoClose closes issues that have been open with no updates past staleAge.
// Excludes P0/P1 priority, epics, hooked/pinned issues, standing-order labels,
// and issues with active dependencies.
func AutoClose(db *sql.DB, dbName string, staleAge time.Duration, dryRun bool) (*AutoCloseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	staleCutoff := time.Now().UTC().Add(-staleAge)
	result := &AutoCloseResult{Database: dbName, DryRun: dryRun}

	// Convoys are excluded from staleness auto-close (hq-jnap): their lifecycle
	// is driven by tracked-bead status (`gt convoy check` / refinery post-merge),
	// and the 'tracks' relation is non-blocking so the dependency exclusions
	// below do NOT protect a convoy with open tracked issues. Stale-closing a
	// convoy while its tracked beads are open orphans them from dispatch
	// tracking and causes duplicate dispatches (hq-qouv/hq-shb1 incident).
	whereClause := fmt.Sprintf(`
		i.status IN ('open', 'in_progress')
		AND i.updated_at < ?
		AND i.priority > 1
		AND i.issue_type NOT IN ('epic', 'convoy')
		AND i.id NOT IN (
			SELECT DISTINCT l.issue_id FROM `+"`%s`"+`.labels l
			WHERE l.label IN ('gt:standing-orders', 'gt:keep', 'gt:role', 'gt:rig')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.issue_id FROM `+"`%s`"+`.dependencies d
			INNER JOIN `+"`%s`"+`.issues dep ON d.depends_on_issue_id = dep.id
			WHERE dep.status IN ('open', 'in_progress')
		)
		AND i.id NOT IN (
			SELECT DISTINCT d.depends_on_issue_id FROM `+"`%s`"+`.dependencies d
			INNER JOIN `+"`%s`"+`.issues blocker ON d.issue_id = blocker.id
			WHERE d.depends_on_issue_id IS NOT NULL
			AND blocker.status IN ('open', 'in_progress')
		)`, dbName, dbName, dbName, dbName, dbName)

	// Two-step SELECT-then-UPDATE to avoid self-referencing subquery in UPDATE,
	// which is not valid MySQL (Error 1093) and fragile in Dolt (dolthub/dolt#10600).
	selectQuery := fmt.Sprintf("SELECT i.id, i.title, i.updated_at FROM issues i WHERE %s", whereClause)
	rows, err := db.QueryContext(ctx, selectQuery, staleCutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil // issues/dependencies not on this server
		}
		return nil, fmt.Errorf("select stale: %w", err)
	}
	type candidate struct {
		id        string
		title     string
		updatedAt time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title, &c.updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan stale id: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	// Build per-issue closure log entries.
	now := time.Now().UTC()
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
		result.ClosedEntries = append(result.ClosedEntries, ClosedEntry{
			ID:       c.id,
			Title:    c.title,
			AgeDays:  int(now.Sub(c.updatedAt).Hours() / 24),
			Database: dbName,
		})
	}

	if dryRun {
		result.Closed = len(ids)
		return result, nil
	}

	if len(ids) == 0 {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW(), close_reason = 'stale:auto-closed by reaper' WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("auto-close: %w", err)
	}

	result.Closed = len(ids)

	if len(ids) > 0 {
		// Flush SQL transaction to working set before DOLT_COMMIT.
		if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "sql_commit_failed",
				Message: fmt.Sprintf("sql commit after auto-close failed: %v", err),
			})
			return result, nil
		}
		commitMsg := fmt.Sprintf("reaper: auto-close %d stale issues in %s", len(ids), dbName)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
			// "nothing to commit" is expected when the updated tables are dolt_ignored.
			if !isNothingToCommit(err) {
				result.Anomalies = append(result.Anomalies, Anomaly{
					Type:    "dolt_commit_failed",
					Message: fmt.Sprintf("dolt commit after auto-close failed: %v", err),
				})
			}
		}
	}

	return result, nil
}

// batchDeleteRows deletes rows from a primary table and its auxiliary tables in batches.
func batchDeleteRows(ctx context.Context, db sqlReadWriter, idQuery string, cutoffArg time.Time, primaryTable string, auxTables []string, record func(context.Context, sqlReadWriter, []string) error) (int, error) {
	totalDeleted := 0
	for {
		idRows, err := db.QueryContext(ctx, idQuery, cutoffArg)
		if err != nil {
			return totalDeleted, fmt.Errorf("select batch: %w", err)
		}

		var ids []string
		for idRows.Next() {
			var id string
			if err := idRows.Scan(&id); err != nil {
				idRows.Close()
				return totalDeleted, fmt.Errorf("scan id: %w", err)
			}
			ids = append(ids, id)
		}
		idRows.Close()

		if len(ids) == 0 {
			break
		}

		// Record before deleting, and stop if the record will not write. The
		// aux-table deletes below are already irreversible for a table with no
		// Dolt history, so there is no point past this one at which the set can
		// still be named. nil means the primary table has real history and
		// needs no side record — see the call sites.
		if record != nil {
			if err := record(ctx, db, ids); err != nil {
				return totalDeleted, err
			}
		}

		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		inClause := "(" + strings.Join(placeholders, ",") + ")"

		for _, tbl := range auxTables {
			delAux := fmt.Sprintf("DELETE FROM `%s` WHERE issue_id IN %s", tbl, inClause) //nolint:gosec // G201: tbl is internal
			if _, err := db.ExecContext(ctx, delAux, args...); err != nil {
				// Non-fatal: log and continue.
			}
		}

		// Clean up typed reverse dependency references to prevent dangling parent refs.
		var reverseDeletes []string
		switch primaryTable {
		case "wisps":
			reverseDeletes = []string{
				fmt.Sprintf("DELETE FROM wisp_dependencies WHERE depends_on_wisp_id IN %s", inClause),
				fmt.Sprintf("DELETE FROM dependencies WHERE depends_on_wisp_id IN %s", inClause),
			}
		case "issues":
			reverseDeletes = []string{
				fmt.Sprintf("DELETE FROM wisp_dependencies WHERE depends_on_issue_id IN %s", inClause),
				fmt.Sprintf("DELETE FROM dependencies WHERE depends_on_issue_id IN %s", inClause),
			}
		}
		for _, delReverse := range reverseDeletes {
			if _, err := db.ExecContext(ctx, delReverse, args...); err != nil {
				// Non-fatal.
			}
		}

		delPrimary := fmt.Sprintf("DELETE FROM `%s` WHERE id IN %s", primaryTable, inClause) //nolint:gosec // G201: primaryTable is internal
		sqlResult, err := db.ExecContext(ctx, delPrimary, args...)
		if err != nil {
			return totalDeleted, fmt.Errorf("delete %s batch: %w", primaryTable, err)
		}
		affected, _ := sqlResult.RowsAffected()
		totalDeleted += int(affected)
	}

	return totalDeleted, nil
}

// ClosePluginReceiptResult holds the results of closing plugin run receipts.
type ClosePluginReceiptResult struct {
	Database  string    `json:"database"`
	Closed    int       `json:"closed"`
	DryRun    bool      `json:"dry_run,omitempty"`
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// ClosePluginReceipts closes open issues labeled "type:plugin-run" that are
// older than maxAge. These are transient run receipts created by deacon dog
// plugins; they should be closed shortly after creation since they exist only
// for audit/cooldown-gate purposes. The standard AutoClose path requires 7 days
// of staleness, which lets plugin receipts accumulate into the hundreds.
func ClosePluginReceipts(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	// Find open issues with the "type:plugin-run" label older than maxAge.
	selectQuery := fmt.Sprintf(`
		SELECT i.id FROM `+"`%s`"+`.issues i
		INNER JOIN `+"`%s`"+`.labels l ON i.id = l.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND l.label = 'type:plugin-run'
		AND i.created_at < ?`, dbName, dbName)

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select plugin receipts: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan plugin receipt id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW() WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close plugin receipts: %w", err)
	}

	// Flush and commit.
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after plugin receipt close failed: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d plugin receipts in %s", len(ids), dbName)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
		if !isNothingToCommit(err) {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after plugin receipt close failed: %v", err),
			})
		}
	}

	return result, nil
}

// ClosePluginDispatches closes open dispatch mail beads created by the daemon
// when sending plugin instructions to dogs. These beads are labeled "gt:message"
// + "from:daemon" with a title prefix "Plugin:" and are never closed after the
// dog completes. Without this, they accumulate at ~288/day (one per 5-minute
// stuck-agent-dog run) and are only caught by AutoClose after 7 days.
func ClosePluginDispatches(db *sql.DB, dbName string, maxAge time.Duration, dryRun bool) (*ClosePluginReceiptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultQueryTimeout)
	defer cancel()

	cutoff := time.Now().UTC().Add(-maxAge)
	result := &ClosePluginReceiptResult{Database: dbName, DryRun: dryRun}

	// Find open issues with both "gt:message" and "from:daemon" labels whose
	// title starts with "Plugin:", older than maxAge.
	selectQuery := fmt.Sprintf(`
		SELECT i.id FROM `+"`%s`"+`.issues i
		INNER JOIN `+"`%s`"+`.labels l1 ON i.id = l1.issue_id
		INNER JOIN `+"`%s`"+`.labels l2 ON i.id = l2.issue_id
		WHERE i.status IN ('open', 'in_progress')
		AND l1.label = 'gt:message'
		AND l2.label = 'from:daemon'
		AND i.title LIKE 'Plugin:%%'
		AND i.created_at < ?`, dbName, dbName, dbName)

	rows, err := db.QueryContext(ctx, selectQuery, cutoff)
	if err != nil {
		if isTableNotFound(err) {
			return result, nil
		}
		return nil, fmt.Errorf("select plugin dispatches: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan plugin dispatch id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	result.Closed = len(ids)
	if len(ids) == 0 || dryRun {
		return result, nil
	}

	if _, err := db.ExecContext(ctx, "SET @@autocommit = 0"); err != nil {
		return nil, fmt.Errorf("disable autocommit: %w", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "SET @@autocommit = 1")
	}()

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	updateQuery := fmt.Sprintf(
		"UPDATE `%s`.issues SET status = 'closed', closed_at = NOW() WHERE id IN (%s)",
		dbName, strings.Join(placeholders, ","))
	if _, err := db.ExecContext(ctx, updateQuery, args...); err != nil {
		return nil, fmt.Errorf("close plugin dispatches: %w", err)
	}

	// Flush and commit.
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		result.Anomalies = append(result.Anomalies, Anomaly{
			Type:    "sql_commit_failed",
			Message: fmt.Sprintf("sql commit after plugin dispatch close failed: %v", err),
		})
		return result, nil
	}
	commitMsg := fmt.Sprintf("reaper: close %d plugin dispatches in %s", len(ids), dbName)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil { //nolint:gosec // G201: commitMsg from safe values
		if !isNothingToCommit(err) {
			result.Anomalies = append(result.Anomalies, Anomaly{
				Type:    "dolt_commit_failed",
				Message: fmt.Sprintf("dolt commit after plugin dispatch close failed: %v", err),
			})
		}
	}

	return result, nil
}

// FormatJSON marshals any value to indented JSON.
func FormatJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data)
}
