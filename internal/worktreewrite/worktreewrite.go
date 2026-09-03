// Package worktreewrite reports when a file was last written under a directory.
//
// THAT SENTENCE IS THE WHOLE CONTRACT, AND THE PACKAGE IS NAMED AFTER IT ON
// PURPOSE. This package does not report whether an agent is alive, working,
// idle, stalled or dead. It reports file writes in one directory. Callers that
// want a liveness verdict must combine this with something else; nothing here
// will give them one.
//
// # Why the contract is spelled out this way (cl-2sp)
//
// Every status-surface instrument that lied on cl-2sp lied the same way: it was
// confidently right about the wrong object. tmux session_activity is right
// about a session — which outlives the agent process inside it, so it reads as
// hours idle thirty seconds after a restart. last_activity is right about a
// record that was written once at creation. A pane footer is right about what
// is on the pane. None was broken; each answered a question adjacent to the one
// being asked, and the reader supplied the confusion.
//
// So this package carries its referent in its name, in its type names, and in
// every JSON key it produces. A reader who has "LastWrite in /path/to/worktree"
// in front of them cannot mistake it for "the agent is alive" as easily as they
// can mistake a field called "last_activity".
//
// # The direction rule, which is load-bearing
//
// Worktree writes are a SOUND POSITIVE and an UNSOUND NEGATIVE:
//
//	writes present  =>  work is happening. Reliable.
//	writes absent   =>  NOTHING. Not idle, not stalled, not dead.
//
// The absence half is not a theoretical caveat, it is measured. On 2026-09-01 a
// healthy polecat (deathclaw) read 1074s and then 6061s quiet while 18 minutes
// into a turn reading dependency source in ~/go/pkg/mod — work performed
// outside the worktree is invisible here. On 2026-09-03 a healthy polecat
// (chrome) read 1h23m quiet while mid-analysis of four failing test packages:
// reading and thinking write nothing. A third healthy shape, blocked on a live
// child process under flock, writes nothing either and does so for as long as
// the build queue is deep. Those are the states a watcher is most tempted to
// interrupt, and an mtime-only rule reports every one of them as dead.
//
// This is why the API below has [Result.ProvesRecentWork] and deliberately has
// no IsIdle, IsStalled or ProvesDead. The missing methods are the design. An
// instrument that can fail silent must never be read as a negative, and the
// cheapest way to enforce that is to make the negative unspeakable.
package worktreewrite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultExcludedDirs are directory names skipped during a scan, at any depth.
//
// Every entry is here because writes inside it are NOT attributable to the
// agent whose worktree this is. Each was measured, not assumed:
//
//   - .git receives writes from any push, fetch, or checkout performed by
//     another seat against the shared repository. Counting those would report a
//     refinery push as the polecat's activity.
//   - .beads receives writes from bd sync and from the beads daemon, which run
//     on their own schedule in every worktree in town.
//   - .runtime holds agent.lock and session_id, written by gt when a SESSION
//     STARTS and not touched again until it ends (internal/lock writes on
//     Acquire, deletes on Release; there is no refresh). Surveying a live rig
//     on 2026-09-03 found .runtime/agent.lock or .runtime/session_id to be the
//     newest file in four polecat worktrees, at ages matching their session
//     starts. Counting it would make a polecat that WEDGED AT STARTUP — input
//     queued, no turn ever begun — read as working for the whole window
//     immediately after spawn, which is exactly when that failure happens and
//     exactly when a watcher is looking for it.
//
// Excluding these costs real signal — a polecat that has just committed and is
// pushing shows nothing here — and that cost is accepted, because this
// instrument is only ever read as a positive and a false positive is the one
// error it cannot afford.
//
// Build outputs are deliberately NOT excluded. obj/, bin/, node_modules and
// friends are where a compiling agent's work actually lands, and on the rig
// surveyed above they were the newest file for most polecats. Excluding them
// as "noise" would blind the instrument to the commonest kind of work in town.
var DefaultExcludedDirs = []string{".git", ".beads", ".runtime"}

// DefaultMaxFiles bounds a scan. Status commands run this synchronously and a
// worktree with a populated node_modules or obj tree can hold a very large
// number of files. On hitting the bound the scan stops and reports
// Truncated — see the note on [Result.Truncated] for why that is safe here and
// would not be safe for a negative-reading instrument.
const DefaultMaxFiles = 200000

// DefaultMaxDuration bounds a scan by wall clock, for the case where the file
// count is modest but the filesystem is slow (a cold cache, a network mount).
const DefaultMaxDuration = 3 * time.Second

