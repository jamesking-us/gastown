package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/worktreewrite"
)

// The activity survey (cl-2sp).
//
// Before this command, every seat that needed to know whether a polecat was
// working ran its own `find <worktree> -type f -not -path '*/.git/*' -printf
// '%T@ %p\n' | sort -rn | head` by hand, and then read the number against a
// threshold it invented on the spot. That is how the same measurement produced
// opposite verdicts on the same polecat within an hour, and how a quiet reading
// twice came within one step of restarting a healthy agent.
//
// So this command exists to make the reading uniform AND to make the reading
// state its own limits every time it is printed, rather than relying on each
// reader to remember them. It prints the direction rule in the human output and
// carries it in the JSON. A survey is also the only form in which the question
// that dominates every per-agent signal can be asked at all — see coTiming.

var (
	polecatActivityJSON   bool
	polecatActivityAll    bool
	polecatActivityWindow time.Duration
)

var polecatActivityCmd = &cobra.Command{
	Use:   "activity [rig]",
	Short: "Survey polecat worktree writes (the activity signal)",
	Long: `Survey when each polecat last wrote a file in its worktree.

This is the town's activity signal. It replaces last_activity and tmux
session_activity, both of which report AGE — they are pinned to session
creation and never move, so they read identically for a busy polecat and a
dead one (cl-2sp).

THE DIRECTION RULE, which this command prints with every result because
ignoring it has twice nearly killed healthy work:

  RECENT WRITE  -> the polecat is working. Positive evidence, reliable.
  NO WRITE      -> NOTHING FOLLOWS. Not idle, not stalled, not dead.

A polecat reading source in the module cache, analysing test failures, or
blocked on a build lock writes nothing to its worktree for as long as that
lasts. All three are healthy and all three read here as silent. A quiet
result is grounds to LOOK — gt peek, and check that the token counter
advances between two readings — never grounds to restart, nudge or reap.

Examples:
  gt polecat activity greenplace
  gt polecat activity --all --json
  gt polecat activity greenplace --window 10m`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPolecatActivity,
}

// PolecatActivity is one polecat's entry in the survey.
type PolecatActivity struct {
	Rig  string `json:"rig"`
	Name string `json:"name"`

	// Working is true when this polecat's worktree was written inside the
	// window. FALSE DOES NOT MEAN NOT WORKING, and the field is named for what
	// it can prove rather than for the verdict a reader wants from it.
	Working bool `json:"working"`

	// SilenceProvesNothing is emitted as a constant true alongside every
	// survey entry. It is redundant to anyone who has read this command's
	// help, and it is here for the reader who has not: a JSON consumer that
	// filters on !working now has the disclaimer in the same object.
	SilenceProvesNothing bool `json:"silence_proves_nothing"`

	LastWrite *worktreewrite.Result `json:"last_write"`
}

// PolecatActivitySurvey is the whole reading, including the cross-seat question
// that no single entry can answer.
type PolecatActivitySurvey struct {
	MeasuredAt    time.Time         `json:"measured_at"`
	WindowSeconds int64             `json:"window_seconds"`
	Polecats      []PolecatActivity `json:"polecats"`
	CoTiming      []CoTimingCluster `json:"co_timing,omitempty"`

	// DirectionRule travels with the data so a pasted survey cannot be read
	// backwards by someone who never saw the help text.
	DirectionRule string `json:"direction_rule"`
}

// CoTimingCluster reports several polecats whose last writes land within a
// hair of each other.
//
// This is the one question that dominates every per-agent instrument on cl-2sp
// and that no per-agent instrument can answer. On 2026-09-03 four agents in
// different rigs stopped within 0.6 seconds of one another on a shared upstream
// API outage. Every per-seat signal read "stranded" on all four, and a watcher
// working seat-by-seat would have nudged four corpses and learned nothing. When
// several seats stop in the same second, the cause is shared — infrastructure,
// a lock, a filesystem — and the remedy is not per-agent.
//
// The cluster is a PROMPT TO ASK, never a diagnosis: co-timing is also what a
// convoy dispatched together and finishing together looks like.
type CoTimingCluster struct {
	Names       []string  `json:"names"`
	FirstWrite  time.Time `json:"first_write"`
	LastWrite   time.Time `json:"last_write"`
	SpreadMilli int64     `json:"spread_ms"`
	Note        string    `json:"note"`
}

