package formula

import (
	"strings"
	"testing"
)

// TestPatrolFormulasHaveBackoffLogic verifies that patrol formulas include
// await-signal backoff logic in their loop-or-exit steps.
//
// This is a regression test for a bug where the witness patrol formula's
// await-signal logic was accidentally removed by subsequent commits,
// causing a tight loop when the rig was idle.
//
// See: PR #1052 (original fix), gt-tjm9q (regression report)
// See: gt-0hzeo (refinery stall bug — missing await-signal)
func TestPatrolFormulasHaveBackoffLogic(t *testing.T) {
	// Patrol formulas that must have backoff logic.
	// The loopStepID is the step that contains the await-signal logic;
	// witness/deacon use "loop-or-exit", refinery uses "burn-or-loop".
	type patrolFormula struct {
		name       string
		loopStepID string
		awaitCmd   string // "await-signal" or "await-event"
	}

	patrolFormulas := []patrolFormula{
		{"mol-witness-patrol.formula.toml", "loop-or-exit", "await-signal"},
		{"mol-deacon-patrol.formula.toml", "loop-or-exit", "await-signal"},
		{"mol-refinery-patrol.formula.toml", "burn-or-loop", "await-event"},
	}

	for _, pf := range patrolFormulas {
		t.Run(pf.name, func(t *testing.T) {
			// Read formula content directly from embedded FS
			content, err := formulasFS.ReadFile("formulas/" + pf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", pf.name, err)
			}

			contentStr := string(content)

			// Verify the formula contains the loop/decision step
			doubleQuoted := `id = "` + pf.loopStepID + `"`
			singleQuoted := `id = '` + pf.loopStepID + `'`
			if !strings.Contains(contentStr, doubleQuoted) &&
				!strings.Contains(contentStr, singleQuoted) {
				t.Fatalf("%s: %s step not found", pf.name, pf.loopStepID)
			}

			// Verify the formula contains the required backoff patterns.
			// Witness/deacon use await-signal; refinery uses await-event
			// (file-based event channel system). Both provide backoff logic.
			requiredPatterns := []string{
				pf.awaitCmd,
				"backoff",
				"gt mol step " + pf.awaitCmd,
			}

			for _, pattern := range requiredPatterns {
				if !strings.Contains(contentStr, pattern) {
					t.Errorf("%s missing required pattern %q\n"+
						"The %s step must include %s with backoff logic "+
						"to prevent tight loops when the rig is idle.\n"+
						"See PR #1052 for the original fix.",
						pf.name, pattern, pf.loopStepID, pf.awaitCmd)
				}
			}
		})
	}
}

// TestPatrolFormulasHaveReportCycle verifies that all three patrol formulas
// include `gt patrol report` in their loop step.
//
// The patrol report command atomically closes the current patrol wisp and
// starts a new one, replacing the old squash+new pattern.
//
// Regression test: replaces TestPatrolFormulasHaveSquashCycle (steveyegge/gastown#1371).
func TestPatrolFormulasHaveReportCycle(t *testing.T) {
	type patrolFormula struct {
		name       string
		loopStepID string
	}

	patrolFormulas := []patrolFormula{
		{"mol-witness-patrol.formula.toml", "loop-or-exit"},
		{"mol-deacon-patrol.formula.toml", "loop-or-exit"},
		{"mol-refinery-patrol.formula.toml", "burn-or-loop"},
	}

	for _, pf := range patrolFormulas {
		t.Run(pf.name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + pf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", pf.name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", pf.name, err)
			}

			var loopDesc string
			for _, step := range f.Steps {
				if step.ID == pf.loopStepID {
					loopDesc = step.Description
					break
				}
			}
			if loopDesc == "" {
				t.Fatalf("%s: %s step not found or has empty description", pf.name, pf.loopStepID)
			}

			// The loop step must use gt patrol report to close current and start next cycle
			if !strings.Contains(loopDesc, "gt patrol report") {
				t.Errorf("%s %s step missing \"gt patrol report\" (close current patrol and start next cycle)\n"+
					"All patrol formulas must use gt patrol report in their loop step.",
					pf.name, pf.loopStepID)
			}
		})
	}
}

