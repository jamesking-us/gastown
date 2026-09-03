package worktreewrite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// touch writes a file with the given mtime, creating parent directories.
func touch(t *testing.T, root, rel string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	return path
}

// TestScan_PositiveControl is the acceptance test cl-2sp asks every remedy on
// that bead to carry: take a subject whose state is independently known — here
// a directory this test just wrote to — and confirm the instrument reports it.
// Two earlier proposed remedies on that bead failed precisely this check.
func TestScan_PositiveControl(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "src/main.go", time.Time{}) // now

	res := Scan(root, Options{})
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil", res.Err)
	}
	if !res.Found {
		t.Fatal("Found = false for a directory written to moments ago")
	}
	if res.LastWritePath != filepath.Join("src", "main.go") {
		t.Errorf("LastWritePath = %q, want src/main.go", res.LastWritePath)
	}
	if !res.ProvesRecentWork(time.Minute) {
		t.Errorf("ProvesRecentWork(1m) = false for a write age %v", res.Age)
	}
	if res.Root != root {
		t.Errorf("Root = %q, want %q — a result must state what it measured", res.Root, root)
	}
	if res.MeasuredAt.IsZero() {
		t.Error("MeasuredAt is zero; two results are only comparable through it")
	}
}

func TestScan_NewestFileWins(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	touch(t, root, "old.txt", now.Add(-2*time.Hour))
	touch(t, root, "deep/nested/new.txt", now.Add(-30*time.Second))
	touch(t, root, "middle.txt", now.Add(-10*time.Minute))

	res := Scan(root, Options{})
	if got, want := res.LastWritePath, filepath.Join("deep", "nested", "new.txt"); got != want {
		t.Errorf("LastWritePath = %q, want %q", got, want)
	}
	if res.Age > 2*time.Minute {
		t.Errorf("Age = %v, want ~30s", res.Age)
	}
	if res.FilesScanned != 3 {
		t.Errorf("FilesScanned = %d, want 3", res.FilesScanned)
	}
}

// TestScan_ExcludesUnattributableDirs pins the exclusion that keeps this
// instrument's positives honest: a push by another seat writes .git, and the
// beads daemon writes .beads, in every worktree in town. Counting either would
// report another process's work as this worktree's.
func TestScan_ExcludesUnattributableDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	touch(t, root, "src/old.go", now.Add(-time.Hour))
	touch(t, root, ".git/refs/heads/main", now)
	touch(t, root, ".beads/issues.jsonl", now)
	touch(t, root, ".runtime/agent.lock", now)
	touch(t, root, "vendor/.git/objects/ab/cd", now) // nested, must also be skipped

	res := Scan(root, Options{})
	if !res.Found {
		t.Fatal("Found = false, want the non-excluded file")
	}
	if got, want := res.LastWritePath, filepath.Join("src", "old.go"); got != want {
		t.Errorf("LastWritePath = %q, want %q (excluded dirs leaked into the reading)", got, want)
	}
	if res.ProvesRecentWork(time.Minute) {
		t.Error("ProvesRecentWork(1m) = true; a .git/.beads/.runtime write was counted as agent work")
	}
	if len(res.ExcludedDirs) == 0 {
		t.Error("ExcludedDirs empty; a reading must describe its own blind spots")
	}
}

func TestScan_ExplicitEmptyExclusionsExcludeNothing(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".git/refs/heads/main", time.Now())

	res := Scan(root, Options{ExcludedDirs: []string{}})
	if !res.Found {
		t.Fatal("Found = false; an explicitly empty exclusion list must exclude nothing")
	}
}

// TestScan_IgnoresDirectoryMtimes guards a false positive: a directory's mtime
// moves when an entry is added OR REMOVED, so counting directories would report
// a deletion as fresh output.
func TestScan_IgnoresDirectoryMtimes(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-time.Hour)
	touch(t, root, "sub/file.txt", old)
	if err := os.Chtimes(filepath.Join(root, "sub"), time.Now(), time.Now()); err != nil {
		t.Fatalf("chtimes dir: %v", err)
	}

	res := Scan(root, Options{})
	if res.ProvesRecentWork(time.Minute) {
		t.Errorf("ProvesRecentWork(1m) = true from a directory mtime (age %v)", res.Age)
	}
}

// TestScan_DoesNotFollowSymlinks guards the other false positive: a link into a
// shared cache would import writes made outside this worktree.
func TestScan_DoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	touch(t, root, "src/old.go", time.Now().Add(-time.Hour))
	touch(t, other, "hot.txt", time.Now())

	if err := os.Symlink(other, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	res := Scan(root, Options{})
	if res.ProvesRecentWork(time.Minute) {
		t.Errorf("ProvesRecentWork(1m) = true via symlink target (path %q)", res.LastWritePath)
	}
}

