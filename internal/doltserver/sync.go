package doltserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/wispaudit"
)

// SyncOptions controls the behavior of SyncDatabases.
type SyncOptions struct {
	// Force enables --force on dolt push.
	Force bool

	// DryRun prints what would be pushed without actually pushing.
	DryRun bool

	// Filter restricts sync to a single database name. Empty means all.
	Filter string
}

// SyncResult records the outcome of syncing a single database.
type SyncResult struct {
	// Database is the rig database name.
	Database string

	// Pushed is true if dolt push succeeded.
	Pushed bool

	// Skipped is true if the database was skipped (e.g., no remote configured).
	Skipped bool

	// DryRun is true if this was a dry-run (no actual push).
	DryRun bool

	// Error is non-nil if the push failed.
	Error error

	// Remote is the origin push URL, or empty if none configured.
	Remote string
}

// FindRemote returns the name and URL of the first configured remote in a Dolt database.
// Returns ("", "", nil) if no remotes are configured.
func FindRemote(dbDir string) (name, url string, err error) {
	cmd := exec.Command("dolt", "remote", "-v")
	cmd.Dir = dbDir
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("dolt remote -v: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	// Parse output lines looking for any remote URL.
	// Dolt format: "origin https://doltremoteapi.dolthub.com/org/repo {}"
	// Git format:  "origin  https://... (push)"
	// Remote names may be "origin", "github", or any user-defined name.
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			return parts[0], parts[1], nil
		}
	}

	return "", "", nil
}

// HasRemote checks whether a Dolt database directory has any remote configured.
// Returns the push URL if found, or empty string if no remote exists.
// Deprecated: use FindRemote for both the remote name and URL.
func HasRemote(dbDir string) (string, error) {
	_, url, err := FindRemote(dbDir)
	return url, err
}

// CommitWorkingSet stages and commits any uncommitted changes in a Dolt database directory.
// Treats "nothing to commit" as success (not an error).
func CommitWorkingSet(dbDir string) error {
	// Stage all changes
	addCmd := exec.Command("dolt", "add", ".")
	addCmd.Dir = dbDir
	setProcessGroup(addCmd)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dolt add: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	// Commit (may fail with "nothing to commit" which is fine)
	commitCmd := exec.Command("dolt", "commit", "-m", "gt dolt sync: auto-commit working changes")
	commitCmd.Dir = dbDir
	setProcessGroup(commitCmd)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		// "nothing to commit" or "no changes added" is success — no changes to push
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "nothing to commit") || strings.Contains(lower, "no changes added") {
			return nil
		}
		return fmt.Errorf("dolt commit: %w (%s)", err, msg)
	}

	return nil
}