// bannedWispGCInvocation reports the first line of a formula's raw source that
// actually INVOKES `bd mol wisp gc` (a bare command line inside a ```bash
// fence), as opposed to merely naming the command in the backtick-quoted
// PROHIBITED prose, where every line begins with a `>` quote marker.
//
// It scans raw source rather than parsed steps so that it covers every formula,
// including aspect formulas that Parse rejects on their own. Descriptions are
// written both as TOML multi-line strings and as single-line strings with
// escaped newlines, so literal `\n` sequences are unescaped before scanning.
func bannedWispGCInvocation(content []byte) (string, bool) {
	unescaped := strings.ReplaceAll(string(content), `\n`, "\n")
	for _, line := range strings.Split(unescaped, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "bd mol wisp gc") {
			return trimmed, true
		}
	}
	return "", false
}

// TestFormulasDoNotInvokeBannedWispGC verifies that NO embedded formula invokes
// `bd mol wisp gc` in any variant.
//
// `bd mol wisp gc --closed --force` is UNSCOPED: it deletes closed wisps across
// the whole database, not just the caller's. It destroys the active patrol
// molecule's own step ledger, deletes completed dog molecules that the hq-z70b
// `gt dog clear --force` guard depends on, and deletes rig merge-request beads,
// permanently orphaning cleanup wisps. The age-based variant additionally reaps
// the active patrol's own open steps (hq-dzz / hq-3pp).
//
// Running it is prohibited town-wide by mayor-ratified standing order
// (hq-hazr, 2026-09-01), in force until hq-hazr ships its fix of record.
//
// This test replaces TestPatrolFormulasHaveWispGC, which asserted the OPPOSITE
// and made the ban unappliable to the formula sources: patching the sources
// turned CI red, and the obvious way to green it was to revert the ban.
// See gt-9ab, gt-34h, hq-gk8d.
func TestFormulasDoNotInvokeBannedWispGC(t *testing.T) {
	entries, err := formulasFS.ReadDir("formulas")
	if err != nil {
		t.Fatalf("reading embedded formulas dir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			if line, found := bannedWispGCInvocation(content); found {
				t.Errorf("%s invokes banned wisp GC: %q\n"+
					"Wisp GC of every variant is prohibited town-wide until hq-hazr\n"+
					"ships its fix of record (mayor-ratified 2026-09-01, hq-hazr).\n"+
					"Run nothing here; stale-wisp cleanup belongs outside active\n"+
					"patrol molecules. See gt-9ab, gt-34h, hq-gk8d.",
					name, line)
			}
		})
	}
}

// TestPatrolFormulasCarryWispGCProhibition verifies that the three patrol
// formulas do not merely omit the banned command but explain WHY, so that a
// future editor restoring "cleanup" has the standing order in front of them.
//
// Regression test for gt-9ab / gt-34h: the ban previously existed only in the
// deployed copies under .beads/formulas, so `gt doctor --fix` — which reports
// the hand-patched copies as drift — would overwrite them with the armed
// sources and re-arm the command town-wide while reporting a successful repair.
func TestPatrolFormulasCarryWispGCProhibition(t *testing.T) {
	patrolFormulas := []string{
		"mol-witness-patrol.formula.toml",
		"mol-deacon-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
	}

	for _, name := range patrolFormulas {
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}

			// Find the inbox-check step (first step in all patrol formulas)
			var inboxDesc string
			for _, step := range f.Steps {
				if step.ID == "inbox-check" {
					inboxDesc = step.Description
					break
				}
			}
			if inboxDesc == "" {
				t.Fatalf("%s: inbox-check step not found or has empty description", name)
			}

			if !strings.Contains(inboxDesc, "PROHIBITED") || !strings.Contains(inboxDesc, "hq-hazr") {
				t.Errorf("%s inbox-check step is missing the hq-hazr PROHIBITED block\n"+
					"Each patrol formula must carry the standing order explaining why\n"+
					"wisp GC is not run here, so it is not silently reinstated.\n"+
					"See gt-9ab, gt-34h, hq-gk8d.",
					name)
			}
		})
	}
}

