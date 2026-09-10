package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/cli"
)

// TestPatrolCycleEndStepIsNeutered guards the second gt-p2x surface: the
// "<Role> Patrol Work Loop" that `gt prime` prints from Go string literals
// (patrol_helpers.go renders PatrolConfig.WorkLoopSteps).
//
// This text is what a fresh patrol session reads FIRST, and the startup protocol
// immediately above it says execute now, without confirmation. The served wisps
// were neutered by hand after hq-hsc6, but the wisp is ~700 lines long and the
// countermand sits deep inside it — a session that follows the work loop printed
// at the top of prime runs the command before it ever gets there. The reporting
// witness nearly did.
func TestPatrolCycleEndStepIsNeutered(t *testing.T) {
	needle := cli.Name() + " patrol report"

	for _, role := range []string{"Deacon patrol", "Witness patrol", "Refinery patrol"} {
		step := patrolCycleEndStep(role)

		if !strings.Contains(step, "COUNTERMANDED") {
			t.Errorf("%s cycle-end step carries no countermand marker", role)
		}
		if !strings.Contains(step, "hq-hsc6") {
			t.Errorf("%s cycle-end step does not cite hq-hsc6, so a reader "+
				"cannot check whether the countermand still stands", role)
		}
		if !strings.Contains(step, "handoff -s \""+role+"\"") {
			t.Errorf("%s cycle-end step lost its handoff instruction — the "+
				"handoff is what turns the loop once the report is banned", role)
		}

		// Every mention of the command must either sit inside the countermand
		// blockquote ("> "-prefixed — the block whose header declares the
		// command countermanded) or carry the DO-NOT-RUN marker on its own
		// line. Mentions inside the block are fine; a bare instruction line
		// is not. The backtick check below applies to every line, block
		// included — backticks execute on transcription regardless of where
		// the line came from (hq-baoe).
		for _, line := range strings.Split(step, "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			inCountermandBlock := strings.HasPrefix(strings.TrimLeft(line, " \t"), ">")
			if !inCountermandBlock && !strings.Contains(line, "COUNTERMANDED") {
				t.Errorf("%s cycle-end step serves an un-neutered "+
					"patrol-report line: %q", role, line)
			}
			if strings.Contains(line, "`") {
				t.Errorf("%s cycle-end step wraps the countermanded command "+
					"in backticks, which execute when the line is transcribed "+
					"into a double-quoted string or an unquoted heredoc "+
					"(hq-baoe): %q", role, line)
			}
		}
	}
}
