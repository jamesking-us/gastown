package templates

import (
	"strings"
	"testing"
)

// runnablePatrolReportLines returns every line of text that reads as a RUNNABLE
// invocation of the countermanded patrol-report command without carrying the
// DO-NOT-RUN marker.
//
// "Runnable" is line-initial invocation OR the command wrapped in inline code —
// NOT mere occurrence of the string. The countermand block itself names the
// command several times inside prohibition prose, and that is exactly the text
// we want to keep. Rejecting mere occurrence follows the audit method of record
// on hq-gk8d ("regexp for a LINE-INITIAL command ... mere occurrence of the
// string is not the test"); the inline-code case is added because line-initial
// alone misses the shape this repo actually served, which had prose in front of
// the command (see TestRunnablePatrolReportLinesDetection).
func runnablePatrolReportLines(text string) []string {
	needle := CmdName() + " patrol report"

	var armed []string
	for _, line := range strings.Split(text, "\n") {
		// (a) A line-initial invocation: a command block or a bare command.
		runnable := strings.HasPrefix(stripCommandDecoration(line), needle)

		// (b) The command inside inline code, anywhere on the line. This is the
		// form the witness template served ("- Report and loop: `gt patrol
		// report ...`"), and backticks are the dangerous wrapper specifically:
		// a reader who transcribes them into a double-quoted bash string or an
		// unquoted heredoc executes the command (hq-baoe).
		if strings.Contains(line, "`"+needle) {
			runnable = true
		}

		if !runnable || strings.Contains(line, "COUNTERMANDED") {
			continue
		}
		armed = append(armed, line)
	}
	return armed
}

// stripCommandDecoration removes what a served markdown doc puts in front of a
// command it intends the reader to run — indentation, blockquote markers, list
// bullets, a shell prompt, an inline-code backtick — and nothing else.
//
// It deliberately does NOT strip "**", so a command named inside bold
// prohibition prose ("**gt patrol report** does not append the report") is not
// mistaken for an instruction to run it.
func stripCommandDecoration(line string) string {
	s := line
	for {
		before := s
		s = strings.TrimLeft(s, " \t>")
		for _, bullet := range []string{"- ", "* ", "+ ", "$ "} {
			s = strings.TrimPrefix(s, bullet)
		}
		s = strings.TrimPrefix(s, "`")
		if s == before {
			return s
		}
	}
}

// TestRunnablePatrolReportLinesDetection is the control on the guard below: a
// detector that never fires proves nothing. The armed cases are the exact lines
// the role templates served before gt-p2x; the inert cases are text that must
// stay serveable.
func TestRunnablePatrolReportLinesDetection(t *testing.T) {
	cmd := CmdName()

	armed := []string{
		// witness.md.tmpl, Context Management (the reported surface)
		"   - Report and loop: `" + cmd + " patrol report --summary \"<summary>\"`",
		// deacon.md.tmpl / refinery.md.tmpl, inside a bash fence
		cmd + " patrol report --summary \"Checked inbox, scanned health, no issues\"",
		// refinery.md.tmpl, Key Commands list
		"- `" + cmd + " patrol report --summary \"...\"` — Close current patrol",
		// a shell-prompt transcription
		"$ " + cmd + " patrol report",
	}
	for _, line := range armed {
		if got := runnablePatrolReportLines(line); len(got) != 1 {
			t.Errorf("armed line not detected: %q", line)
		}
	}

	inert := []string{
		PatrolReportNeutered("--summary \"<summary>\""),
		"> **" + cmd + " patrol report** does not append, attach, or comment the report.",
		"Mark skipped steps as SKIP in the patrol report.",
		"The daemon schedules the next cycle off your handoff.",
	}
	for _, line := range inert {
		if got := runnablePatrolReportLines(line); len(got) != 0 {
			t.Errorf("inert line flagged as runnable: %q", line)
		}
	}
}

// TestRoleContextsServeNoRunnablePatrolReport is the gt-p2x regression guard.
//
// hq-hsc6 measured that the patrol-report command REPLACES the patrol wisp's
// description with the cycle summary (72172 chars -> 2641), unrecoverably,
// because wisps sit in dolt_ignore and carry no Dolt history. The served wisps
// were neutered by hand, but the role context documents that `gt prime` prints
// were not, and prime is what a fresh session reads FIRST — its startup protocol
// says execute immediately. So an un-neutered instruction here fires before the
// session can ever reach the countermand in its own wisp. Keep every role
// context free of a runnable form.
func TestRoleContextsServeNoRunnablePatrolReport(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, role := range tmpl.RoleNames() {
		data := RoleData{
			Role:          role,
			RigName:       "testrig",
			TownRoot:      "/test/town",
			TownName:      "town",
			WorkDir:       "/test/town",
			DefaultBranch: "main",
			Polecat:       "testcat",
			DogName:       "testdog",
			MayorSession:  "gt-town-mayor",
			DeaconSession: "gt-town-deacon",
		}

		output, err := tmpl.RenderRole(role, data)
		if err != nil {
			t.Fatalf("RenderRole(%q) error = %v", role, err)
		}

		if armed := runnablePatrolReportLines(output); len(armed) > 0 {
			t.Errorf("role context %q serves %d runnable patrol-report line(s) — "+
				"countermanded by hq-hsc6, see gt-p2x. Neuter with "+
				"{{ patrolReportNeutered }} under a {{ patrolReportCountermand }} "+
				"block.\nOffending lines:\n%s",
				role, len(armed), strings.Join(armed, "\n"))
		}
	}
}

// TestPatrolReportNeuteredIsInert checks the two properties that make the
// neutered form safe to serve.
func TestPatrolReportNeuteredIsInert(t *testing.T) {
	line := PatrolReportNeutered("--summary \"<summary>\"")

	if !strings.Contains(line, "COUNTERMANDED") {
		t.Errorf("neutered line carries no DO-NOT-RUN marker: %q", line)
	}

	// No backticks and no $( — a reader transcribing this line into a
	// double-quoted bash string or an unquoted heredoc must not execute it.
	// Documenting the ban has run the banned command before (hq-baoe).
	if strings.ContainsAny(line, "`") || strings.Contains(line, "$(") {
		t.Errorf("neutered line contains shell command substitution, which "+
			"executes when transcribed into a double-quoted string or an "+
			"unquoted heredoc (hq-baoe): %q", line)
	}
}

// TestPatrolReportCountermandBlockIsInert applies the same no-substitution rule
// to the countermand block, which is the text most likely to be copied into a
// mail body or bead comment by an agent explaining the prohibition.
func TestPatrolReportCountermandBlockIsInert(t *testing.T) {
	block := PatrolReportCountermand()

	if strings.ContainsAny(block, "`") || strings.Contains(block, "$(") {
		t.Error("countermand block contains shell command substitution — the " +
			"prohibition's own documentation must not be executable (hq-baoe)")
	}
	if !strings.Contains(block, "hq-hsc6") {
		t.Error("countermand block missing its bead of record (hq-hsc6)")
	}
	if len(runnablePatrolReportLines(block)) > 0 {
		t.Error("countermand block itself contains a runnable patrol-report line")
	}
}
