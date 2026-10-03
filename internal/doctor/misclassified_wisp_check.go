package doctor

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// CheckMisclassifiedWisps detects ephemeral beads that are in the issues table
// instead of the wisps table. This is a data integrity check, not a heuristic —
// it only acts on beads whose ephemeral flag is already set (ZFC: agent decides,
// Go transports).
//
// Detection prefers Dolt (live DB via bd sql --csv) over JSONL, falling back to
// JSONL when the DB is unreachable.
type CheckMisclassifiedWisps struct {
	FixableCheck
	misclassified     []misclassifiedWisp
	misclassifiedRigs map[string]int // rig -> count
}

type misclassifiedWisp struct {
	rigName string
	workDir string
	id      string
	title   string
	reason  string
}

// Kept as a variable so copy/verify tests do not need a live Dolt server just
// to exercise the migration ordering. Production always uses the real commit.
var commitMisclassifiedWispWorkingSet = doltserver.CommitServerWorkingSet

// NewCheckMisclassifiedWisps creates a new misclassified wisp check.
func NewCheckMisclassifiedWisps() *CheckMisclassifiedWisps {
	return &CheckMisclassifiedWisps{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "misclassified-wisps",
				CheckDescription: "Detect ephemeral beads misplaced in the issues table",
				CheckCategory:    CategoryCleanup,
			},
		},
		misclassifiedRigs: make(map[string]int),
	}
}

// Run checks for ephemeral beads in the issues table across all rigs.
// Only flags beads where ephemeral=1 — never guesses based on titles,
// labels, or ID patterns (ZFC compliance).
func (c *CheckMisclassifiedWisps) Run(ctx *CheckContext) *CheckResult {
	c.misclassified = nil
	c.misclassifiedRigs = make(map[string]int)

	// Try Dolt-first detection via ListDatabases (matches NullAssigneeCheck pattern).
	databases, dbErr := doltserver.ListDatabases(ctx.TownRoot)
	useDolt := dbErr == nil && len(databases) > 0

	var details []string
	var totalProbeErrors int

	if useDolt {
		for _, db := range databases {
			rigDir := resolveMisclassifiedWispWorkDir(ctx.TownRoot, misclassifiedWisp{rigName: db})
			found, probeErrors := c.findMisplacedEphemeralsDolt(rigDir, db)
			totalProbeErrors += probeErrors
			if len(found) > 0 {
				c.misclassified = append(c.misclassified, found...)
				c.misclassifiedRigs[db] = len(found)
				details = append(details, fmt.Sprintf("%s: %d misplaced ephemeral(s)", db, len(found)))
			}
		}
	} else {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Dolt unavailable — skipping misplaced ephemeral check",
		}
	}

	if totalProbeErrors > 0 {
		details = append(details, fmt.Sprintf("%d DB probe(s) failed — some databases were skipped", totalProbeErrors))
	}

	total := len(c.misclassified)
	if total > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d ephemeral bead(s) misplaced in issues table", total),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to migrate to wisps table",
		}
	}

	if totalProbeErrors > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No misplaced ephemerals found (some DB probes failed)",
			Details: details,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No misplaced ephemerals found",
	}
}

// findMisplacedEphemeralsDolt queries the live Dolt DB for beads in the issues
// table that have ephemeral=1. These should be in the wisps table instead.
// No heuristics — only the ephemeral flag matters.
func (c *CheckMisclassifiedWisps) findMisplacedEphemeralsDolt(rigDir, rigName string) ([]misclassifiedWisp, int) {
	issueQuery := `SELECT id, title FROM issues WHERE ephemeral = 1`
	cmd := exec.Command("bd", "sql", "--csv", issueQuery) //nolint:gosec // G204: query is a constant
	cmd.Dir = rigDir
	issueOutput, err := cmd.CombinedOutput()
	if err != nil {
		return nil, 1 // DB unavailable for this rig
	}

	issueReader := csv.NewReader(strings.NewReader(string(issueOutput)))
	issueRecords, err := issueReader.ReadAll()
	if err != nil || len(issueRecords) < 2 {
		return nil, 0
	}

	var found []misclassifiedWisp
	for _, rec := range issueRecords[1:] {
		if len(rec) < 2 {
			continue
		}
		found = append(found, misclassifiedWisp{
			rigName: rigName,
			workDir: rigDir,
			id:      strings.TrimSpace(rec[0]),
			title:   strings.TrimSpace(rec[1]),
			reason:  "ephemeral bead in issues table",
		})
	}

	return found, 0
}

