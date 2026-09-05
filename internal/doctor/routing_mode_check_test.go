package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingModeCheckEvaluate(t *testing.T) {
	check := NewRoutingModeCheck()

	tests := []struct {
		name       string
		settings   routingSettings
		wantStatus CheckStatus
		wantDetail string
	}{
		{
			name: "explicit in config.yaml with a clean database is healthy",
			settings: routingSettings{
				yaml: map[string]string{"routing.mode": "explicit"},
				db:   map[string]string{},
			},
			wantStatus: StatusOK,
		},
		{
			// The gt-sym failure: nothing in config.yaml, auto mode plus a
			// contributor repo in the beads database. bd resolves a fork-backed
			// rig as contributor, so reads and writes both leave the rig.
			name: "auto mode and a contributor repo in the database is an error",
			settings: routingSettings{
				yaml: map[string]string{},
				db: map[string]string{
					"routing.mode":        "auto",
					"routing.contributor": "/home/agent/.beads-planning",
				},
			},
			wantStatus: StatusError,
			wantDetail: "routing.contributor=/home/agent/.beads-planning (beads database)",
		},
		{
			// The recurrence mechanism: config.yaml outranks the database rows
			// today, and stops outranking them the moment it is regenerated.
			name: "explicit config.yaml shadowing database rows still warns",
			settings: routingSettings{
				yaml: map[string]string{"routing.mode": "explicit"},
				db: map[string]string{
					"routing.mode":        "auto",
					"routing.contributor": "/home/agent/.beads-planning",
				},
			},
			wantStatus: StatusWarning,
			wantDetail: "routing.contributor=/home/agent/.beads-planning",
		},
		{
			name:       "nothing configured defaults to auto and warns",
			settings:   routingSettings{yaml: map[string]string{}, db: map[string]string{}},
			wantStatus: StatusWarning,
		},
		{
			// routing.default routes in explicit mode too.
			name: "routing.default routes even in explicit mode",
			settings: routingSettings{
				yaml: map[string]string{"routing.mode": "explicit", "routing.default": "/home/agent/.beads-planning"},
				db:   map[string]string{},
			},
			wantStatus: StatusError,
			wantDetail: "routing.default=/home/agent/.beads-planning (config.yaml)",
		},
		{
			// bd treats contributor.auto_route=true as auto mode, and
			// contributor.planning_repo as the contributor target.
			name: "legacy contributor keys are a live route",
			settings: routingSettings{
				yaml: map[string]string{},
				db: map[string]string{
					"contributor.auto_route":    "true",
					"contributor.planning_repo": "/home/agent/.beads-planning",
				},
			},
			wantStatus: StatusError,
			wantDetail: "contributor.planning_repo=/home/agent/.beads-planning",
		},
		{
			name: "auto mode with no target repo only warns",
			settings: routingSettings{
				yaml: map[string]string{"routing.mode": "auto"},
				db:   map[string]string{},
			},
			wantStatus: StatusWarning,
		},
		{
			name: "a routing value of '.' stays on the rig",
			settings: routingSettings{
				yaml: map[string]string{"routing.mode": "explicit", "routing.default": "."},
				db:   map[string]string{},
			},
			wantStatus: StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check.evaluate(tt.settings, "rig 'gastown'")
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %v, want %v (message: %s)", got.Status, tt.wantStatus, got.Message)
			}
			if tt.wantDetail == "" {
				return
			}
			joined := strings.Join(got.Details, "\n")
			if !strings.Contains(joined, tt.wantDetail) {
				t.Fatalf("details missing %q:\n%s", tt.wantDetail, joined)
			}
		})
	}
}

func TestRoutingSettingsConfigYAMLWinsOverDatabase(t *testing.T) {
	settings := routingSettings{
		yaml: map[string]string{"routing.mode": "explicit"},
		db:   map[string]string{"routing.mode": "auto"},
	}

	if got := settings.mode(); got != "explicit" {
		t.Fatalf("mode() = %q, want explicit", got)
	}
	if got := settings.source("routing.mode"); got != "config.yaml" {
		t.Fatalf("source() = %q, want config.yaml", got)
	}
	if got := settings.source("routing.contributor"); got != "beads database" {
		t.Fatalf("source() for an unset yaml key = %q, want beads database", got)
	}
}

func TestReadYAMLRoutingConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{
			name:    "flat dotted key",
			content: "prefix: gt\nrouting.mode: \"explicit\"\n",
			want:    map[string]string{"routing.mode": "explicit"},
		},
		{
			name:    "nested mapping",
			content: "prefix: hq\nrouting:\n    mode: explicit\n    contributor: /home/agent/.beads-planning\n",
			want: map[string]string{
				"routing.mode":        "explicit",
				"routing.contributor": "/home/agent/.beads-planning",
			},
		},
		{
			name:    "no routing config",
			content: "prefix: gt\nissue-prefix: gt\n",
			want:    map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.content), 0644); err != nil {
				t.Fatalf("write config.yaml: %v", err)
			}

			got, err := readYAMLRoutingConfig(configPath)
			if err != nil {
				t.Fatalf("readYAMLRoutingConfig: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Fatalf("%s = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestReadYAMLRoutingConfigMissingFile(t *testing.T) {
	got, err := readYAMLRoutingConfig(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("missing config.yaml should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

// routingLocations must follow routes.jsonl to the seat that actually serves a
// prefix. The gt- route points at gastown/mayor/rig, which is not the directory
// ctx.RigPath() names.
func TestRoutingLocationsFollowsRoutes(t *testing.T) {
	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	routes := `{"prefix":"hq-","path":"."}
{"prefix":"gt-","path":"gastown/mayor/rig"}
{"prefix":"ki-","path":"kingforge"}
`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	got := routingLocations(&CheckContext{TownRoot: townRoot, RigName: "gastown"})

	want := map[string]string{
		townBeads: "town",
		filepath.Join(townRoot, "gastown/mayor/rig", ".beads"): "rig 'gastown'",
		filepath.Join(townRoot, "kingforge", ".beads"):         "rig 'kingforge'",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d locations, want %d: %+v", len(got), len(want), got)
	}
	for _, loc := range got {
		name, ok := want[loc.beadsDir]
		if !ok {
			t.Fatalf("unexpected location %q", loc.beadsDir)
		}
		if loc.name != name {
			t.Fatalf("location %q named %q, want %q", loc.beadsDir, loc.name, name)
		}
	}
}

func TestRoutingLocationsWithoutRoutesFile(t *testing.T) {
	townRoot := t.TempDir()
	got := routingLocations(&CheckContext{TownRoot: townRoot})
	if len(got) != 1 || got[0].name != "town" {
		t.Fatalf("got %+v, want just the town location", got)
	}
}