// PushDatabase pushes a Dolt database directory to the specified remote's main branch.
// If force is true, uses --force. Requires the Dolt server to be stopped (CLI mode).
func PushDatabase(dbDir, remote string, force bool) error {
	args := []string{"push", remote, "main"}
	if force {
		args = append(args, "--force")
	}

	cmd := exec.Command("dolt", args...)
	cmd.Dir = dbDir
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt push: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// validSQLName checks that a database or remote name contains only safe characters
// (alphanumeric, underscore, hyphen, dot). This is a defense-in-depth measure since
// these values come from internal sources (filesystem scan, SQL query output), but
// prevents SQL breakage or injection if a name ever contains backticks or quotes.
func validSQLName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// PullDatabaseSQL pulls a database from its remote via SQL (CALL DOLT_PULL) through
// the running Dolt server. This avoids lock contention with the server process.
func PullDatabaseSQL(townRoot, db, remote string) error {
	if !validSQLName(db) {
		return fmt.Errorf("invalid database name %q: must match [a-zA-Z0-9_.-]+", db)
	}
	if !validSQLName(remote) {
		return fmt.Errorf("invalid remote name %q: must match [a-zA-Z0-9_.-]+", remote)
	}

	// Pull via SQL — fetch + merge through the running server
	pullQuery := fmt.Sprintf("USE `%s`; CALL DOLT_PULL('%s')", db, remote)

	// Pull can be slow for large databases or slow remotes
	config := DefaultConfig(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := buildDoltSQLCmd(ctx, config, "-q", pullQuery)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("DOLT_PULL: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// PullDatabase pulls a Dolt database directory from the specified remote's main branch
// using the CLI. Requires the Dolt server to be stopped (CLI mode).
func PullDatabase(dbDir, remote string) error {
	cmd := exec.Command("dolt", "pull", remote, "main")
	cmd.Dir = dbDir
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dolt pull: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// PullDatabasesSQL iterates all databases (or a filtered subset) and pulls via SQL
// through the running Dolt server. This avoids lock contention between the CLI and server.
func PullDatabasesSQL(townRoot string, opts SyncOptions) []SyncResult {
	databases, err := ListDatabases(townRoot)
	if err != nil {
		return []SyncResult{{
			Database: "(list)",
			Error:    fmt.Errorf("listing databases: %w", err),
		}}
	}

	var results []SyncResult

	for _, db := range databases {
		if opts.Filter != "" && db != opts.Filter {
			continue
		}

		result := SyncResult{Database: db}

		// Skip databases with a .no-sync marker file (local-only databases),
		// unless explicitly requested via Filter (--db flag).
		dbDir := RigDatabaseDir(townRoot, db)
		if opts.Filter == "" {
			if _, err := os.Stat(filepath.Join(dbDir, ".no-sync")); err == nil {
				result.Skipped = true
				results = append(results, result)
				continue
			}
		}

		// Check for remote via SQL
		remoteName, remoteURL, err := FindRemoteSQL(townRoot, db)
		if err != nil {
			result.Error = fmt.Errorf("checking remote: %w", err)
			results = append(results, result)
			continue
		}
		result.Remote = remoteURL

		if remoteURL == "" {
			result.Skipped = true
			results = append(results, result)
			continue
		}

		if opts.DryRun {
			result.DryRun = true
			results = append(results, result)
			continue
		}

		// Pull via SQL (server stays running)
		if err := PullDatabaseSQL(townRoot, db, remoteName); err != nil {
			result.Error = err
			results = append(results, result)
			continue
		}

		result.Pushed = true // reusing Pushed field to indicate success
		results = append(results, result)
	}

	return results
}

// PullDatabases iterates all databases (or a filtered subset) and pulls via CLI.
// Requires the Dolt server to be stopped.
func PullDatabases(townRoot string, opts SyncOptions) []SyncResult {
	databases, err := ListDatabases(townRoot)
	if err != nil {
		return []SyncResult{{
			Database: "(list)",
			Error:    fmt.Errorf("listing databases: %w", err),
		}}
	}

	var results []SyncResult

	for _, db := range databases {
		if opts.Filter != "" && db != opts.Filter {
			continue
		}

		dbDir := RigDatabaseDir(townRoot, db)
		result := SyncResult{Database: db}

		// Skip databases with a .no-sync marker file,
		// unless explicitly requested via Filter (--db flag).
		if opts.Filter == "" {
			if _, err := os.Stat(filepath.Join(dbDir, ".no-sync")); err == nil {
				result.Skipped = true
				results = append(results, result)
				continue
			}
		}

		// Check for remote
		remoteName, remoteURL, err := FindRemote(dbDir)
		if err != nil {
			result.Error = fmt.Errorf("checking remote: %w", err)
			results = append(results, result)
			continue
		}
		result.Remote = remoteURL

		if remoteURL == "" {
			result.Skipped = true
			results = append(results, result)
			continue
		}

		if opts.DryRun {
			result.DryRun = true
			results = append(results, result)
			continue
		}

		if err := PullDatabase(dbDir, remoteName); err != nil {
			result.Error = err
			results = append(results, result)
			continue
		}

		result.Pushed = true // reusing Pushed field to indicate success
		results = append(results, result)
	}

	return results
}

// PushDatabaseSQL pushes a database to its remote via SQL (CALL DOLT_PUSH) through
// the running Dolt server. This avoids stopping the server and crashing all agents.
func PushDatabaseSQL(townRoot, db, remote string, force bool) error {
	if !validSQLName(db) {
		return fmt.Errorf("invalid database name %q: must match [a-zA-Z0-9_.-]+", db)
	}
	if !validSQLName(remote) {
		return fmt.Errorf("invalid remote name %q: must match [a-zA-Z0-9_.-]+", remote)
	}

	// Stage any unstaged changes
	addQuery := fmt.Sprintf("USE `%s`; CALL DOLT_ADD('-A')", db)
	if err := serverExecSQL(townRoot, addQuery); err != nil {
		// Non-fatal — may have nothing to stage
		errStr := err.Error()
		if !strings.Contains(errStr, "nothing to commit") && !strings.Contains(errStr, "no changes") {
			fmt.Fprintf(os.Stderr, "  %s: add (non-fatal): %v\n", db, err)
		}
	}

	// Commit working set
	commitQuery := fmt.Sprintf(
		"USE `%s`; CALL DOLT_COMMIT('-m', 'gt dolt sync: auto-commit working changes', '--allow-empty', '--author', 'Gas Town Sync <sync@gastown.local>')",
		db,
	)
	if err := serverExecSQL(townRoot, commitQuery); err != nil {
		errStr := err.Error()
		if !strings.Contains(errStr, "nothing to commit") && !strings.Contains(errStr, "no changes") {
			fmt.Fprintf(os.Stderr, "  %s: commit (non-fatal): %v\n", db, err)
		}
	}

	// Push via SQL — this works through the running server
	pushQuery := fmt.Sprintf("USE `%s`; CALL DOLT_PUSH('%s', 'main')", db, remote)
	if force {
		pushQuery = fmt.Sprintf("USE `%s`; CALL DOLT_PUSH('--force', '%s', 'main')", db, remote)
	}

	// Push can be slow for large databases — use a longer timeout
	config := DefaultConfig(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := buildDoltSQLCmd(ctx, config, "-q", pushQuery)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("DOLT_PUSH: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// FindRemoteSQL returns the name and URL of the first remote for a database
// via SQL query through the running server.
func FindRemoteSQL(townRoot, db string) (name, url string, err error) {
	if !validSQLName(db) {
		return "", "", fmt.Errorf("invalid database name %q: must match [a-zA-Z0-9_.-]+", db)
	}
	config := DefaultConfig(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name, url FROM dolt_remotes LIMIT 1", db)
	cmd := buildDoltSQLCmd(ctx, config, "-r", "csv", "-q", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("querying remotes for %s: %w (%s)", db, err, strings.TrimSpace(string(output)))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return "", "", nil // no remotes
	}

	parts := strings.SplitN(strings.TrimSpace(lines[1]), ",", 2)
	if len(parts) < 2 {
		return "", "", nil
	}
	return parts[0], parts[1], nil
}

// SyncDatabases iterates all databases (or a filtered subset), checks for remotes,
// commits working changes, and pushes to origin. Never fails fast — collects all results.
func SyncDatabases(townRoot string, opts SyncOptions) []SyncResult {
	databases, err := ListDatabases(townRoot)
	if err != nil {
		return []SyncResult{{
			Database: "(list)",
			Error:    fmt.Errorf("listing databases: %w", err),
		}}
	}

	var results []SyncResult

	for _, db := range databases {
		// Apply filter if set
		if opts.Filter != "" && db != opts.Filter {
			continue
		}

		dbDir := RigDatabaseDir(townRoot, db)
		result := SyncResult{Database: db}

		// Skip databases with a .no-sync marker file (local-only databases),
		// unless explicitly requested via Filter (--db flag).
		if opts.Filter == "" {
			if _, err := os.Stat(filepath.Join(dbDir, ".no-sync")); err == nil {
				result.Skipped = true
				results = append(results, result)
				continue
			}
		}

		// Check for remote (any name — "origin", "github", etc.)
		remoteName, remoteURL, err := FindRemote(dbDir)
		if err != nil {
			result.Error = fmt.Errorf("checking remote: %w", err)
			results = append(results, result)
			continue
		}
		result.Remote = remoteURL

		if remoteURL == "" {
			// Auto-setup DoltHub remote if credentials are available.
			token := DoltHubToken()
			org := DoltHubOrg()
			if token != "" && org != "" {
				if err := SetupDoltHubRemote(dbDir, org, db, token); err != nil {
					// Setup failed — skip this database for now.
					result.Error = fmt.Errorf("auto-setup DoltHub remote: %w", err)
					results = append(results, result)
					continue
				}
				// Remote is now configured; re-read it.
				remoteName, remoteURL, err = FindRemote(dbDir)
				if err != nil || remoteURL == "" {
					result.Error = fmt.Errorf("remote not found after auto-setup")
					results = append(results, result)
					continue
				}
				result.Remote = remoteURL
			} else {
				result.Skipped = true
				results = append(results, result)
				continue
			}
		}

		if opts.DryRun {
			result.DryRun = true
			results = append(results, result)
			continue
		}

		// Commit working set
		if err := CommitWorkingSet(dbDir); err != nil {
			result.Error = fmt.Errorf("committing: %w", err)
			results = append(results, result)
			continue
		}

		// Push
		if err := PushDatabase(dbDir, remoteName, opts.Force); err != nil {
			result.Error = err
			results = append(results, result)
			continue
		}

		result.Pushed = true
		results = append(results, result)
	}

	return results
}

// SyncDatabasesSQL iterates all databases (or a filtered subset) and pushes via SQL
// through the running Dolt server. Unlike SyncDatabases, this does NOT require
// stopping the server, so it won't crash running agents.
func SyncDatabasesSQL(townRoot string, opts SyncOptions) []SyncResult {
	databases, err := ListDatabases(townRoot)
	if err != nil {
		return []SyncResult{{
			Database: "(list)",
			Error:    fmt.Errorf("listing databases: %w", err),
		}}
	}

	var results []SyncResult

	for _, db := range databases {
		if opts.Filter != "" && db != opts.Filter {
			continue
		}

		result := SyncResult{Database: db}

		// Skip databases with a .no-sync marker file (local-only databases),
		// unless explicitly requested via Filter (--db flag).
		dbDir := RigDatabaseDir(townRoot, db)
		if opts.Filter == "" {
			if _, err := os.Stat(filepath.Join(dbDir, ".no-sync")); err == nil {
				result.Skipped = true
				results = append(results, result)
				continue
			}
		}

		// Check for remote via SQL
		remoteName, remoteURL, err := FindRemoteSQL(townRoot, db)
		if err != nil {
			result.Error = fmt.Errorf("checking remote: %w", err)
			results = append(results, result)
			continue
		}
		result.Remote = remoteURL

		if remoteURL == "" {
			// Try auto-setup if credentials are available
			token := DoltHubToken()
			org := DoltHubOrg()
			if token != "" && org != "" {
				if err := SetupDoltHubRemote(dbDir, org, db, token); err != nil {
					result.Error = fmt.Errorf("auto-setup DoltHub remote: %w", err)
					results = append(results, result)
					continue
				}
				remoteName, remoteURL, err = FindRemoteSQL(townRoot, db)
				if err != nil || remoteURL == "" {
					result.Error = fmt.Errorf("remote not found after auto-setup")
					results = append(results, result)
					continue
				}
				result.Remote = remoteURL
			} else {
				result.Skipped = true
				results = append(results, result)
				continue
			}
		}

		if opts.DryRun {
			result.DryRun = true
			results = append(results, result)
			continue
		}

		// Push via SQL (server stays running)
		if err := PushDatabaseSQL(townRoot, db, remoteName, opts.Force); err != nil {
			result.Error = err
			results = append(results, result)
			continue
		}

		result.Pushed = true
		results = append(results, result)
	}

	return results
}

// PurgeClosedEphemerals runs "bd purge" for a specific rig database to remove
// closed ephemeral beads (wisps, convoys) before pushing to DoltHub.
// Returns the number of beads purged and any error encountered.
// Errors are non-fatal — the caller should log them but continue with sync.
// Must be called while the Dolt server is still running (bd purge needs SQL access).
//
// path names the caller for the deletion record (hq-6ewp): what this removes is
// in dolt_ignore, so it is never committed and no AS OF can read it back, and
// the record in <town>/.events.jsonl is the only thing that survives it. The
// record is written first, and a database whose record will not write is not
// purged — the caller sees an error and continues with the rest of the sync.
func PurgeClosedEphemerals(townRoot, dbName, path string, dryRun bool) (int, error) {
	env, workDir, ok, err := resolvePurgeWorkdir(townRoot, dbName)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil // no beads dir, or not initialized — nothing to purge
	}

	// bd purge reports a count and never the ids it removed, so the set is
	// enumerated here instead and recorded before the purge runs. It is also
	// how gt-12f's protected-bead exclusion is applied: `bd purge` has no
	// concept of a protected bead (it only protects pinned rows), so a
	// merge-request bead or a compliance-commented bead must be identified and
	// withheld here, before bd ever runs.
	//
	// A listing failure must abort the purge rather than fall through to bd's
	// own blanket definition (gt-12f round 2): an empty exclusion set because
	// the list could not be read is indistinguishable, at the isZero() check
	// below, from an empty exclusion set because nothing protected exists — and
	// only the second one is safe to hand to bd's unconditional purge.
	doomed, excluded, err := planClosedEphemeralPurge(env, workDir)
	if err != nil {
		return 0, fmt.Errorf("skipping purge for %s: could not list closed ephemerals first: %w", dbName, err)
	}

	if dryRun {
		return len(doomed), nil
	}

	extra := map[string]interface{}{"predicted": true}
	excluded.asExtra(extra)
	if err := wispaudit.Plan(wispaudit.Actor("gt"), path, "database", dbName, doomed, extra); err != nil {
		return 0, fmt.Errorf("skipping purge for %s: the deletion could not be recorded first: %w", dbName, err)
	}

	var purgedCount int
	if excluded.isZero() {
		// Fast path: nothing protected is sitting in this database right now,
		// so bd's own blanket purge is exactly as safe as the explicit delete
		// below and far cheaper for the common case.
		purgedCount, err = runBDPurge(env, workDir, dbName)
	} else {
		// A protected bead exists among the closed ephemerals. bd purge has no
		// way to exclude it, so the real delete runs only against the filtered
		// id list instead of bd's own blanket definition.
		purgedCount, err = deleteWispsByID(env, workDir, dbName, doomed)
	}
	if err != nil {
		return 0, err
	}

	// Only on success, and only here. Every earlier return is a purge that did
	// NOT happen, and a "completed" record for one of those would tell a later
	// investigation that a set of wisps went when it is still sitting in the
	// database — a false record is worse than a missing one. The planned record
	// already stands in every case, which is the point of writing it first.
	extra["reported_count"] = purgedCount

	// Post-delete verification (gt-12f round 2): the cl-wisp-0u30 shape was a
	// wisp named in 202 "completed" purge events that was never actually gone.
	// A reported count or a predicted id list is not evidence a row is gone;
	// re-querying the predicted set and recording only what is confirmed absent
	// is. Survivors are named so a later investigation does not have to
	// rediscover that the purge silently failed for part of its set.
	var failures []string
	if survivors, verifyErr := verifyPurgeSurvivors(env, workDir, doomed); verifyErr != nil {
		extra["verify_error"] = verifyErr.Error()
	} else if len(survivors) > 0 {
		extra["survived_purge"] = survivors
		for _, id := range survivors {
			failures = append(failures, id+": still present after purge")
		}
	}
	_ = wispaudit.Completed(wispaudit.Actor("gt"), path, "database", dbName, doomed, failures, extra)

	return purgedCount, nil
}

// runBDPurge runs bd's own blanket purge (no --older-than: this GC removes any
// closed ephemeral bead, not just old ones) and parses its reported count. Only
// safe to call when planClosedEphemeralPurge found nothing protected to exclude.
func runBDPurge(env []string, workDir, dbName string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := beads.MaybePrependAllowStaleWithEnv(env, []string{"purge", "--json"})
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = workDir
	cmd.Env = env
	setProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("bd purge for %s: timed out after 60s", dbName)
	}
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = strings.TrimSpace(stdout.String())
		}
		return 0, fmt.Errorf("bd purge for %s: %w (%s)", dbName, err, errMsg)
	}

	// Parse JSON output (from stdout only) to get purged count.
	// bd may emit non-JSON warning lines before the JSON object,
	// so extract the first JSON object from stdout.
	jsonBytes := extractJSON(stdout.Bytes())
	var result struct {
		PurgedCount *int `json:"purged_count"`
	}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return 0, fmt.Errorf("bd purge for %s: unexpected output format: %s", dbName, strings.TrimSpace(stdout.String()))
	}

	// Warn if purged_count field was missing from the JSON response — may indicate
	// a schema mismatch (e.g., field renamed). An explicit 0 is a valid success case.
	if result.PurgedCount == nil {
		fmt.Fprintf(os.Stderr, "Warning: bd purge for %s: purged_count field missing (raw: %s)\n", dbName, strings.TrimSpace(stdout.String()))
		return 0, nil
	}
	return *result.PurgedCount, nil
}

// deleteWispsByID deletes exactly the given wisps by id, in batches, instead
// of bd's blanket purge. Used only when the predicted set excludes a
// protected bead, so bd's own "closed ephemeral" definition — which has no
// notion of a protected bead — never runs against this database.
func deleteWispsByID(env []string, workDir, dbName string, wisps []wispaudit.Wisp) (int, error) {
	const batchSize = 100
	deleted := 0
	for start := 0; start < len(wisps); start += batchSize {
		end := start + batchSize
		if end > len(wisps) {
			end = len(wisps)
		}
		batch := wisps[start:end]
		args := []string{"delete", "--force"}
		for _, w := range batch {
			args = append(args, w.ID)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, "bd", args...)
		cmd.Dir = workDir
		cmd.Env = env
		setProcessGroup(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			return deleted, fmt.Errorf("bd delete for %s: timed out after 60s", dbName)
		}
		if err != nil {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg == "" {
				errMsg = strings.TrimSpace(stdout.String())
			}
			return deleted, fmt.Errorf("bd delete for %s: %w (%s)", dbName, err, errMsg)
		}
		deleted += len(batch)
	}
	return deleted, nil
}

// verifyPurgeSurvivors re-queries ids after a purge and reports which of them
// are still present. `bd show --json <ids...>` returns a JSON array
// containing only the ids it could find — the same tolerant-of-missing-ids
// behavior ShowMultiple relies on elsewhere in this tree — so anything that
// comes back is evidence the delete did not actually take for that id (the
// cl-wisp-0u30 shape: a purge reported success for a row that stayed live).
//
// A failure to run the check itself is reported as an error, never silently
// treated as "everything survived" or "everything is gone" — an unverified
// batch is an unknown, not a verdict.
func verifyPurgeSurvivors(env []string, workDir string, wisps []wispaudit.Wisp) ([]string, error) {
	ids := wispaudit.IDs(wisps)
	if len(ids) == 0 {
		return nil, nil
	}
	const batchSize = 100
	var survivors []string
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		args := append([]string{"show", "--json"}, batch...)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, "bd", args...)
		cmd.Dir = workDir
		cmd.Env = env
		setProcessGroup(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			return survivors, fmt.Errorf("verifying purge: bd show timed out after 30s")
		}
		if err != nil {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg == "" {
				errMsg = strings.TrimSpace(stdout.String())
			}
			return survivors, fmt.Errorf("verifying purge: bd show: %w (%s)", err, errMsg)
		}

		out := extractJSONArray(stdout.Bytes())
		if len(out) == 0 || out[0] != '[' {
			continue // bd's "nothing found" shape — the whole batch is confirmed gone
		}
		var found []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(out, &found); err != nil {
			return survivors, fmt.Errorf("verifying purge: parsing bd show output: %w", err)
		}
		for _, f := range found {
			survivors = append(survivors, f.ID)
		}
	}
	return survivors, nil
}

// resolvePurgeWorkdir resolves the bd environment and working directory for a
// rig's closed-ephemeral purge, shared by PurgeClosedEphemerals (which acts)
// and PreviewClosedEphemeralsPurge (which only looks). ok=false means there is
// nothing to purge for this database (no beads dir, or not yet initialized) —
// not an error.
func resolvePurgeWorkdir(townRoot, dbName string) (env []string, workDir string, ok bool, err error) {
	// Resolve the beads directory for this rig (read-only — never create dirs during purge)
	beadsDir := FindRigBeadsDir(townRoot, dbName)

	// Check that the beads directory actually exists on disk.
	// FindRigBeadsDir returns a path even for non-existent directories,
	// so we must verify existence explicitly.
	if _, statErr := os.Stat(beadsDir); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("checking beads dir for %s: %w", dbName, statErr)
	}

	// Skip databases with uninitialized beads dirs (no metadata.json).
	// An empty .beads/ directory causes bd to attempt a fresh bootstrap,
	// which hangs waiting on dolt init or lock acquisition.
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if info, statErr := os.Stat(metadataPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("checking metadata for %s: %w", dbName, statErr)
	} else if info.IsDir() {
		return nil, "", false, fmt.Errorf("metadata.json for %s is a directory", dbName)
	}

	// bd purge v2 uses batched SQL (completes in seconds), but we keep a
	// generous timeout as a circuit breaker against future regressions.
	env = beads.BuildMutationPinnedBDEnv(os.Environ(), beadsDir)
	workDir = filepath.Dir(beadsDir) // run from parent of .beads
	return env, workDir, true, nil
}

// PreviewClosedEphemeralsPurge returns the exact candidate wisps — and what
// gt-12f's exclusions withheld — that PurgeClosedEphemerals would remove for
// dbName, without deleting or recording anything. It is the same
// planClosedEphemeralPurge call the real purge makes, so a dry-run caller
// cannot show a different set than the one that would actually be deleted
// (gt-12f: "--dry-run must show the purge" means the id list, not a count).
func PreviewClosedEphemeralsPurge(townRoot, dbName string) ([]wispaudit.Wisp, purgeExclusion, error) {
	env, workDir, ok, err := resolvePurgeWorkdir(townRoot, dbName)
	if err != nil {
		return nil, purgeExclusion{}, err
	}
	if !ok {
		return nil, purgeExclusion{}, nil
	}
	return planClosedEphemeralPurge(env, workDir)
}

// closedEphemeralCandidate is one closed ephemeral bead considered for the
// pre-push/maintenance GC purge, carrying what gt-12f's exclusion rules need.
type closedEphemeralCandidate struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Labels       []string `json:"labels,omitempty"`
	CommentCount int      `json:"comment_count"`
}