// CoTimingWindow is how close two last-writes must fall to be worth flagging.
// Deliberately tight: the signature being looked for is several seats stopping
// in the SAME SECOND, which is a shared cause. A loose window would flag any
// convoy that happens to be working at a similar pace.
const CoTimingWindow = 2 * time.Second

// DirectionRuleText is the one sentence this whole bead reduces to.
const DirectionRuleText = "A recent write proves the polecat is working. Silence proves NOTHING: reading, thinking, and waiting on a child process all write nothing. Never restart, nudge or reap on silence alone."

func runPolecatActivity(cmd *cobra.Command, args []string) error {
	var rigs []*rig.Rig
	if polecatActivityAll {
		allRigs, err := getAllRigs()
		if err != nil {
			return err
		}
		rigs = allRigs
	} else {
		if len(args) < 1 {
			return fmt.Errorf("rig name required (or use --all)")
		}
		_, r, err := getPolecatManager(args[0])
		if err != nil {
			return err
		}
		rigs = []*rig.Rig{r}
	}

	window := polecatActivityWindow
	if window <= 0 {
		window = PolecatWorkingWindow
	}

	survey := PolecatActivitySurvey{
		MeasuredAt:    time.Now(),
		WindowSeconds: int64(window / time.Second),
		DirectionRule: DirectionRuleText,
	}

	for _, r := range rigs {
		mgr, _, err := getPolecatManager(r.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", r.Name, err)
			continue
		}
		names, err := listPolecatDirectoryNames(r.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list polecats in %s: %v\n", r.Name, err)
			continue
		}
		for _, name := range names {
			res := worktreewrite.Scan(mgr.ClonePath(name), worktreewrite.Options{})
			survey.Polecats = append(survey.Polecats, PolecatActivity{
				Rig:                  r.Name,
				Name:                 name,
				Working:              res.ProvesRecentWork(window),
				SilenceProvesNothing: true,
				LastWrite:            &res,
			})
		}
	}

	survey.CoTiming = detectCoTiming(survey.Polecats, CoTimingWindow)
	sortActivityQuietestFirst(survey.Polecats)

	if polecatActivityJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(survey)
	}

	return printActivitySurvey(survey, window)
}

// sortActivityQuietestFirst orders the survey with the least recently written
// worktrees at the top, because those are the entries a reader is surveying
// for. Unmeasurable and never-written entries sort above everything: "we could
// not look" must be more visible than a large number, not less.
func sortActivityQuietestFirst(items []PolecatActivity) {
	rank := func(a PolecatActivity) int {
		switch {
		case a.LastWrite == nil || a.LastWrite.Err != nil:
			return 0
		case !a.LastWrite.Found:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := rank(items[i]), rank(items[j])
		if ri != rj {
			return ri < rj
		}
		if ri == 2 && items[i].LastWrite.Age != items[j].LastWrite.Age {
			return items[i].LastWrite.Age > items[j].LastWrite.Age
		}
		if items[i].Rig != items[j].Rig {
			return items[i].Rig < items[j].Rig
		}
		return items[i].Name < items[j].Name
	})
}