// TestScan_MissingRootIsNotSilentlyQuiet keeps "could not measure" distinct
// from "measured, nothing found". Collapsing them is how an absence of evidence
// becomes evidence of absence.
func TestScan_MissingRootIsNotSilentlyQuiet(t *testing.T) {
	res := Scan(filepath.Join(t.TempDir(), "does-not-exist"), Options{})
	if res.Err == nil {
		t.Fatal("Err = nil for a missing root")
	}
	if res.ErrText == "" {
		t.Error("ErrText empty; JSON consumers would see an unexplained blank")
	}
	if res.Found {
		t.Error("Found = true for a missing root")
	}
	if res.ProvesRecentWork(time.Hour) {
		t.Error("ProvesRecentWork = true for a scan that failed")
	}
	if !strings.Contains(res.Describe(), "not measured") {
		t.Errorf("Describe() = %q, want it to say the measurement failed", res.Describe())
	}
}

func TestScan_EmptyDirFoundFalse(t *testing.T) {
	res := Scan(t.TempDir(), Options{})
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil for a readable empty dir", res.Err)
	}
	if res.Found {
		t.Error("Found = true for an empty directory")
	}
}

// TestDescribe_NoWritesCarriesTheDirectionRule pins the wording, because the
// wording is the safeguard. Two independent watchers on cl-2sp read a quiet
// mtime as a stalled agent; both were wrong, and both were reading a surface
// that stated an age without stating what an age does not prove.
func TestDescribe_NoWritesCarriesTheDirectionRule(t *testing.T) {
	got := Result{Root: "/w", Found: false}.Describe()
	if !strings.Contains(got, "not evidence of idleness") {
		t.Errorf("Describe() = %q, want it to disclaim the negative reading", got)
	}
}

// TestProvesRecentWork_WindowBoundary fixes the window semantics as inclusive,
// so a caller and this package cannot disagree by one tick about a threshold.
func TestProvesRecentWork_WindowBoundary(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		win  time.Duration
		want bool
	}{
		{"inside", Result{Found: true, Age: 30 * time.Second}, time.Minute, true},
		{"at boundary", Result{Found: true, Age: time.Minute}, time.Minute, true},
		{"outside", Result{Found: true, Age: 90 * time.Second}, time.Minute, false},
		{"not found", Result{Found: false}, time.Hour, false},
		{"errored", Result{Found: true, Age: 0, Err: os.ErrPermission}, time.Hour, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.ProvesRecentWork(tt.win); got != tt.want {
				t.Errorf("ProvesRecentWork(%v) = %v, want %v", tt.win, got, tt.want)
			}
		})
	}
}

// TestScan_FutureMtimeClampsToZero covers a checkout that preserves timestamps
// or a skewed clock. A negative age must never render as a huge positive one.
func TestScan_FutureMtimeClampsToZero(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "f.txt", time.Now().Add(time.Hour))

	res := Scan(root, Options{})
	if res.Age < 0 {
		t.Errorf("Age = %v, want >= 0", res.Age)
	}
	if res.AgeSeconds < 0 {
		t.Errorf("AgeSeconds = %d, want >= 0", res.AgeSeconds)
	}
}

func TestScan_TruncationIsReported(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		touch(t, root, "f"+itoa(i)+".txt", time.Now())
	}

	res := Scan(root, Options{MaxFiles: 3})
	if !res.Truncated {
		t.Error("Truncated = false after scanning 10 files with MaxFiles=3")
	}
}

// TestScanAll_SortedAndComparable covers the survey form. Co-timing — several
// seats going quiet in the same second, which means infrastructure rather than
// several independent stalls — is only visible across results, never within one.
func TestScanAll_SortedAndComparable(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	touch(t, a, "x.txt", time.Now())
	touch(t, b, "y.txt", time.Now())

	got := ScanAll(map[string]string{"zeta": b, "alpha": a}, Options{})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("order = %q,%q, want alpha,zeta", got[0].Name, got[1].Name)
	}
	for _, r := range got {
		if r.MeasuredAt.IsZero() {
			t.Errorf("%s: MeasuredAt is zero, results are not comparable", r.Name)
		}
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{59 * time.Minute, "59m"},
		{time.Hour + 23*time.Minute, "1h23m"},
		{-5 * time.Second, "0s"},
	}
	for _, tt := range tests {
		if got := FormatAge(tt.d); got != tt.want {
			t.Errorf("FormatAge(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// TestScan_SessionStartupWritesDoNotCountAsWork is the startup-wedge case. gt
// writes .runtime/agent.lock and .runtime/session_id when a session starts and
// does not touch them again. A polecat that wedged at startup — messages queued
// behind a turn that never began — has exactly those two files and nothing
// else, and it has them SECONDS old. If they counted, the instrument would
// certify a wedged agent as working during the only window in which anyone is
// looking for that failure.
func TestScan_SessionStartupWritesDoNotCountAsWork(t *testing.T) {
	root := t.TempDir()
	touch(t, root, ".runtime/agent.lock", time.Now())
	touch(t, root, ".runtime/session_id", time.Now())

	res := Scan(root, Options{})
	if res.ProvesRecentWork(5 * time.Minute) {
		t.Errorf("ProvesRecentWork = true for a worktree holding only session-startup files (%q)",
			res.LastWritePath)
	}
	if res.Found {
		t.Errorf("Found = true, newest %q; nothing outside .runtime was written", res.LastWritePath)
	}
}