// Fix migrates misplaced ephemeral beads from the issues table to the wisps table.
//
// Pattern follows wisps_migrate.go (INSERT IGNORE) + NullAssigneeCheck (bd sql + commit).
func (c *CheckMisclassifiedWisps) Fix(ctx *CheckContext) error {
	if len(c.misclassified) == 0 {
		return nil
	}

	// Group by rig for batch operations.
	rigBatches := make(map[string][]misclassifiedWisp)
	for _, w := range c.misclassified {
		workDir := resolveMisclassifiedWispWorkDir(ctx.TownRoot, w)
		rigBatches[workDir] = append(rigBatches[workDir], w)
	}

	var errs []string

	for workDir, batch := range rigBatches {
		for _, w := range batch {
			// A failed copy must not prevent independent beads in this rig from
			// being repaired, nor permit the failed bead to be deleted as part of
			// a batch. Copy, verify, and delete one id at a time.
			idList := "'" + strings.ReplaceAll(w.id, "'", "''") + "'"
			if err := c.purgeRigBatch(ctx, workDir, w.rigName, idList); err != nil {
				errs = append(errs, fmt.Sprintf("%s/%s: %v", w.rigName, w.id, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial fix: %s", strings.Join(errs, "; "))
	}
	return nil
}

func resolveMisclassifiedWispWorkDir(townRoot string, w misclassifiedWisp) string {
	if w.workDir != "" {
		return w.workDir
	}

	if w.rigName == "town" || w.rigName == "hq" {
		return townRoot
	}

	if rigDir := beads.GetRigPathForPrefix(townRoot, w.rigName+"-"); rigDir != "" {
		return rigDir
	}

	return filepath.Join(townRoot, w.rigName)
}

// purgeRigBatch migrates one misplaced ephemeral bead from issues to wisps.
//
// This is deliberately fail-closed. Every target table must be present, every
// INSERT must succeed without an IGNORE clause, and the copied data must be
// read back before the source is eligible for deletion. A target collision or
// a missing table is not evidence that an earlier copy was complete.
func (c *CheckMisclassifiedWisps) purgeRigBatch(ctx *CheckContext, workDir, rigName, idList string) error {
	for _, table := range []string{
		"issues", "labels", "comments", "events", "dependencies",
		"wisps", "wisp_labels", "wisp_comments", "wisp_events", "wisp_dependencies",
	} {
		if !bdTableExistsDoctor(workDir, table) {
			return fmt.Errorf("required table %q is unavailable; kept source", table)
		}
	}

	// Step 1: Copy the primary row. Do not use INSERT IGNORE: an ignored row
	// could be stale or malformed, and is never safe to delete the source for.
	migrateQuery := fmt.Sprintf(
		"INSERT INTO wisps (%s) SELECT %s FROM issues WHERE id IN (%s)",
		misclassifiedWispColumns, misclassifiedWispColumns, idList)
	if err := execBdSQLWrite(workDir, migrateQuery); err != nil {
		return fmt.Errorf("migrate to wisps: %w", err)
	}

	// Step 2: Copy every related row, including comment authors. A later
	// verification query checks the content of each copied row.
	auxCopies := []struct {
		query string
	}{
		{
			query: fmt.Sprintf("INSERT INTO wisp_labels (issue_id, label) SELECT l.issue_id, l.label FROM labels l WHERE l.issue_id IN (%s)", idList),
		},
		{
			query: fmt.Sprintf("INSERT INTO wisp_comments (id, issue_id, author, text, created_at) SELECT c.id, c.issue_id, c.author, c.text, c.created_at FROM comments c WHERE c.issue_id IN (%s)", idList),
		},
		{
			query: fmt.Sprintf("INSERT INTO wisp_events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at) SELECT e.id, e.issue_id, e.event_type, e.actor, e.old_value, e.new_value, e.comment, e.created_at FROM events e WHERE e.issue_id IN (%s)", idList),
		},
		{
			query: fmt.Sprintf("INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT d.id, d.issue_id, CASE WHEN target_wisp.id IS NULL THEN d.depends_on_issue_id ELSE NULL END, CASE WHEN target_wisp.id IS NOT NULL THEN d.depends_on_issue_id ELSE d.depends_on_wisp_id END, d.depends_on_external, d.type, d.created_at, d.created_by, d.metadata, d.thread_id FROM dependencies d LEFT JOIN wisps target_wisp ON target_wisp.id = d.depends_on_issue_id WHERE d.issue_id IN (%s)", idList),
		},
	}
	for _, aux := range auxCopies {
		if err := execBdSQLWrite(workDir, aux.query); err != nil {
			return fmt.Errorf("copy related rows: %w", err)
		}
	}

	// Retarget incoming dependencies only after the source dependency rows have
	// copied successfully. These are part of the target graph and therefore are
	// also verified before anything from the source is removed.
	if err := execBdSQLWrite(workDir, fmt.Sprintf("UPDATE wisp_dependencies SET depends_on_wisp_id = depends_on_issue_id, depends_on_issue_id = NULL WHERE depends_on_issue_id IN (%s)", idList)); err != nil {
		return fmt.Errorf("retargeting incoming wisp dependencies: %w", err)
	}
	if err := execBdSQLWrite(workDir, fmt.Sprintf("UPDATE dependencies SET depends_on_wisp_id = depends_on_issue_id, depends_on_issue_id = NULL WHERE depends_on_issue_id IN (%s)", idList)); err != nil {
		return fmt.Errorf("retargeting incoming dependencies: %w", err)
	}

	if err := verifyMisclassifiedWispCopy(workDir, idList); err != nil {
		return fmt.Errorf("verify copied rows: %w", err)
	}

	// Step 3: Delete the source only if it is still closed, old enough, and
	// unprotected at the exact delete query. Keeping labels and comments until
	// this point makes the protection re-check meaningful.
	deleteQuery := fmt.Sprintf(`DELETE FROM issues
WHERE id IN (%s)
  AND status = 'closed'
  AND closed_at IS NOT NULL
  AND closed_at <= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 7 DAY)
  AND NOT EXISTS (SELECT 1 FROM labels l WHERE l.issue_id = issues.id AND (
    l.label = 'gt:merge-request'
    OR l.label IN ('from:crew/compliance', 'from:crew/compliance_b')
    OR l.label LIKE 'from:%%/crew/compliance'
    OR l.label LIKE 'from:%%/crew/compliance_b'
  ))
  AND NOT EXISTS (SELECT 1 FROM comments c WHERE c.issue_id = issues.id AND (
    c.author IN ('crew/compliance', 'crew/compliance_b')
    OR c.author LIKE '%%/crew/compliance'
    OR c.author LIKE '%%/crew/compliance_b'
  ))`, idList)
	if err := execBdSQLWrite(workDir, deleteQuery); err != nil {
		return fmt.Errorf("delete verified source: %w", err)
	}
	absent, err := misclassifiedWispSourceAbsent(workDir, idList)
	if err != nil {
		return fmt.Errorf("verify source deletion: %w", err)
	}
	if !absent {
		return fmt.Errorf("source no longer met deletion safeguards; kept source")
	}

	// Step 4: Now, and only now, clean source auxiliary rows. The absence check
	// above is the only evidence used for the destructive migration record.
	auxDeletes := []string{
		fmt.Sprintf("DELETE FROM labels WHERE issue_id IN (%s)", idList),
		fmt.Sprintf("DELETE FROM comments WHERE issue_id IN (%s)", idList),
		fmt.Sprintf("DELETE FROM events WHERE issue_id IN (%s)", idList),
		fmt.Sprintf("DELETE FROM dependencies WHERE issue_id IN (%s)", idList),
	}
	for _, q := range auxDeletes {
		if err := execBdSQLWrite(workDir, q); err != nil {
			return fmt.Errorf("clean verified-absent source rows: %w", err)
		}
	}

	// Step 5: Commit to Dolt history.
	commitMsg := "fix: migrate misplaced ephemeral beads to wisps table (gt doctor)"
	if err := commitMisclassifiedWispWorkingSet(ctx.TownRoot, rigName, commitMsg); err != nil {
		_ = err // Non-fatal
	}

	return nil
}

// misclassifiedWispColumns is deliberately shared by INSERT and verification.
// It contains every column common to issues and wisps, so a migration does not
// silently discard newer bead metadata when schemas evolve.
const misclassifiedWispColumns = "id, content_hash, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, estimated_minutes, created_at, created_by, owner, updated_at, closed_at, closed_by_session, external_ref, spec_id, compaction_level, compacted_at, compacted_at_commit, original_size, sender, ephemeral, wisp_type, pinned, is_template, mol_type, work_type, source_system, metadata, source_repo, close_reason, event_kind, actor, target, payload, await_type, await_id, timeout_ns, waiters, hook_bead, role_bead, agent_state, last_activity, role_type, rig, due_at, defer_until, no_history, started_at, is_blocked"

// verifyMisclassifiedWispCopy compares source and target row counts and then
// checks every copied row. Comments are checked as a multiset of SHA-256
// content digests which includes author, text, and creation timestamp.
func verifyMisclassifiedWispCopy(workDir, idList string) error {
	checks := []struct {
		name  string
		query string
	}{
		{"issues count", fmt.Sprintf("SELECT (SELECT COUNT(*) FROM issues WHERE id IN (%s)) - (SELECT COUNT(*) FROM wisps WHERE id IN (%s))", idList, idList)},
		{"issue content", fmt.Sprintf("SELECT COUNT(*) FROM issues i LEFT JOIN wisps w ON w.id = i.id WHERE i.id IN (%s) AND (w.id IS NULL OR NOT (%s))", idList, misclassifiedWispEqualColumns("i", "w"))},
		{"labels count", fmt.Sprintf("SELECT (SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)) - (SELECT COUNT(*) FROM wisp_labels WHERE issue_id IN (%s))", idList, idList)},
		{"labels content", fmt.Sprintf("SELECT COUNT(*) FROM labels l LEFT JOIN wisp_labels w ON w.issue_id = l.issue_id AND w.label <=> l.label WHERE l.issue_id IN (%s) AND w.issue_id IS NULL", idList)},
		{"events count", fmt.Sprintf("SELECT (SELECT COUNT(*) FROM events WHERE issue_id IN (%s)) - (SELECT COUNT(*) FROM wisp_events WHERE issue_id IN (%s))", idList, idList)},
		{"events content", fmt.Sprintf("SELECT COUNT(*) FROM events e LEFT JOIN wisp_events w ON w.id <=> e.id AND w.issue_id = e.issue_id AND w.event_type <=> e.event_type AND w.actor <=> e.actor AND w.old_value <=> e.old_value AND w.new_value <=> e.new_value AND w.comment <=> e.comment AND w.created_at <=> e.created_at WHERE e.issue_id IN (%s) AND w.issue_id IS NULL", idList)},
		{"dependencies count", fmt.Sprintf("SELECT (SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s)) - (SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id IN (%s))", idList, idList)},
		{"dependencies content", fmt.Sprintf("SELECT COUNT(*) FROM dependencies d LEFT JOIN wisp_dependencies w ON w.id <=> d.id AND w.issue_id = d.issue_id AND w.depends_on_issue_id <=> d.depends_on_issue_id AND w.depends_on_wisp_id <=> d.depends_on_wisp_id AND w.depends_on_external <=> d.depends_on_external AND w.type <=> d.type AND w.created_at <=> d.created_at AND w.created_by <=> d.created_by AND CAST(w.metadata AS CHAR) <=> CAST(d.metadata AS CHAR) AND w.thread_id <=> d.thread_id WHERE d.issue_id IN (%s) AND w.issue_id IS NULL", idList)},
		{"comments count", fmt.Sprintf("SELECT (SELECT COUNT(*) FROM comments WHERE issue_id IN (%s)) - (SELECT COUNT(*) FROM wisp_comments WHERE issue_id IN (%s))", idList, idList)},
		{"comment content digests", fmt.Sprintf(`SELECT COUNT(*) FROM (
SELECT issue_id, SHA2(CONCAT_WS('|', COALESCE(id, '<NULL>'), COALESCE(author, '<NULL>'), COALESCE(text, '<NULL>'), COALESCE(CAST(created_at AS CHAR), '<NULL>')), 256) AS digest, COUNT(*) AS copies
FROM comments WHERE issue_id IN (%s) GROUP BY issue_id, digest
) source LEFT JOIN (
SELECT issue_id, SHA2(CONCAT_WS('|', COALESCE(id, '<NULL>'), COALESCE(author, '<NULL>'), COALESCE(text, '<NULL>'), COALESCE(CAST(created_at AS CHAR), '<NULL>')), 256) AS digest, COUNT(*) AS copies
FROM wisp_comments WHERE issue_id IN (%s) GROUP BY issue_id, digest
) target ON target.issue_id = source.issue_id AND target.digest = source.digest
WHERE target.issue_id IS NULL OR target.copies <> source.copies`, idList, idList)},
	}
	for _, check := range checks {
		value, err := bdScalarDoctor(workDir, check.query)
		if err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		if value != "0" {
			return fmt.Errorf("%s mismatch (%s)", check.name, value)
		}
	}
	return nil
}

func misclassifiedWispEqualColumns(source, target string) string {
	columns := strings.Split(misclassifiedWispColumns, ", ")
	comparisons := make([]string, 0, len(columns))
	for _, column := range columns {
		if column == "metadata" {
			comparisons = append(comparisons, "CAST("+source+"."+column+" AS CHAR) <=> CAST("+target+"."+column+" AS CHAR)")
			continue
		}
		comparisons = append(comparisons, source+"."+column+" <=> "+target+"."+column)
	}
	return strings.Join(comparisons, " AND ")
}

func misclassifiedWispSourceAbsent(workDir, idList string) (bool, error) {
	value, err := bdScalarDoctor(workDir, fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id IN (%s)", idList))
	if err != nil {
		return false, err
	}
	return value == "0", nil
}

// bdScalarDoctor executes an exact scalar verification query. An unreadable or
// malformed response is an error, never interpreted as an empty result.
func bdScalarDoctor(workDir, query string) (string, error) {
	cmd := exec.Command("bd", "sql", "--csv", query) //nolint:gosec // G204: query is built from fixed SQL fragments
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil || len(records) != 2 || len(records[1]) != 1 {
		return "", fmt.Errorf("unexpected scalar result %q", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(records[1][0]), nil
}

// bdTableExistsDoctor checks if a table exists by attempting to query it.
// Doctor-local wrapper (wisps_migrate.go has its own unexported copy).
func bdTableExistsDoctor(workDir, tableName string) bool {
	cmd := exec.Command("bd", "sql", fmt.Sprintf("SELECT 1 FROM `%s` LIMIT 1", tableName)) //nolint:gosec // G204: tableName is hardcoded
	cmd.Dir = workDir
	err := cmd.Run()
	return err == nil
}