// TestPatrolFormulasUseDynamicBeadResolution verifies that patrol formulas
// resolve their agent bead ID dynamically at runtime via `gt agents resolve`,
// rather than hardcoding a prefix like `gt-<rig>-refinery`.
//
// Hardcoded IDs break when AgentBeadIDWithPrefix collapses the rig component
// (prefix == rig), producing e.g. "cp-refinery" instead of "gt-cp-refinery".
//
// Regression test for hq-9xs.
func TestPatrolFormulasUseDynamicBeadResolution(t *testing.T) {
	patrolFormulas := []string{
		"mol-witness-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
	}
	expectedResolver := map[string]string{
		"mol-witness-patrol.formula.toml":  "YOUR_AGENT_BEAD=$(gt agents resolve --role witness --rig {{rig}})",
		"mol-refinery-patrol.formula.toml": "YOUR_AGENT_BEAD=$(gt agents resolve --role refinery --rig {{rig}})",
	}

	for _, name := range patrolFormulas {
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}

			// Find the loop/exit step
			var loopDesc string
			for _, step := range f.Steps {
				if step.ID == "loop-or-exit" || step.ID == "burn-or-loop" {
					loopDesc = step.Description
					break
				}
			}
			if loopDesc == "" {
				t.Fatalf("%s: loop step not found or has empty description", name)
			}

			// Must use dynamic resolution through the agent resolver. The older
			// bd-list query only sees one table in one DB and misses wisp-backed
			// or town-stranded agent beads.
			if !strings.Contains(loopDesc, expectedResolver[name]) {
				t.Errorf("%s loop step missing dynamic agent bead resolution via gt agents resolve.\n"+
					"Agent bead IDs must be resolved at runtime, not hardcoded.\n"+
					"See hq-9xs.",
					name)
			}
			if !strings.Contains(loopDesc, `--agent-bead "$YOUR_AGENT_BEAD"`) {
				t.Errorf("%s loop step must pass the resolved agent bead to await", name)
			}
			if !strings.Contains(loopDesc, `gt agents state "$YOUR_AGENT_BEAD" --set idle=0`) {
				t.Errorf("%s loop step must reset state on the resolved agent bead", name)
			}
			if strings.Contains(loopDesc, "bd list --label=gt:agent") {
				t.Errorf("%s loop step still uses legacy bd-list agent resolution", name)
			}

			// Must NOT hardcode gt-<rig> prefix pattern
			if strings.Contains(loopDesc, "gt-<rig>") {
				t.Errorf("%s loop step hardcodes gt-<rig> prefix.\n"+
					"This breaks when AgentBeadIDWithPrefix collapses the ID (prefix == rig).\n"+
					"See hq-9xs.",
					name)
			}
			if strings.Contains(loopDesc, "{{prefix}}-{{rig}}-witness") || strings.Contains(loopDesc, "{{prefix}}-{{rig}}-refinery") {
				t.Errorf("%s loop step hardcodes prefix/rig agent bead instead of resolved ID", name)
			}
		})
	}
}

