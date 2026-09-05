package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConfigYAMLIfMissing_DoesNotOverwriteExisting(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: keep\nissue-prefix: keep\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAMLIfMissing(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAMLIfMissing: %v", err)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(after) != original {
		t.Fatalf("config.yaml changed:\n got: %q\nwant: %q", string(after), original)
	}
}

func TestEnsureConfigYAMLFromMetadataIfMissing_UsesMetadataPrefix(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq","issue_prefix":"foo"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	if err := EnsureConfigYAMLFromMetadataIfMissing(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAMLFromMetadataIfMissing: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: foo\n") {
		t.Fatalf("config.yaml missing metadata prefix: %q", got)
	}
	if !strings.Contains(got, "issue-prefix: foo\n") {
		t.Fatalf("config.yaml missing metadata issue-prefix: %q", got)
	}
	if !strings.Contains(got, "export.auto: \"false\"\n") {
		t.Fatalf("config.yaml missing export.auto default: %q", got)
	}
}

func TestConfigDefaultsFromMetadata_FallsBackToDoltDatabase(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq-custom"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	prefix := ConfigDefaultsFromMetadata(beadsDir, "hq")
	if prefix != "hq-custom" {
		t.Fatalf("prefix = %q, want %q", prefix, "hq-custom")
	}
}

func TestConfigDefaultsFromMetadata_StripsLegacyBeadsPrefixFromDoltDatabase(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	prefix := ConfigDefaultsFromMetadata(beadsDir, "fallback")
	if prefix != "hq" {
		t.Fatalf("prefix = %q, want %q", prefix, "hq")
	}
}

func TestEnsureConfigYAMLFromMetadataIfMissing_StripsLegacyBeadsPrefixFromDoltDatabase(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"beads_hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	if err := EnsureConfigYAMLFromMetadataIfMissing(beadsDir, "fallback"); err != nil {
		t.Fatalf("EnsureConfigYAMLFromMetadataIfMissing: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: hq\n") {
		t.Fatalf("config.yaml missing normalized prefix: %q", got)
	}
	if !strings.Contains(got, "issue-prefix: hq\n") {
		t.Fatalf("config.yaml missing normalized issue-prefix: %q", got)
	}
}

func TestEnsureConfigYAML_DisablesAutoExport(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: old\nissue-prefix: old\ndolt.idle-timeout: \"30\"\nexport.auto: true\nsync.mode: dolt-native\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "gt"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"prefix: gt\n",
		"issue-prefix: gt\n",
		"dolt.idle-timeout: \"0\"\n",
		"export.auto: \"false\"\n",
		"sync.mode: dolt-native\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config.yaml missing %q after repair:\n%s", want, got)
		}
	}
}

func TestEnsureConfigYAMLValue(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: hq\ntypes.custom: old\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAMLValue(beadsDir, "types.custom", "agent,role"); err != nil {
		t.Fatalf("EnsureConfigYAMLValue replace: %v", err)
	}
	if err := EnsureConfigYAMLValue(beadsDir, "allowed_prefixes", "hq,hq-cv"); err != nil {
		t.Fatalf("EnsureConfigYAMLValue append: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"prefix: hq\n",
		"types.custom: agent,role\n",
		"allowed_prefixes: hq,hq-cv\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config.yaml missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "types.custom: old") {
		t.Fatalf("config.yaml kept stale value:\n%s", got)
	}
}

func TestConfigYAMLDisablesAutoExport(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"double quoted false", "export.auto: \"false\"\n", true},
		{"single quoted false", "export.auto: 'false'\n", true},
		{"bare false", "export.auto: false\n", true},
		{"true", "export.auto: true\n", false},
		{"missing", "prefix: hq\n", false},
		{"comment only", "# export.auto: false\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConfigYAMLDisablesAutoExport(tt.content); got != tt.want {
				t.Fatalf("ConfigYAMLDisablesAutoExport() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureConfigYAML_PinsRoutingModeExplicit(t *testing.T) {
	beadsDir := t.TempDir()

	if err := EnsureConfigYAML(beadsDir, "gt"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "routing.mode: \"explicit\"\n") {
		t.Fatalf("new config.yaml missing routing.mode default: %q", string(data))
	}
}

func TestEnsureConfigYAML_AddsRoutingModeToExistingConfig(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: gt\nissue-prefix: gt\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "gt"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "routing.mode: \"explicit\"\n") {
		t.Fatalf("existing config.yaml not repaired: %q", string(data))
	}
}

func TestEnsureConfigYAML_RewritesNonExplicitRoutingMode(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: gt\nrouting.mode: auto\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "gt"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "routing.mode: auto") {
		t.Fatalf("auto routing mode survived: %q", got)
	}
	if strings.Count(got, "routing.mode:") != 1 {
		t.Fatalf("expected exactly one routing.mode line: %q", got)
	}
}