// purgeExclusion counts candidates withheld from this GC purge, by reason.
type purgeExclusion struct {
	mergeRequest int
	compliance   int
	unreadable   int
}

func (e purgeExclusion) isZero() bool {
	return e.mergeRequest == 0 && e.compliance == 0 && e.unreadable == 0
}

func (e purgeExclusion) asExtra(extra map[string]interface{}) {
	if e.mergeRequest > 0 {
		extra["kept_merge_request"] = e.mergeRequest
	}
	if e.compliance > 0 {
		extra["kept_compliance_commented"] = e.compliance
	}
	if e.unreadable > 0 {
		extra["kept_unreadable"] = e.unreadable
	}
}

// planClosedEphemeralPurge names the wisps this GC purge is expected to
// remove from this database, for the deletion record, and excludes any that
// gt-12f/cl-kf00 protects: a bead labelled gt:merge-request, or carrying a
// comment from a compliance seat. A candidate whose comments cannot be read
// is excluded too (fail closed) — an unknown protection state is not evidence
// of safety to delete.
//
// It goes through `bd query ephemeral=true`, not `bd list`: bd list does not
// surface wisps (hq-v9t), so it would silently predict an empty set for exactly
// the rows this is trying to name.
//
// A listing error is returned, not swallowed (gt-12f round 2): the caller uses
// an empty purgeExclusion to decide whether bd's own blanket purge is safe to
// run, and a listing failure that produced doomed=nil, excluded={} was
// indistinguishable from "nothing protected exists" — which sent the blanket
// purge ahead with no exclusions at all. The caller must abort instead.
func planClosedEphemeralPurge(env []string, workDir string) ([]wispaudit.Wisp, purgeExclusion, error) {
	candidates, err := listClosedEphemerals(env, workDir)
	if err != nil {
		return nil, purgeExclusion{}, err
	}

	var doomed []wispaudit.Wisp
	var excluded purgeExclusion
	for _, w := range candidates {
		if w.Status != "closed" {
			continue
		}
		if wispaudit.HasProtectedLabel(w.Labels) {
			excluded.mergeRequest++
			continue
		}
		if w.CommentCount > 0 {
			protected, readable := wispCommentsAreProtected(env, workDir, w.ID)
			if !readable {
				excluded.unreadable++
				continue
			}
			if protected {
				excluded.compliance++
				continue
			}
		}
		doomed = append(doomed, wispaudit.Wisp{ID: w.ID, Title: w.Title})
	}
	return doomed, excluded, nil
}

