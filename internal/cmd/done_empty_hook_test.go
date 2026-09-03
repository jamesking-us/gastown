package cmd

import (
	"strings"
	"testing"
)

// The two variants this bug exists to close, as they were measured live on
// 2026-09-01 (cl-lqj). Both restarted into an empty hook; both would have
// submitted; only one of them is stopped by the obvious assignee/open-bead
// check, which is why that check must not ship as the fix.
//
//	foundation — bead had been reassigned away. Not the assignee. An
//	             assignee check stops it.
//	refuge     — bead reopened by the mayor and assigned to it, so it IS the
//	             assignee of an open bead, and its branch carries the only
//	             REVIEWED copy of the work. An assignee check waves it through.
//
// Both reach refuseEmptyHookSubmit with the same input, because "open with an
// assignee" is not work on a hook: activeWorkStatuses is hooked|in_progress,
// so a reopened bead produces no active assignment for the seat.
func TestRefuseEmptyHookSubmitBothCascadeVariants(t *testing.T) {
	variants := []struct {
		name string
		in   emptyHookSubmit
	}{
		{
			// Reassigned away: not the assignee, bead not on its hook.
			name: "foundation: no longer the assignee",
			in: emptyHookSubmit{
				ExitType:     ExitCompleted,
				PolecatSeat:  true,
				Seat:         "cloudcontentmanager/polecats/foundation",
				Branch:       "polecat/foundation/cl-39w.6+mtigkuzc",
				ActiveHook:   nil,
				CommitsAhead: 3,
			},
		},
		{
			// Still the assignee of a bead the mayor reopened. The assignee and
			// open-bead checks both pass here; the hook is still empty.
			name: "refuge: still the assignee of a reopened bead",
			in: emptyHookSubmit{
				ExitType:     ExitCompleted,
				PolecatSeat:  true,
				Seat:         "cloudcontentmanager/polecats/refuge",
				Branch:       "polecat/refuge/cl-39w.6+mtihsdo0",
				ActiveHook:   nil,
				CommitsAhead: 7,
			},
		},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			err := refuseEmptyHookSubmit(v.in)
			if err == nil {
				t.Fatalf("refuseEmptyHookSubmit(%+v) = nil, want refusal", v.in)
			}
			msg := err.Error()
			for _, want := range []string{"REFUSING TO SUBMIT", "cl-lqj", v.in.Seat, v.in.Branch} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal message missing %q:\n%s", want, msg)
				}
			}
			// The refusal must not advertise a way to submit anyway. Agents
			// read error messages and self-bypass; the only exit it offers is
			// the one that creates no merge request.
			for _, forbidden := range []string{"--issue", "--cleanup-status", "--force", "--skip-verify"} {
				if strings.Contains(msg, forbidden) {
					t.Errorf("refusal message advertises bypass %q:\n%s", forbidden, msg)
				}
			}
		})
	}
}

