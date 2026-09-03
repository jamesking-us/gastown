package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/worktreewrite"
)

func at(base time.Time, offset time.Duration) *worktreewrite.Result {
	return &worktreewrite.Result{Found: true, LastWrite: base.Add(offset)}
}

// TestDetectCoTiming_FindsSharedStop covers the question no per-agent instrument
// can answer. Four agents stopped within 0.6s of each other on 2026-09-03 on a
// shared upstream outage; every per-seat signal read "stranded" on all four, and
// a watcher working seat-by-seat would have nudged four corpses.
func TestDetectCoTiming_FindsSharedStop(t *testing.T) {
	base := time.Date(2026, 9, 3, 6, 8, 54, 0, time.UTC)
	items := []PolecatActivity{
		{Rig: "r", Name: "deacon", LastWrite: at(base, 26*time.Millisecond)},
		{Rig: "r", Name: "fury", LastWrite: at(base, 542*time.Millisecond)},
		{Rig: "r", Name: "chrome", LastWrite: at(base, 543*time.Millisecond)},
	}

	clusters := detectCoTiming(items, CoTimingWindow)
	if len(clusters) != 1 {
		t.Fatalf("len(clusters) = %d, want 1", len(clusters))
	}
	if len(clusters[0].Names) != 3 {
		t.Errorf("Names = %v, want all three seats", clusters[0].Names)
	}
	if got, want := strings.Join(clusters[0].Names, ","), "r/chrome,r/deacon,r/fury"; got != want {
		t.Errorf("Names = %q, want %q (sorted, rig-qualified)", got, want)
	}
	if clusters[0].SpreadMilli != 517 {
		t.Errorf("SpreadMilli = %d, want 517", clusters[0].SpreadMilli)
	}
	if !strings.Contains(clusters[0].Note, "SHARE") {
		t.Errorf("Note = %q, want it to point at a shared cause", clusters[0].Note)
	}
}

// TestDetectCoTiming_IgnoresUnrelatedWork keeps the flag rare enough to mean
// something. Polecats writing at their own pace are the normal case.
func TestDetectCoTiming_IgnoresUnrelatedWork(t *testing.T) {
	base := time.Now()
	items := []PolecatActivity{
		{Name: "a", LastWrite: at(base, -30*time.Second)},
		{Name: "b", LastWrite: at(base, -5*time.Minute)},
		{Name: "c", LastWrite: at(base, -22*time.Minute)},
	}
	if got := detectCoTiming(items, CoTimingWindow); got != nil {
		t.Errorf("detectCoTiming = %v, want nil for independently-paced writes", got)
	}
}

// TestDetectCoTiming_DoesNotChainOffPredecessor guards a manufactured cluster: a
// steady drip of writes one second apart is not "everything stopped at once",
// and chaining each stamp off its predecessor would report it as such.
func TestDetectCoTiming_DoesNotChainOffPredecessor(t *testing.T) {
	base := time.Now()
	var items []PolecatActivity
	for i := 0; i < 10; i++ {
		items = append(items, PolecatActivity{
			Name:      string(rune('a' + i)),
			LastWrite: at(base, time.Duration(i)*1500*time.Millisecond),
		})
	}
	for _, c := range detectCoTiming(items, CoTimingWindow) {
		if len(c.Names) > 2 {
			t.Errorf("cluster %v spans %dms — a drip was chained into one stop",
				c.Names, c.SpreadMilli)
		}
	}
}

// TestDetectCoTiming_SkipsUnmeasured refuses to invent a timestamp for a
// worktree that could not be read. Substituting the scan time would correlate
// every unmeasurable seat with every other one — manufacturing exactly the
// signal this is meant to detect.
func TestDetectCoTiming_SkipsUnmeasured(t *testing.T) {
	items := []PolecatActivity{
		{Name: "a", LastWrite: &worktreewrite.Result{Err: errors.New("gone"), ErrText: "gone"}},
		{Name: "b", LastWrite: &worktreewrite.Result{Found: false}},
		{Name: "c", LastWrite: nil},
	}
	if got := detectCoTiming(items, CoTimingWindow); got != nil {
		t.Errorf("detectCoTiming = %v, want nil when nothing was measured", got)
	}
}

// TestSortActivityQuietestFirst puts unmeasurable entries above everything:
// "we could not look" must be more visible than a large number, not less.
func TestSortActivityQuietestFirst(t *testing.T) {
	items := []PolecatActivity{
		{Name: "busy", LastWrite: &worktreewrite.Result{Found: true, Age: 5 * time.Second}},
		{Name: "quiet", LastWrite: &worktreewrite.Result{Found: true, Age: 40 * time.Minute}},
		{Name: "empty", LastWrite: &worktreewrite.Result{Found: false}},
		{Name: "unmeasured", LastWrite: &worktreewrite.Result{Err: errors.New("x"), ErrText: "x"}},
	}
	sortActivityQuietestFirst(items)

	want := []string{"unmeasured", "empty", "quiet", "busy"}
	for i, w := range want {
		if items[i].Name != w {
			t.Fatalf("order = %s, want %s at position %d",
				names(items), w, i)
		}
	}
}

func names(items []PolecatActivity) string {
	var out []string
	for _, i := range items {
		out = append(out, i.Name)
	}
	return strings.Join(out, ",")
}

// TestSurveyJSONCarriesTheDirectionRule is a wording test on purpose. The rule
// is the safeguard, and a survey pasted into a report or parsed by another
// agent must carry it rather than assume the reader has seen the help text.
func TestSurveyJSONCarriesTheDirectionRule(t *testing.T) {
	survey := PolecatActivitySurvey{
		DirectionRule: DirectionRuleText,
		Polecats: []PolecatActivity{{
			Name:                 "quiet",
			Working:              false,
			SilenceProvesNothing: true,
			LastWrite:            &worktreewrite.Result{Found: true, Age: time.Hour},
		}},
	}
	blob, err := json.Marshal(survey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(blob)
	for _, want := range []string{"silence_proves_nothing", "direction_rule", "Silence proves NOTHING"} {
		if !strings.Contains(got, want) {
			t.Errorf("survey JSON missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"last_activity"`) {
		t.Error("survey JSON reintroduces the last_activity key that cl-2sp is about")
	}
}