// Options configures a scan. The zero value is valid and uses the defaults.
type Options struct {
	// ExcludedDirs overrides DefaultExcludedDirs when non-nil. An explicitly
	// empty (non-nil, zero-length) slice excludes nothing.
	ExcludedDirs []string

	// MaxFiles overrides DefaultMaxFiles when > 0.
	MaxFiles int

	// MaxDuration overrides DefaultMaxDuration when > 0.
	MaxDuration time.Duration

	// Now overrides the clock, for tests.
	Now func() time.Time
}

func (o Options) excludedDirs() []string {
	if o.ExcludedDirs != nil {
		return o.ExcludedDirs
	}
	return DefaultExcludedDirs
}

func (o Options) maxFiles() int {
	if o.MaxFiles > 0 {
		return o.MaxFiles
	}
	return DefaultMaxFiles
}

func (o Options) maxDuration() time.Duration {
	if o.MaxDuration > 0 {
		return o.MaxDuration
	}
	return DefaultMaxDuration
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Result is one measurement of one directory. It is shaped to be quotable:
// everything a later reader needs in order to know WHAT WAS MEASURED travels
// with the answer, so a result pasted into a report still states its own scope.
type Result struct {
	// Root is the directory that was scanned. The referent of every other
	// field in this struct is "under Root", never "this agent".
	Root string `json:"root"`

	// Found reports whether any qualifying file was seen. False means the scan
	// found nothing to measure — an empty worktree, an unreadable one, or a
	// tree consisting only of excluded directories. It does NOT mean idle.
	Found bool `json:"found"`

	// LastWrite is the modification time of the most recently modified file
	// under Root, zero when !Found.
	LastWrite time.Time `json:"last_write,omitempty"`

	// LastWritePath is that file's path relative to Root. Naming the file is
	// most of this instrument's value: "obj/Debug/build.log 4s ago" is
	// evidence a reader can act on, where a bare timestamp is not.
	LastWritePath string `json:"last_write_path,omitempty"`

	// Age is the time between LastWrite and the moment of measurement, clamped
	// at zero for a file whose mtime is in the future (clock skew, a checkout
	// with preserved timestamps).
	Age time.Duration `json:"-"`

	// AgeSeconds is Age in whole seconds, for JSON consumers.
	AgeSeconds int64 `json:"age_seconds,omitempty"`

	// MeasuredAt is when the scan finished. Two Results from different seats
	// are only comparable through this field, and comparing them is how a
	// reader asks the question no per-agent instrument can answer: did
	// several seats go quiet in the same second, i.e. is this one stall or one
	// outage? (Four agents died within 0.6s of each other on 2026-09-03;
	// every per-agent instrument read "stranded" on all four.)
	MeasuredAt time.Time `json:"measured_at"`

	// FilesScanned is how many files were examined.
	FilesScanned int `json:"files_scanned"`

	// ExcludedDirs records which directory names were skipped, so a reading
	// describes its own blind spots rather than relying on the reader to
	// remember them.
	ExcludedDirs []string `json:"excluded_dirs,omitempty"`

	// Truncated reports that the scan hit MaxFiles or MaxDuration and stopped
	// early. A truncated scan may have missed a newer file, so its LastWrite
	// is a LOWER BOUND on recency.
	//
	// That is harmless for the only reading this package supports: a positive
	// found under truncation is still a real write, and a truncated scan that
	// found nothing recent proves nothing — exactly as an untruncated one that
	// found nothing recent proves nothing.
	Truncated bool `json:"truncated,omitempty"`

	// Err records a scan that could not be completed (Root missing, permission
	// denied at the top). Individual unreadable entries deeper in the tree are
	// skipped without setting this.
	Err error `json:"-"`

	// ErrText is Err rendered for JSON consumers.
	ErrText string `json:"error,omitempty"`
}

// ProvesRecentWork reports whether this measurement is positive evidence that
// work happened under Root within the given window.
//
// This is the ONLY verdict this package offers, and its negative is not the
// complement of its positive: !ProvesRecentWork means "this instrument did not
// see work", never "no work happened". A caller that treats a false return as
// grounds to restart, nudge, or reap an agent has reintroduced the exact defect
// cl-2sp exists to prevent.
func (r Result) ProvesRecentWork(window time.Duration) bool {
	return r.Found && r.Err == nil && r.Age <= window
}

// Describe renders the measurement as a phrase that states its own referent,
// suitable for dropping straight into CLI output or a patrol report.
func (r Result) Describe() string {
	switch {
	case r.Err != nil:
		return "worktree writes not measured (" + r.ErrText + ")"
	case !r.Found:
		return "no writes found under " + r.Root + " (not evidence of idleness)"
	default:
		return "last write " + FormatAge(r.Age) + " ago: " + r.LastWritePath
	}
}

// Scan measures the most recent file write under root.
//
// Directories are not counted: a directory's mtime changes when an entry is
// added or removed, which makes a deletion elsewhere in the tree look like
// fresh output. Symbolic links are not followed and their targets are not
// counted, so a link into a shared cache cannot import another process's
// writes into this worktree's reading.
//
// A missing or unreadable root is reported through Result.Err, not returned as
// an error, because every caller of this function is a status surface: it must
// render "could not measure" distinctly from "measured, nothing found", and
// the two must never collapse into a single silent zero.
func Scan(root string, opts Options) Result {
	excluded := opts.excludedDirs()
	maxFiles := opts.maxFiles()
	maxDuration := opts.maxDuration()
	start := opts.now()

	res := Result{
		Root:         root,
		ExcludedDirs: excluded,
	}

	excludedSet := make(map[string]struct{}, len(excluded))
	for _, d := range excluded {
		excludedSet[d] = struct{}{}
	}

	info, err := os.Stat(root)
	if err != nil {
		res.Err = err
		res.ErrText = err.Error()
		res.MeasuredAt = opts.now()
		return res
	}
	if !info.IsDir() {
		res.Err = errors.New("not a directory: " + root)
		res.ErrText = res.Err.Error()
		res.MeasuredAt = opts.now()
		return res
	}

	deadline := start.Add(maxDuration)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is skipped rather than failing the scan.
			// Losing part of the tree can only cost a positive; it cannot
			// manufacture one, and this instrument is only read as a positive.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if path != root {
				if _, skip := excludedSet[d.Name()]; skip {
					return fs.SkipDir
				}
			}
			return nil
		}

		// Regular files only. Symlinks, sockets, fifos and devices are skipped:
		// a symlink's own mtime records when the link was made, and following
		// it would attribute writes performed outside this worktree to it.
		if !d.Type().IsRegular() {
			return nil
		}

		res.FilesScanned++
		if res.FilesScanned > maxFiles {
			res.Truncated = true
			return filepath.SkipAll
		}
		// The clock is consulted once per 1024 files rather than per file: the
		// bound exists to stop a pathological tree, and checking it is not
		// worth a syscall per entry on a healthy one.
		if res.FilesScanned%1024 == 0 && opts.now().After(deadline) {
			res.Truncated = true
			return filepath.SkipAll
		}

		fi, err := d.Info()
		if err != nil {
			// Raced with a delete, or unreadable. Same reasoning as above.
			return nil
		}

		mt := fi.ModTime()
		if !res.Found || mt.After(res.LastWrite) {
			res.Found = true
			res.LastWrite = mt
			if rel, relErr := filepath.Rel(root, path); relErr == nil {
				res.LastWritePath = rel
			} else {
				res.LastWritePath = path
			}
		}
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		res.Err = walkErr
		res.ErrText = walkErr.Error()
	}

	res.MeasuredAt = opts.now()
	if res.Found {
		res.Age = res.MeasuredAt.Sub(res.LastWrite)
		if res.Age < 0 {
			res.Age = 0
		}
		res.AgeSeconds = int64(res.Age / time.Second)
	}
	return res
}

// ScanAll measures several directories and returns the results keyed by the
// name the caller passed, sorted by name.
//
// This exists because the single most useful question about a set of quiet
// agents is not answerable one agent at a time. When several seats' last writes
// cluster in the same second, that is infrastructure — a shared API outage, a
// build lock, a filesystem stall — and not several independent stalls. A
// per-agent call site can never see it; a survey can. Callers rendering these
// should keep MeasuredAt visible for exactly that reason.
func ScanAll(roots map[string]string, opts Options) []NamedResult {
	out := make([]NamedResult, 0, len(roots))
	for name, root := range roots {
		out = append(out, NamedResult{Name: name, Result: Scan(root, opts)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NamedResult pairs a scan with the caller's label for the directory.
type NamedResult struct {
	Name string `json:"name"`
	Result
}

// FormatAge renders a duration the way the town's status surfaces read ages:
// seconds under a minute, then minutes, then hours and minutes.
func FormatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return itoa(h) + "h" + itoa(m) + "m"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