// TestDeaconPatrolHasHeartbeatSteps verifies the deacon patrol formula
// includes heartbeat refresh steps to prevent the daemon from killing a
// healthy Deacon mid-cycle.
//
// Without heartbeat refreshes, a patrol cycle that exceeds 20 minutes
// (HeartbeatVeryStaleThreshold = 20m) causes the daemon to consider the Deacon
// stuck and kill it, even though the Deacon is actively executing steps.
func TestDeaconPatrolHasHeartbeatSteps(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}

	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing deacon patrol formula: %v", err)
	}

	// brief-in precedes the heartbeat by mayor order (gt-9yi): a fresh session
	// must read the standing rulings before it runs anything, and the heartbeat
	// is "anything". brief-in is three cheap reads, so the heartbeat still lands
	// far inside the 20-minute HeartbeatVeryStaleThreshold window.
	if len(f.Steps) == 0 {
		t.Fatal("deacon patrol formula has no steps")
	}
	if f.Steps[0].ID != "brief-in" {
		t.Errorf("first step should be \"brief-in\", got %q", f.Steps[0].ID)
	}
	if len(f.Steps) < 2 || f.Steps[1].ID != "heartbeat" {
		got := ""
		if len(f.Steps) > 1 {
			got = f.Steps[1].ID
		}
		t.Errorf("second step should be \"heartbeat\", got %q", got)
	}

	// heartbeat must run early and must depend only on brief-in
	for _, step := range f.Steps {
		if step.ID != "heartbeat" {
			continue
		}
		if !strings.Contains(step.Description, "gt deacon heartbeat") {
			t.Error("heartbeat step must contain \"gt deacon heartbeat\" command")
		}
		if len(step.Needs) != 1 || step.Needs[0] != "brief-in" {
			t.Errorf("heartbeat must depend only on \"brief-in\", got %v", step.Needs)
		}
	}

	// inbox-check must depend on heartbeat
	for _, step := range f.Steps {
		if step.ID == "inbox-check" {
			hasHeartbeatDep := false
			for _, dep := range step.Needs {
				if dep == "heartbeat" {
					hasHeartbeatDep = true
					break
				}
			}
			if !hasHeartbeatDep {
				t.Error("inbox-check step must depend on \"heartbeat\" step")
			}
			break
		}
	}

	// There should be a mid-cycle heartbeat step
	foundMid := false
	foundPreAwait := false
	foundMandatoryHandoff := false
	for _, step := range f.Steps {
		if step.ID == "heartbeat-mid" {
			foundMid = true
			if !strings.Contains(step.Description, "gt deacon heartbeat") {
				t.Error("heartbeat-mid step must contain \"gt deacon heartbeat\" command")
			}
		}
		if step.ID == "loop-or-exit" && strings.Contains(step.Description, "pre-await checkpoint") {
			foundPreAwait = true
			if !strings.Contains(step.Description, "gt deacon heartbeat") {
				t.Error("loop-or-exit step must refresh heartbeat before await-signal")
			}
			if strings.Contains(step.Description, "gt handoff -s") && strings.Contains(step.Description, "mandatory") {
				foundMandatoryHandoff = true
			}
			heartbeatPos := strings.Index(step.Description, "gt deacon heartbeat \"pre-await checkpoint\"")
			awaitPos := strings.Index(step.Description, "gt mol step await-signal")
			if heartbeatPos == -1 || awaitPos == -1 {
				t.Error("loop-or-exit step must contain both pre-await heartbeat and await-signal commands")
			} else if heartbeatPos > awaitPos {
				t.Error("pre-await heartbeat must appear before await-signal to close the stale-heartbeat window")
			}
		}
	}
	if !foundMid {
		t.Error("deacon patrol formula must have a \"heartbeat-mid\" step for mid-cycle refresh")
	}
	if !foundPreAwait {
		t.Error("deacon patrol formula must refresh heartbeat again before await-signal")
	}
	if !foundMandatoryHandoff {
		t.Error("deacon patrol formula must require gt handoff after patrol report")
	}
}

