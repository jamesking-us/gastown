package config

import "testing"

func TestGoflagsWithParallelismCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		existing    string
		parallelism int
		want        string
	}{
		{"empty goflags gets the cap", "", 4, "-p=4"},
		{"existing flags are preserved", "-mod=mod", 4, "-mod=mod -p=4"},
		{"explicit -p= wins", "-p=16", 4, "-p=16"},
		{"explicit bare -p wins", "-mod=mod -p", 4, "-mod=mod -p"},
		{"-parallel is not -p", "-parallel=8", 4, "-parallel=8 -p=4"},
		{"zero parallelism disables injection", "-mod=mod", 0, "-mod=mod"},
		{"negative parallelism disables injection", "", -1, ""},
		{"surrounding whitespace is trimmed", "  -mod=mod  ", 4, "-mod=mod -p=4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GoflagsWithParallelismCap(tt.existing, tt.parallelism); got != tt.want {
				t.Errorf("GoflagsWithParallelismCap(%q, %d) = %q, want %q",
					tt.existing, tt.parallelism, got, tt.want)
			}
		})
	}
}

func TestResolveGoParallelism(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int
	}{
		// t.Setenv cannot unset a variable; empty is the same input path as unset.
		{name: "empty uses default", env: "", want: GoParallelismDefault},
		{name: "override honored", env: "2", want: 2},
		{name: "whitespace tolerated", env: " 8 ", want: 8},
		{name: "zero disables the cap", env: "0", want: 0},
		{name: "garbage falls back to default", env: "lots", want: GoParallelismDefault},
		{name: "negative falls back to default", env: "-3", want: GoParallelismDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(GoParallelismEnvVar, tt.env)
			if got := ResolveGoParallelism(); got != tt.want {
				t.Errorf("ResolveGoParallelism() with %s=%q = %d, want %d",
					GoParallelismEnvVar, tt.env, got, tt.want)
			}
		})
	}
}

// Agent sessions must carry the -p cap structurally: the recurring I/O storms
// (gt-38l) came from workers who were never served the serialization rule.
func TestAgentEnv_CapsGoParallelism(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	t.Setenv(GoParallelismEnvVar, "")

	for _, role := range []string{"polecat", "crew", "witness", "refinery", "deacon", "mayor"} {
		env := AgentEnv(AgentEnvConfig{
			Role:      role,
			Rig:       "myrig",
			AgentName: "toast",
			TownRoot:  "/town",
		})
		if got := env["GOFLAGS"]; got != "-p=4" {
			t.Errorf("role %s: GOFLAGS = %q, want %q", role, got, "-p=4")
		}
	}
}

func TestAgentEnv_PreservesOperatorGoflags(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv(GoParallelismEnvVar, "")

	env := AgentEnv(AgentEnvConfig{Role: "polecat", Rig: "myrig", AgentName: "toast", TownRoot: "/town"})
	if got := env["GOFLAGS"]; got != "-mod=mod -p=4" {
		t.Errorf("GOFLAGS = %q, want %q", got, "-mod=mod -p=4")
	}
}

func TestAgentEnv_GoParallelismOptOutLeavesGoflagsUntouched(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	t.Setenv(GoParallelismEnvVar, "0")

	env := AgentEnv(AgentEnvConfig{Role: "polecat", Rig: "myrig", AgentName: "toast", TownRoot: "/town"})
	if _, ok := env["GOFLAGS"]; ok {
		t.Errorf("GOFLAGS set to %q, want unset when the cap is disabled", env["GOFLAGS"])
	}
}
