package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdleTimeoutCheck_Run(t *testing.T) {
	townRoot := t.TempDir()

	// Create town beads with routes.jsonl
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown"}
{"prefix":"bd-","path":"beads"}
`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create gastown rig with correct idle-timeout
	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	gastownConfig := `prefix: gt
issue-prefix: gt
dolt.idle-timeout: "0"
`
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(gastownConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// Create beads rig WITHOUT idle-timeout
	beadsBeads := filepath.Join(townRoot, "beads", ".beads")
	if err := os.MkdirAll(beadsBeads, 0755); err != nil {
		t.Fatal(err)
	}
	beadsConfig := `prefix: bd
issue-prefix: bd
`
	if err := os.WriteFile(filepath.Join(beadsBeads, "config.yaml"), []byte(beadsConfig), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := &CheckContext{
		TownRoot: townRoot,
	}

	check := NewIdleTimeoutCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(result.Details) != 1 {
		t.Errorf("expected 1 rig missing idle-timeout, got %d: %v", len(result.Details), result.Details)
	}
	if result.Details[0] != "beads" {
		t.Errorf("expected 'beads' in details, got %q", result.Details[0])
	}
}

func TestIdleTimeoutCheck_Run_AllCorrect(t *testing.T) {
	townRoot := t.TempDir()

	// Create town beads with routes.jsonl
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown"}
`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create gastown rig with correct idle-timeout
	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	gastownConfig := `prefix: gt
issue-prefix: gt
dolt.idle-timeout: "0"
`
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(gastownConfig), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := &CheckContext{
		TownRoot: townRoot,
	}

	check := NewIdleTimeoutCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func TestIdleTimeoutCheck_Fix(t *testing.T) {
	townRoot := t.TempDir()

	// Create town beads with routes.jsonl
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"gt-","path":"gastown"}
`
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create gastown rig WITHOUT idle-timeout
	gastownBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(gastownBeads, 0755); err != nil {
		t.Fatal(err)
	}
	gastownConfig := `prefix: gt
issue-prefix: gt
`
	if err := os.WriteFile(filepath.Join(gastownBeads, "config.yaml"), []byte(gastownConfig), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := &CheckContext{
		TownRoot: townRoot,
	}

	check := NewIdleTimeoutCheck()
	err := check.Fix(ctx)
	if err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify config was updated
	data, err := os.ReadFile(filepath.Join(gastownBeads, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `dolt.idle-timeout: "0"`) {
		t.Errorf("config.yaml should contain dolt.idle-timeout: \"0\", got:\n%s", content)
	}
}

// TestIdleTimeoutCheckFix_PreservesRigPrefix guards gt-7pn: the idle-timeout
// fix used to call EnsureConfigYAML with an empty prefix, which blanked
// prefix/issue-prefix in every rig's config.yaml and handed routing to bd's
// Dolt config fallback (the state that produced gt-sym).
func TestIdleTimeoutCheckFix_PreservesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	rigBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	configPath := filepath.Join(rigBeads, "config.yaml")
	original := "prefix: gt\nissue-prefix: gt\nexport.auto: \"false\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	check := NewIdleTimeoutCheck()
	if err := check.Fix(&CheckContext{TownRoot: townRoot}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: gt\n") || !strings.Contains(got, "issue-prefix: gt\n") {
		t.Fatalf("rig prefix was destroyed by --fix: %q", got)
	}
	if !strings.Contains(got, "dolt.idle-timeout: \"0\"\n") {
		t.Fatalf("idle-timeout not set: %q", got)
	}
}

// TestIdleTimeoutCheckFix_MissingConfigGetsNoBlankPrefix checks that creating a
// config.yaml from scratch never writes a blank prefix line, since an empty
// prefix is the armed state for the Dolt routing fallback.
func TestIdleTimeoutCheckFix_MissingConfigGetsNoBlankPrefix(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	rigBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}

	check := NewIdleTimeoutCheck()
	if err := check.Fix(&CheckContext{TownRoot: townRoot}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rigBeads, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "prefix: \n") || strings.Contains(got, "prefix:\n") {
		t.Fatalf("blank prefix written: %q", got)
	}
	if !strings.Contains(got, "dolt.idle-timeout: \"0\"\n") {
		t.Fatalf("idle-timeout not set: %q", got)
	}
}

// TestIdleTimeoutCheckFix_UsesMetadataPrefix checks the derived-prefix path:
// when config.yaml is missing but metadata.json names the prefix, the created
// config.yaml carries that prefix rather than nothing.
func TestIdleTimeoutCheckFix_UsesMetadataPrefix(t *testing.T) {
	townRoot := t.TempDir()

	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := `{"prefix":"gt-","path":"gastown"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	rigBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","issue_prefix":"gt"}`
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	check := NewIdleTimeoutCheck()
	if err := check.Fix(&CheckContext{TownRoot: townRoot}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rigBeads, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: gt\n") || !strings.Contains(got, "issue-prefix: gt\n") {
		t.Fatalf("metadata prefix not used: %q", got)
	}
	if !strings.Contains(got, "dolt.idle-timeout: \"0\"\n") {
		t.Fatalf("idle-timeout not set: %q", got)
	}
}