// runnableLines returns the lines of a step description that could execute if a
// session pasted them: everything except blank lines, shell comments, and lines
// inside a `>` blockquote — the shape every prohibition block in these formulas
// uses, so that quoting a banned command in prose does not read as invoking it.
func runnableLines(desc string) []string {
	var out []string
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// TestDeaconPatrolCarriesServedWispProtections verifies that the five
// protection blocks that used to exist ONLY in the hand-patched served-wisp
// lineage are present in the formula SOURCE.
//
// Why this test exists (gt-9yi, hq-hazr): a wisp description is a snapshot
// taken at pour time, and pours are deterministic from source — two consecutive
// pours (hq-wisp-xa4dw, hq-wisp-ibqtr) were measured byte-identical. So a
// protection that lives only in a patched wisp does not survive the next pour,
// and re-applying it by hand every cycle is a one-cycle half-life: the mayor's
// `gt compact` ban was gone from the next pour 40 minutes after it was made.
// Dropping a block is silent — no error, no ledger trace — which is exactly
// what an assertion is for.
//
// Related: hq-gk8d (standing wisp-GC ruling), hq-zj0l (idle-triage property
// test), gt-9ab (the source strip this completes).
func TestDeaconPatrolCarriesServedWispProtections(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing deacon patrol formula: %v", err)
	}

	steps := make(map[string]string, len(f.Steps))
	for _, s := range f.Steps {
		steps[s.ID] = s.Description
	}

	requirePresent := func(stepID string, substrings ...string) {
		t.Helper()
		desc, ok := steps[stepID]
		if !ok {
			t.Errorf("step %q is missing from the deacon patrol formula", stepID)
			return
		}
		for _, want := range substrings {
			if !strings.Contains(desc, want) {
				t.Errorf("step %q no longer carries %q\n"+
					"This protection exists only here; a pour that drops it drops it silently.\n"+
					"See gt-9yi, hq-hazr.", stepID, want)
			}
		}
	}

	// (1) Step 0 brief-in: read the standing rulings before running anything.
	requirePresent("brief-in",
		"bd show hq-gk8d",
		"bd show hq-hazr",
		"gt mail inbox",
		"delete, purge, force, prune, reap, or GC",
		"DIFF, DO NOT OVERWRITE")

	// (2) Rotation exclusion for interactions.jsonl — after the 1449-wisp loss
	// this local append-only file is the sole surviving record of wisp history.
	requirePresent("log-maintenance",
		"ROTATION EXCLUSION",
		"/gt/.beads/interactions.jsonl",
		"sole surviving record of wisp history")

	// (3) The served-wisp audit / self-propagation step.
	requirePresent("served-wisp-audit",
		"SNAPSHOT TAKEN AT POUR TIME",
		"PRE-EXECUTION CHECK",
		"DIFF, NOT OVERWRITE")

	// (4) Idle triage, with check 3 as a property test rather than an
	// enumeration of known-bad states (hq-zj0l).
	requirePresent("loop-or-exit",
		"TRIAGE FIRST, THEN DECIDE",
		"hq-zj0l",
		"session_start",
		"CHECK 3 MUST TEST")
	if desc, ok := steps["loop-or-exit"]; ok {
		foundPropertyTest := false
		for _, line := range runnableLines(desc) {
			if strings.Contains(line, "gt polecat list") && strings.Contains(line, "!='idle'") {
				foundPropertyTest = true
			}
		}
		if !foundPropertyTest {
			t.Error("loop-or-exit check 3 must be the state != idle property test.\n" +
				"An enumeration of known-bad states missed state=review-needed within\n" +
				"15 minutes of going live. See hq-zj0l, gt-9yi.")
		}
		if strings.Contains(desc, "Reset the idle counter and start next patrol cycle") {
			t.Error("loop-or-exit still resets the idle counter unconditionally on signal.\n" +
				"await-signal returns on ANY beads activity; an unconditional reset gives\n" +
				"a fully idle town a full patrol every ~17 minutes. See hq-zj0l.")
		}
	}

	// (5) The gt compact ban: compaction deletes closed wisps past TTL, which is
	// the hq-gk8d banned shape reached by another road. Only the weekly rollup,
	// which branches before runDailyDigest and never compacts, stays runnable.
	//
	// NOTE the served-wisp text this block was carried from recommended
	// `gt compact report --dry-run` as a read-only substitute. When carried it
	// was not one: runDailyDigest shelled out to `gt compact --json` as a
	// separate process with no --dry-run, thirty-six lines before its own
	// dry-run guard, so the subprocess reached deleteWisp's
	// `bd delete --force`. Carrying that claim into source would have made a
	// false safety claim durable, so it was corrected here rather than
	// reproduced. Tracked for a tooling split on hq-la3m.
	//
	// gt-h7z has since fixed the source: compaction runs in-process under a
	// single compactOptions.DryRun, and TestRunDailyDigestDryRunPerformsNoDeletes
	// holds it there. THE BAN BELOW STAYS ANYWAY, and this test still enforces
	// it. Agents invoke the installed `gt`, not this tree, and lifting the
	// prohibition is the mayor's call — relaxing the formula text on the
	// strength of a source fix would hand a still-broken binary a safety claim
	// it does not have.
	requirePresent("compact-report",
		"BANNED IN ITS MUTATING FORM",
		"hq-gk8d",
		"IS ALSO BANNED — IT IS NOT READ-ONLY")
	if desc, ok := steps["compact-report"]; ok {
		for _, line := range runnableLines(desc) {
			if !strings.HasPrefix(line, "gt compact") {
				continue
			}
			if !strings.Contains(line, "--weekly") {
				t.Errorf("compact-report has a runnable compaction command: %q\n"+
					"`gt compact report` runs compaction as a side effect of sending a\n"+
					"digest, deleting closed wisps past TTL — and --dry-run does NOT\n"+
					"suppress it. Only --weekly is safe. See hq-gk8d, hq-hazr, hq-la3m.", line)
			}
		}
	}
}
