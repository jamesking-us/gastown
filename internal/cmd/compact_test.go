package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestGetTTL(t *testing.T) {
	ttls := defaultTTLs

	tests := []struct {
		wispType string
		want     time.Duration
	}{
		{"heartbeat", 6 * time.Hour},
		{"ping", 6 * time.Hour},
		{"patrol", 24 * time.Hour},
		{"gc_report", 24 * time.Hour},
		{"error", 7 * 24 * time.Hour},
		{"recovery", 7 * 24 * time.Hour},
		{"escalation", 7 * 24 * time.Hour},
		{"default", 24 * time.Hour},
		{"", 24 * time.Hour},        // empty falls back to default
		{"unknown", 24 * time.Hour}, // unknown falls back to default
	}

	for _, tc := range tests {
		t.Run(tc.wispType, func(t *testing.T) {
			got := getTTL(ttls, tc.wispType)
			if got != tc.want {
				t.Errorf("getTTL(%q) = %v, want %v", tc.wispType, got, tc.want)
			}
		})
	}
}

func TestWispAge(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		updatedAt string
		wantAge   time.Duration
		wantErr   bool
	}{
		{
			name:      "RFC3339",
			updatedAt: "2026-02-07T06:00:00Z",
			wantAge:   6 * time.Hour,
		},
		{
			name:      "one day old",
			updatedAt: "2026-02-06T12:00:00Z",
			wantAge:   24 * time.Hour,
		},
		{
			name:      "invalid",
			updatedAt: "not-a-date",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{UpdatedAt: tc.updatedAt},
			}
			got, err := wispAge(w, now)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantAge {
				t.Errorf("wispAge = %v, want %v", got, tc.wantAge)
			}
		})
	}
}

func TestHasKeepLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", nil, false},
		{"other labels", []string{"bug", "urgent"}, false},
		{"keep label", []string{"keep"}, true},
		{"gt:keep label", []string{"bug", "gt:keep"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{Labels: tc.labels},
			}
			if got := hasKeepLabel(w); got != tc.want {
				t.Errorf("hasKeepLabel = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasComments(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  bool
	}{
		{"no comments", 0, false},
		{"has comments", 3, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{CommentCount: tc.count}
			if got := hasComments(w); got != tc.want {
				t.Errorf("hasComments = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsReferenced(t *testing.T) {
	tests := []struct {
		name    string
		depCnt  int
		deptCnt int
		want    bool
	}{
		{"no refs", 0, 0, false},
		{"has dependents", 0, 1, true},
		{"has dependencies", 1, 0, true},
		{"both", 2, 3, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &compactIssue{
				Issue: beads.Issue{
					DependencyCount: tc.depCnt,
					DependentCount:  tc.deptCnt,
				},
			}
			if got := isReferenced(w); got != tc.want {
				t.Errorf("isReferenced = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompactTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short ASCII", "short", 10, "short"},
		{"exact length", "exactly10!", 10, "exactly10!"},
		{"ASCII too long", "this is too long", 10, "this is..."},
		{"short maxLen", "ab", 3, "ab"},
		{"maxLen 3", "abcdef", 3, "abc"},
		// Multi-byte UTF-8: emoji is 1 rune, not 4 bytes
		{"emoji within limit", "🤝 HANDOFF", 10, "🤝 HANDOFF"},
		{"emoji truncated", "🤝 HANDOFF: Routine cycle for witness", 15, "🤝 HANDOFF: R..."},
		// CJK characters: each is 1 rune, 3 bytes
		{"CJK within limit", "日本語テスト", 10, "日本語テスト"},
		{"CJK truncated", "日本語テストデータ", 6, "日本語..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactTruncate(tc.s, tc.maxLen); got != tc.want {
				t.Errorf("compactTruncate(%q, %d) = %q, want %q", tc.s, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestExtractJSONArray(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			"clean JSON array",
			`[{"id":"test"}]`,
			`[{"id":"test"}]`,
		},
		{
			"warning prefix before JSON",
			"Warning: no route found for prefix \"gt-\"\n[{\"id\":\"test\"}]",
			`[{"id":"test"}]`,
		},
		{
			"unicode warning prefix",
			"⚠ Warning: something with 🤝 emoji\n[{\"id\":\"test\"}]",
			`[{"id":"test"}]`,
		},
		{
			"no array in data",
			"just some text without json",
			"just some text without json",
		},
		{
			"empty data",
			"",
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(extractJSONArray([]byte(tc.data)))
			if got != tc.want {
				t.Errorf("extractJSONArray(%q) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

func TestLoadTTLConfigDefaults(t *testing.T) {
	// With empty town root, should return defaults
	ttls := loadTTLConfig("", "")

	if ttls["heartbeat"] != 6*time.Hour {
		t.Errorf("heartbeat TTL = %v, want 6h", ttls["heartbeat"])
	}
	if ttls["patrol"] != 24*time.Hour {
		t.Errorf("patrol TTL = %v, want 24h", ttls["patrol"])
	}
	if ttls["error"] != 7*24*time.Hour {
		t.Errorf("error TTL = %v, want 168h", ttls["error"])
	}
}

func TestLoadTTLConfigWithRoleDefaults(t *testing.T) {
	// With empty town root, should return hardcoded defaults
	ttls := loadTTLConfigWithRole("", "")

	for k, want := range defaultTTLs {
		if got := ttls[k]; got != want {
			t.Errorf("loadTTLConfigWithRole TTLs[%q] = %v, want %v", k, got, want)
		}
	}
}

func TestLoadTTLConfigWithRoleSkipsInvalidPaths(t *testing.T) {
	// With nonexistent paths, rig bead lookup should gracefully skip
	ttls := loadTTLConfigWithRole("/nonexistent/town", "myrig")

	// Should still have defaults even though lookups failed
	if ttls["patrol"] != defaultTTLs["patrol"] {
		t.Errorf("patrol TTL = %v, want %v", ttls["patrol"], defaultTTLs["patrol"])
	}
	if ttls["error"] != defaultTTLs["error"] {
		t.Errorf("error TTL = %v, want %v", ttls["error"], defaultTTLs["error"])
	}
}

func TestCleanOrphanedWispDepsUsesTypedTargets(t *testing.T) {
	data, err := os.ReadFile("compact.go")
	if err != nil {
		t.Fatalf("read compact.go: %v", err)
	}
	body := compactSourceBetween(t, string(data), "func cleanOrphanedWispDeps(", "// listWisps")
	if strings.Contains(body, "depends_on_id") {
		t.Fatalf("cleanOrphanedWispDeps should not use legacy depends_on_id:\n%s", body)
	}
	for _, want := range []string{
		"depends_on_wisp_id IS NOT NULL AND NOT EXISTS",
		"wisps WHERE id = wisp_dependencies.depends_on_wisp_id",
		"depends_on_issue_id IS NOT NULL AND NOT EXISTS",
		"issues WHERE id = wisp_dependencies.depends_on_issue_id",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("cleanOrphanedWispDeps missing %q:\n%s", want, body)
		}
	}
}

func compactSourceBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start == -1 {
		t.Fatalf("could not find %q", startMarker)
	}
	end := strings.Index(source[start:], endMarker)
	if end == -1 {
		t.Fatalf("could not find %q after %q", endMarker, startMarker)
	}
	return source[start : start+end]
}

// setupExpiredWispStub puts a fake `bd` on PATH that reports one closed wisp
// far past its TTL — a live delete candidate — and logs every invocation.
// Returns the path to that log.
func setupExpiredWispStub(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	bdLog := filepath.Join(t.TempDir(), "bd-args.log")

	bdScript := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_ARGS_LOG"
case "$1" in
  list)
    printf '[{"id":"hq-wisp-expired","title":"stale heartbeat","status":"closed","issue_type":"chore","ephemeral":true,"wisp_type":"heartbeat","created_at":"2020-01-01T00:00:00Z","updated_at":"2020-01-01T00:00:00Z"}]\n'
    ;;
  delete)
    exit 0
    ;;
  *)
    echo "unexpected bd command: $*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", bdLog)
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	return bdLog
}

func readBdArgsLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	return string(data)
}

func TestPerformCompactionDryRunDoesNotDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	bdLog := setupExpiredWispStub(t)

	result, err := performCompaction(compactOptions{DryRun: true, Quiet: true})
	if err != nil {
		t.Fatalf("performCompaction: %v", err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "hq-wisp-expired" {
		t.Fatalf("Deleted = %#v, want the expired wisp reported as a would-delete", result.Deleted)
	}
	if args := readBdArgsLog(t, bdLog); strings.Contains(args, "delete") {
		t.Fatalf("dry run issued bd delete:\n%s", args)
	}
}

// Paired negative control for TestPerformCompactionDryRunDoesNotDelete: with
// DryRun off the same wisp IS deleted, so the test above is measuring the
// dry-run guard rather than an empty candidate set.
func TestPerformCompactionDeletesExpiredWispWhenNotDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	bdLog := setupExpiredWispStub(t)

	result, err := performCompaction(compactOptions{Quiet: true})
	if err != nil {
		t.Fatalf("performCompaction: %v", err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].ID != "hq-wisp-expired" {
		t.Fatalf("Deleted = %#v, want the expired wisp deleted", result.Deleted)
	}
	args := readBdArgsLog(t, bdLog)
	if !strings.Contains(args, "delete hq-wisp-expired --force") {
		t.Fatalf("bd delete --force was not issued:\n%s", args)
	}
}

// gt-12f/cl-kf00: deleteWisp must never remove a merge-request bead or a
// compliance-commented bead, independent of the TTL/promotion decision that
// routed a wisp to it — see the gt-qtt gap this closes: a molecule step
// (Parent != "") is never promoted, so before this fix a closed, past-TTL
// molecule step carrying a compliance comment reached deleteWisp anyway.

// erroringBD fails any invocation — used to prove a protected candidate is
// rejected before bd is ever called.
func erroringBD(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
echo "unexpected bd invocation: $*" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDeleteWispProtectsMergeRequestLabel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	erroringBD(t)
	bd := beads.New(t.TempDir())
	w := &compactIssue{Issue: beads.Issue{ID: "cl-wisp-mr", Title: "Merge: cl-abc", Labels: []string{"gt:merge-request"}}}
	result := &compactResult{}

	deleteWisp(bd, w, "TTL expired", result, compactAudit{}, compactOptions{Quiet: true})

	if len(result.Deleted) != 0 {
		t.Fatalf("Deleted = %#v, want nothing deleted — the merge-request label must protect it", result.Deleted)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestDeleteWispProtectsComplianceComment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  comments) echo '[{"author":"cloudcontentmanager/crew/compliance"}]' ;;
  delete) echo "deleteWisp must not delete a compliance-commented bead" >&2; exit 1 ;;
  *) echo "unexpected bd invocation: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	bd := beads.New(t.TempDir())
	// A molecule step (Parent set) with a comment is never promoted (see
	// performCompaction), so this is exactly the gt-qtt shape: it must still
	// never reach the actual delete.
	w := &compactIssue{Issue: beads.Issue{ID: "cl-wisp-step", Title: "mol step", Parent: "cl-wisp-root"}, CommentCount: 1}
	result := &compactResult{}

	deleteWisp(bd, w, "molecule step past TTL", result, compactAudit{}, compactOptions{Quiet: true})

	if len(result.Deleted) != 0 {
		t.Fatalf("Deleted = %#v, want nothing deleted — the compliance comment must protect it", result.Deleted)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestDeleteWispFailsClosedOnUnreadableComments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  comments) echo 'dolt: connection refused' >&2; exit 1 ;;
  delete) echo "deleteWisp must not delete when comments are unreadable" >&2; exit 1 ;;
  *) echo "unexpected bd invocation: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	bd := beads.New(t.TempDir())
	w := &compactIssue{Issue: beads.Issue{ID: "cl-wisp-unknown", Title: "mystery"}, CommentCount: 1}
	result := &compactResult{}

	deleteWisp(bd, w, "TTL expired", result, compactAudit{}, compactOptions{Quiet: true})

	if len(result.Deleted) != 0 {
		t.Fatalf("Deleted = %#v, want nothing deleted — an unreadable comment check must fail closed", result.Deleted)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestDeleteWispFailsClosedOnCommentCountMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  comments) echo '[]' ;;
  delete) echo "deleteWisp must not delete on comment count mismatch" >&2; exit 1 ;;
  *) echo "unexpected bd invocation: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	bd := beads.New(t.TempDir())
	w := &compactIssue{Issue: beads.Issue{ID: "cl-wisp-mismatch", Title: "mystery"}, CommentCount: 1}
	result := &compactResult{}

	deleteWisp(bd, w, "TTL expired", result, compactAudit{}, compactOptions{Quiet: true})
	if len(result.Deleted) != 0 || result.Skipped != 1 {
		t.Fatalf("Deleted=%#v Skipped=%d, want the mismatched candidate retained", result.Deleted, result.Skipped)
	}
}

func TestDeleteWispDeletesUnprotectedWisp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  comments) echo '[{"author":"gastown/polecats/toast"}]' ;;
  delete) exit 0 ;;
  *) echo "unexpected bd invocation: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
	bd := beads.New(t.TempDir())
	w := &compactIssue{Issue: beads.Issue{ID: "cl-wisp-plain", Title: "ordinary"}, CommentCount: 1}
	result := &compactResult{}

	deleteWisp(bd, w, "TTL expired", result, compactAudit{}, compactOptions{Quiet: true})

	if len(result.Deleted) != 1 || result.Deleted[0].ID != "cl-wisp-plain" {
		t.Fatalf("Deleted = %#v, want cl-wisp-plain deleted — a plain comment from a non-compliance author is not protected", result.Deleted)
	}
}