func TestRefuseEmptyHookSubmit(t *testing.T) {
	hooked := []string{"cl-abc"}

	tests := []struct {
		name       string
		in         emptyHookSubmit
		wantRefuse bool
	}{
		{
			// Positive control: the ordinary polecat submit is untouched.
			name: "hooked bead with commits submits",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", ActiveHook: hooked, CommitsAhead: 2,
			},
		},
		{
			// The bead a polecat claimed with `bd update --status=in_progress`
			// is on its hook the same way a slung one is (hq-xa4z).
			name: "in_progress claim counts as a hook",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", ActiveHook: []string{"cl-claimed"}, CommitsAhead: 1,
			},
		},
		{
			name: "empty hook with commits is refused",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 1,
			},
			wantRefuse: true,
		},
		{
			// Blank ids are not a hook — a lookup that returned placeholders
			// must not read as work slung to the seat.
			name: "blank hook ids are not a hook",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", ActiveHook: []string{"", "   "}, CommitsAhead: 1,
			},
			wantRefuse: true,
		},
		{
			// The already-pushed branch path creates an MR just as surely as
			// local commits do, so it is refused on the same terms.
			name: "empty hook with pushed branch is refused",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 0, BranchPushedWithWork: true,
			},
			wantRefuse: true,
		},
		{
			// Nothing to submit, so nothing to refuse: a hookless seat keeps a
			// way out. Refusing here would strand it as a zombie, which is what
			// starts the restart loop.
			name: "empty hook with nothing to submit exits",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 0,
			},
		},
		{
			name: "empty hook deferred exit is allowed",
			in: emptyHookSubmit{
				ExitType: ExitDeferred, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 9,
			},
		},
		{
			name: "empty hook escalated exit is allowed",
			in: emptyHookSubmit{
				ExitType: ExitEscalated, PolecatSeat: true, Seat: "rig/polecats/dust",
				Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 9,
			},
		},
		{
			// Crew, witnesses and humans do not work a hook; gt done already
			// rejects non-polecat actors on its own terms.
			name: "non-polecat seat is not subject to the invariant",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: false, Seat: "rig/crew/max",
				Branch: "feat/thing", CommitsAhead: 4,
			},
		},
		{
			// An identity we could not resolve inside a polecat session is
			// still a polecat seat, and is refused rather than waved through.
			name: "unresolved identity on a polecat seat is refused",
			in: emptyHookSubmit{
				ExitType: ExitCompleted, PolecatSeat: true, Seat: "",
				Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 1,
			},
			wantRefuse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := refuseEmptyHookSubmit(tt.in)
			if tt.wantRefuse && err == nil {
				t.Fatalf("refuseEmptyHookSubmit(%+v) = nil, want refusal", tt.in)
			}
			if !tt.wantRefuse && err != nil {
				t.Fatalf("refuseEmptyHookSubmit(%+v) = %v, want nil", tt.in, err)
			}
		})
	}
}

// The refusal is a property of the seat's hook alone. An issue id supplied by
// the caller, decoded from the branch, or belonging to a bead this seat owns
// cannot manufacture the hook it does not have — those are the inputs a
// restarted session can produce on its own.
func TestRefuseEmptyHookSubmitIgnoresManufacturableInputs(t *testing.T) {
	base := emptyHookSubmit{
		ExitType: ExitCompleted, PolecatSeat: true, Seat: "rig/polecats/dust",
		Branch: "polecat/dust/cl-abc+m1", CommitsAhead: 5,
	}
	if err := refuseEmptyHookSubmit(base); err == nil {
		t.Fatal("baseline empty-hook submit was not refused")
	}

	// Same invocation, every branch-derived and caller-supplied detail changed.
	for _, branch := range []string{"polecat/dust/cl-other+m2", "feature-work", ""} {
		v := base
		v.Branch = branch
		if err := refuseEmptyHookSubmit(v); err == nil {
			t.Errorf("branch %q produced no refusal — branch-derived state must not lift the invariant", branch)
		}
	}
}

func TestEmptyHookBlocksSourceClose(t *testing.T) {
	tests := []struct {
		name        string
		polecatSeat bool
		activeHook  []string
		want        bool
	}{
		{name: "hookless polecat may not close", polecatSeat: true, want: true},
		{name: "hooked polecat closes normally", polecatSeat: true, activeHook: []string{"cl-abc"}},
		{name: "blank ids are not a hook", polecatSeat: true, activeHook: []string{""}, want: true},
		{name: "non-polecat unaffected", polecatSeat: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emptyHookBlocksSourceClose(tt.polecatSeat, tt.activeHook); got != tt.want {
				t.Fatalf("emptyHookBlocksSourceClose(%v, %v) = %v, want %v",
					tt.polecatSeat, tt.activeHook, got, tt.want)
			}
		})
	}
}

func TestDoneSeatIsPolecat(t *testing.T) {
	tests := []struct {
		name       string
		actor      string
		polecatEnv string
		want       bool
	}{
		{name: "polecat actor", actor: "rig/polecats/dust", want: true},
		{name: "polecat env only", polecatEnv: "dust", want: true},
		{name: "crew actor", actor: "gastown/crew/max"},
		{name: "witness actor", actor: "rig/witness"},
		{name: "nothing known", want: false},
		{name: "blank env is not a marker", polecatEnv: "   "},
		{name: "crew actor inside a polecat session is still a polecat seat", actor: "gastown/crew/max", polecatEnv: "dust", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doneSeatIsPolecat(tt.actor, tt.polecatEnv); got != tt.want {
				t.Fatalf("doneSeatIsPolecat(%q, %q) = %v, want %v", tt.actor, tt.polecatEnv, got, tt.want)
			}
		})
	}
}