func TestEnsureConfigYAML_LeavesNestedRoutingBlockAlone(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: hq\nissue-prefix: hq\nrouting:\n    mode: explicit\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "routing.mode:") {
		t.Fatalf("flat routing.mode duplicated a nested routing block: %q", got)
	}
	if !strings.Contains(got, "routing:\n    mode: explicit\n") {
		t.Fatalf("nested routing block was disturbed: %q", got)
	}
}

func TestEnsureConfigYAML_EmptyPrefixLeavesPrefixLinesIntact(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: gt\nissue-prefix: gt\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	// This is the 'gt doctor --fix' idle-timeout call shape: it wants the
	// non-prefix defaults and must not touch the routing prefixes (gt-7pn).
	if err := EnsureConfigYAML(beadsDir, ""); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: gt\n") || !strings.Contains(got, "issue-prefix: gt\n") {
		t.Fatalf("prefix lines were rewritten: %q", got)
	}
	if strings.Contains(got, "prefix: \n") || strings.Contains(got, "prefix:\n") {
		t.Fatalf("config.yaml has a blank prefix line: %q", got)
	}
	if !strings.Contains(got, "dolt.idle-timeout: \"0\"\n") {
		t.Fatalf("config.yaml missing idle-timeout default: %q", got)
	}
}

func TestEnsureConfigYAML_EmptyPrefixDoesNotAddBlankPrefixLines(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("export.auto: \"false\"\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, ""); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "prefix:") {
		t.Fatalf("empty prefix was written into config.yaml: %q", got)
	}
	if !strings.Contains(got, "dolt.idle-timeout: \"0\"\n") {
		t.Fatalf("config.yaml missing idle-timeout default: %q", got)
	}
}

func TestEnsureConfigYAML_EmptyPrefixCreatesConfigWithoutPrefixKeys(t *testing.T) {
	beadsDir := t.TempDir()

	if err := EnsureConfigYAML(beadsDir, ""); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "prefix:") {
		t.Fatalf("new config.yaml has a blank prefix line: %q", got)
	}
	if !strings.Contains(got, "dolt.idle-timeout: \"0\"\n") || !strings.Contains(got, "export.auto: \"false\"\n") {
		t.Fatalf("new config.yaml missing defaults: %q", got)
	}
}

func TestEnsureConfigYAML_NonEmptyPrefixStillWritesPrefixKeys(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: old\nissue-prefix: old\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := EnsureConfigYAML(beadsDir, "gt"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: gt\n") || !strings.Contains(got, "issue-prefix: gt\n") {
		t.Fatalf("prefix keys not updated: %q", got)
	}
	if strings.Contains(got, "prefix: old") {
		t.Fatalf("stale prefix left behind: %q", got)
	}
}

func TestEnsureConfigYAMLValue_RefusesEmptyPrefixKeys(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	original := "prefix: gt\nissue-prefix: gt\n"
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	for _, key := range []string{"prefix", "issue-prefix"} {
		if err := EnsureConfigYAMLValue(beadsDir, key, ""); err == nil {
			t.Fatalf("EnsureConfigYAMLValue(%q, \"\") = nil, want error", key)
		}
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(after) != original {
		t.Fatalf("config.yaml changed:\n got: %q\nwant: %q", string(after), original)
	}
}
