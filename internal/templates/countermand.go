package templates

import "strings"

// This file holds the canonical inline countermand text for
// "<cmd> patrol report", which is countermanded by hq-hsc6 until that bead
// ships a fix making the cycle report additive.
//
// Why it lives here rather than being written out at each call site: the
// countermand has to appear at EVERY point of instruction (the mayor's
// inline-countermand precedent, hq-gk8d served-wisp clause), and the served
// surfaces are split between Go string literals (the patrol work loop printed
// by gt prime) and the embedded role templates. Three hand-copies drift; one
// constant does not.
//
// Formatting rules for this text, which are load-bearing, not cosmetic:
//
//   - The command is written in bold, NEVER in backticks. Backticks and
//     dollar-paren are command substitution inside a double-quoted bash string
//     and inside an unquoted heredoc, so an agent transcribing a backticked
//     prohibition into a -m body executes the very command being banned. That
//     has happened (hq-baoe: cl-refinery advanced its own patrol hook and caused
//     unrecoverable wisp overwrites purely by quoting the command in a mail
//     body). The prohibition's own documentation is executable; keep it inert.
//     The body below is a Go raw string literal, which is itself backtick
//     delimited — so a backtick cannot be added to it without breaking the
//     build. Keep it that way; do not convert it to a quoted string.
//   - The instruction line itself is kept, not deleted, but prefixed with a
//     DO-NOT-RUN marker so a reader can still recognize the command when they
//     meet it elsewhere.

// patrolReportCountermandMarker prefixes any surviving transcription of the
// countermanded command so it cannot be mistaken for a live instruction.
const patrolReportCountermandMarker = "[COUNTERMANDED — DO NOT RUN, see the hq-hsc6 countermand block above]"

// patrolReportCountermandBody is the countermand block, with {{cmd}} standing
// in for the CLI command name. Rendered by PatrolReportCountermand.
const patrolReportCountermandBody = `> ## ⛔⛔ **{{cmd}} patrol report** IS COUNTERMANDED — IT DESTROYS THIS PATROL WISP ⛔⛔
>
> Measured on hq-hsc6; in force until hq-hsc6 ships a fix that makes the cycle
> report additive. Printed inline at the point of the instruction, per the
> mayor's inline-countermand precedent (hq-gk8d served-wisp clause).
>
> **{{cmd}} patrol report** does not append, attach, or comment the cycle report.
> It **REPLACES the patrol wisp's description field** with the report. Measured:
> closing hq-wisp-b8pbe took its stored description from 72172 chars / 1692 lines
> to 2641 chars / 3 lines — a 96.3% silent overwrite on the tool's own normal
> success path, with no confirmation prompt.
>
> **WHAT IT DELETES IS YOUR PATROL FORMULA — including the standing orders in it.**
> Every prohibition block in a served wisp exists only as a hand-merge into that
> wisp's text; pours are deterministic and regress all of them.
>
> **THE LOSS IS UNRECOVERABLE.** wisps and wisp_% are in dolt_ignore, so there is
> no commit, no AS OF, and no snapshot to restore from (hq-6ewp).
>
> **AND IT USUALLY GUTS A WISP YOU ARE NOT LOOKING AT.** The report closes and
> pours on a lineage that the hook does not serve, so the damage lands out of view
> while the session that caused it sees a healthy hook.
>
> **WHAT TO RUN INSTEAD — this replaces the report step, in full:**
>
> 1. Post the cycle digest and the full step audit as a COMMENT on a durable bead:
>    bd comment <your patrol/agent bead> '<summary + step audit>'. A comment is
>    additive and cannot overwrite a description. Verify it by re-reading the bead.
> 2. Then go straight to the handoff step, which is unaffected and still mandatory.
>
> **Do NOT substitute --dry-run or any other flag as a "safe read"** — that guess
> is exactly what made hq-b8eo dangerous.
>
> **⚠ ESCAPE HATCH (mayor amendment):** the loop evidence behind this countermand
> was measured at the DEACON seat and may not describe yours. If YOUR loop
> **demonstrably** fails to advance without the report (hook not advancing,
> successor wisp not arming), then BEFORE running it: (1) archive this wisp's FULL
> current description to your patrol/agent bead as a bd comment, and verify the
> comment stored by re-reading the bead; (2) only then run the report — the
> overwrite now loses nothing; (3) report the dependency to the mayor by mail so
> the formula can be fixed for your loop. Do not skip step 1, and do not treat a
> guess about your loop's mechanics as a demonstration.
>
> **When you transcribe this command anywhere — mail, bead comment, commit
> message, merge record — use single quotes or a single-quoted heredoc, and strip
> the backticks.** Backticks and dollar-paren are command substitution inside a
> double-quoted bash string and inside an unquoted heredoc, so documenting the ban
> has itself run the command (hq-baoe). Never pass a body with -m and backticks in
> it, and never use an unquoted heredoc.
>
> Ref: hq-hsc6 (countermand of record), hq-gk8d (standing ruling), hq-6ewp,
> hq-45p5, hq-f73g, gt-p2x (this prime-context surface).`

// PatrolReportCountermand returns the inline countermand block for
// "<cmd> patrol report" as a markdown blockquote, ready to print into served
// role context. See hq-hsc6.
func PatrolReportCountermand() string {
	return strings.ReplaceAll(patrolReportCountermandBody, "{{cmd}}", CmdName())
}

// PatrolReportNeutered returns the countermanded patrol-report invocation with
// a DO-NOT-RUN marker and no backticks, for use wherever the served text used
// to print it as a live instruction. Always print PatrolReportCountermand
// immediately above it — the marker refers to "the block above".
//
// args is appended verbatim (e.g. `--summary "<summary>"`); pass "" for none.
func PatrolReportNeutered(args string) string {
	line := patrolReportCountermandMarker + " " + CmdName() + " patrol report"
	if args != "" {
		line += " " + args
	}
	return line
}