// detectCoTiming groups polecats whose last writes fall within window of each
// other. Only groups of two or more are returned, and only among polecats that
// actually have a measured write — an absent measurement has no timestamp to
// cluster on, and inventing one would manufacture the very correlation this is
// meant to detect.
func detectCoTiming(items []PolecatActivity, window time.Duration) []CoTimingCluster {
	type stamped struct {
		name string
		at   time.Time
	}
	var stamps []stamped
	for _, it := range items {
		if it.LastWrite == nil || it.LastWrite.Err != nil || !it.LastWrite.Found {
			continue
		}
		label := it.Name
		if it.Rig != "" {
			label = it.Rig + "/" + it.Name
		}
		stamps = append(stamps, stamped{name: label, at: it.LastWrite.LastWrite})
	}
	if len(stamps) < 2 {
		return nil
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].at.Before(stamps[j].at) })

	var clusters []CoTimingCluster
	i := 0
	for i < len(stamps) {
		j := i + 1
		// Chain forward while each next stamp is within window of the cluster's
		// FIRST member, not merely of its predecessor. Chaining off the
		// predecessor would let a long steady drip of writes accumulate into
		// one spurious "everything stopped at once" cluster.
		for j < len(stamps) && stamps[j].at.Sub(stamps[i].at) <= window {
			j++
		}
		if j-i >= 2 {
			names := make([]string, 0, j-i)
			for _, s := range stamps[i:j] {
				names = append(names, s.name)
			}
			sort.Strings(names)
			spread := stamps[j-1].at.Sub(stamps[i].at)
			clusters = append(clusters, CoTimingCluster{
				Names:       names,
				FirstWrite:  stamps[i].at,
				LastWrite:   stamps[j-1].at,
				SpreadMilli: spread.Milliseconds(),
				Note:        "last writes within " + worktreewrite.FormatAge(window) + " of each other — if these seats are quiet, ask what they SHARE (an outage, a build lock, a filesystem) before treating them as separate stalls",
			})
		}
		i = j
	}
	return clusters
}

func printActivitySurvey(survey PolecatActivitySurvey, window time.Duration) error {
	if len(survey.Polecats) == 0 {
		fmt.Println("No polecats found.")
		return nil
	}

	fmt.Printf("%s\n", style.Bold.Render("Polecat worktree writes"))
	fmt.Printf("%s\n\n", style.Dim.Render(fmt.Sprintf("measured %s · working window %s · quietest first",
		survey.MeasuredAt.Format("15:04:05"), worktreewrite.FormatAge(window))))

	for _, p := range survey.Polecats {
		ww := p.LastWrite
		switch {
		case ww == nil || ww.Err != nil:
			reason := "unknown"
			if ww != nil {
				reason = ww.ErrText
			}
			fmt.Printf("  %s %s/%s  %s\n", style.Warning.Render("?"), p.Rig, p.Name,
				style.Warning.Render("not measured: "+reason))
		case !ww.Found:
			fmt.Printf("  %s %s/%s  %s\n", style.Dim.Render("·"), p.Rig, p.Name,
				style.Dim.Render("no writes found"))
		case p.Working:
			fmt.Printf("  %s %s/%s  %s  %s\n", style.Success.Render("●"), p.Rig, p.Name,
				style.Success.Render(worktreewrite.FormatAge(ww.Age)+" ago"),
				style.Dim.Render(ww.LastWritePath))
		default:
			fmt.Printf("  %s %s/%s  %s  %s\n", style.Dim.Render("·"), p.Rig, p.Name,
				worktreewrite.FormatAge(ww.Age)+" ago",
				style.Dim.Render(ww.LastWritePath))
		}
	}

	if len(survey.CoTiming) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Co-timing"))
		for _, c := range survey.CoTiming {
			fmt.Printf("  %s last wrote within %dms of each other\n",
				style.Warning.Render(fmt.Sprintf("%v", c.Names)), c.SpreadMilli)
			fmt.Printf("    %s\n", style.Dim.Render("if these are quiet, ask what they SHARE before calling them separate stalls"))
		}
	}

	fmt.Printf("\n%s\n", style.Dim.Render(DirectionRuleText))
	fmt.Printf("%s\n", style.Dim.Render("A quiet worktree is grounds to LOOK (gt peek, twice, watching the token counter), never to act."))
	return nil
}

func init() {
	polecatActivityCmd.Flags().BoolVar(&polecatActivityJSON, "json", false, "Output as JSON")
	polecatActivityCmd.Flags().BoolVar(&polecatActivityAll, "all", false, "Survey polecats in all rigs")
	polecatActivityCmd.Flags().DurationVar(&polecatActivityWindow, "window", PolecatWorkingWindow,
		"How recent a write counts as positive evidence of work")
	polecatCmd.AddCommand(polecatActivityCmd)
}