// listClosedEphemerals queries every closed ephemeral bead in the database
// `bd` is bound to via env/workDir. A non-nil error means the listing could
// not be trusted — a command failure, a timeout, or output that does not
// parse as the JSON array bd promises with --json — and callers must not
// treat that the same as a confirmed-empty result (gt-12f round 2).
func listClosedEphemerals(env []string, workDir string) ([]closedEphemeralCandidate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Same --allow-stale probe the purge itself uses, so the prediction is not
	// the one call that fails on a stale database while the delete goes ahead.
	args := beads.MaybePrependAllowStaleWithEnv(env,
		[]string{"query", "--json", "ephemeral=true", "--all", "--limit=0"})
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = workDir
	cmd.Env = env
	setProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("bd query for closed ephemerals: timed out after 60s")
	}
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("bd query for closed ephemerals: %w (%s)", err, errMsg)
	}

	out := extractJSONArray(stdout.Bytes())
	if len(out) == 0 || out[0] != '[' {
		// bd answers a confirmed-empty result set with prose ("No issues
		// found."), which leaves nothing for extractJSONArray to find. That is
		// the one non-array shape treated as success; anything else unparsed
		// is an unknown state, not evidence the database holds nothing.
		trimmed := strings.ToLower(strings.TrimSpace(stdout.String()))
		if trimmed == "" || strings.Contains(trimmed, "no issues found") {
			return nil, nil
		}
		return nil, fmt.Errorf("bd query for closed ephemerals: unexpected output format: %s", strings.TrimSpace(stdout.String()))
	}
	var wisps []closedEphemeralCandidate
	if err := json.Unmarshal(out, &wisps); err != nil {
		return nil, fmt.Errorf("bd query for closed ephemerals: parsing output: %w", err)
	}
	return wisps, nil
}

// wispCommentsAreProtected reports whether id carries a comment from a
// compliance seat, and whether its comments could be read at all. Callers
// must treat readable=false as "do not purge", never as "not protected".
func wispCommentsAreProtected(env []string, workDir, id string) (protected, readable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bd", "comments", id, "--json")
	cmd.Dir = workDir
	cmd.Env = env
	setProcessGroup(cmd)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return false, false
	}

	return wispaudit.CommentsProtected(stdout.Bytes())
}

// extractJSONArray finds the first '[' in raw output that may carry a non-JSON
// preamble (warnings, notices), and returns from there onward.
func extractJSONArray(data []byte) []byte {
	start := bytes.IndexByte(data, '[')
	if start < 0 {
		return data
	}
	return data[start:]
}

// extractJSON finds the first JSON object in raw output that may contain
// non-JSON preamble (warnings, debug lines). Returns data from the first '{' onward,
// letting json.Unmarshal handle end-detection (it stops at the end of the first valid
// JSON value and tolerates trailing content).
func extractJSON(data []byte) []byte {
	start := bytes.IndexByte(data, '{')
	if start < 0 {
		return data
	}
	return data[start:]
}
