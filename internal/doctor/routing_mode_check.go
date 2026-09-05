package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/gastown/internal/beads"
)

// RoutingModeCheck detects beads routing config that sends a seat's reads and
// writes to a store outside its own rig.
//
// bd resolves routing from two layers: config.yaml wins, and the beads database
// `config` table is the fallback (see beads cmd/bd/routing_read.go,
// resolveRoutingConfigValue). When routing.mode is "auto" — which is also the
// default when nothing sets it — bd picks the target from routing.maintainer or
// routing.contributor using a git-remote heuristic, and a fork-backed rig always
// resolves as "contributor". routing.default routes in either mode.
//
// The database layer is the dangerous one: `bd config get`/`bd config unset`
// treat every routing.* key as yaml-only, so a routing row stored in the
// database is invisible to both and cannot be removed through them. It stays
// dormant while config.yaml sets routing.mode, and takes over the moment
// config.yaml is regenerated without it — which is exactly how gt-sym recurred
// after a manual repair: 44 gt- beads were created in an off-rig embedded store
// that no other seat could read.
//
// See: https://github.com/steveyegge/beads/issues/1165
type RoutingModeCheck struct {
	FixableCheck
}

// NewRoutingModeCheck creates a new routing mode check.
func NewRoutingModeCheck() *RoutingModeCheck {
	return &RoutingModeCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "routing-mode",
				CheckDescription: "Check beads routing keeps each seat on its own rig store",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// routingKeys are the config keys bd consults when resolving a routing target.
// Mirrors routingConfigKeys in beads cmd/bd/routing_read.go.
var routingKeys = []string{
	"routing.mode",
	"routing.contributor",
	"routing.default",
	"routing.maintainer",
	"contributor.auto_route",
	"contributor.planning_repo",
}

// routingSettings holds the two config layers bd consults, in precedence order.
type routingSettings struct {
	yaml map[string]string // config.yaml — wins
	db   map[string]string // beads database config table — fallback
}

// effective resolves a key the way bd does: config.yaml first, database second.
func (s routingSettings) effective(key string) string {
	if v := strings.TrimSpace(s.yaml[key]); v != "" {
		return v
	}
	return strings.TrimSpace(s.db[key])
}

// source names the layer an effective value came from, for reporting.
func (s routingSettings) source(key string) string {
	if strings.TrimSpace(s.yaml[key]) != "" {
		return "config.yaml"
	}
	return "beads database"
}

// mode returns the effective routing mode, applying bd's defaults: an unset
// mode behaves as "auto", and contributor.auto_route=true also means "auto".
func (s routingSettings) mode() string {
	mode := s.effective("routing.mode")
	if mode == "" && s.effective("contributor.auto_route") == "true" {
		mode = "auto"
	}
	if mode == "" {
		mode = "auto"
	}
	return mode
}

// offRigTargets lists the routing values that would send this seat's traffic to
// another store, each rendered as "key=value (layer)". Empty means the seat
// stays on its own rig.
//
// The doctor cannot tell whether bd will resolve a seat as maintainer or
// contributor — that depends on git remotes — so in auto mode any role repo
// counts as a live route.
func (s routingSettings) offRigTargets() []string {
	keys := []string{"routing.default"}
	if s.mode() == "auto" {
		keys = append(keys, "routing.maintainer", "routing.contributor", "contributor.planning_repo")
	}

	var targets []string
	for _, key := range keys {
		value := s.effective(key)
		if value == "" || value == "." {
			continue
		}
		targets = append(targets, fmt.Sprintf("%s=%s (%s)", key, value, s.source(key)))
	}
	return targets
}

// latentDBKeys lists routing keys still stored in the beads database. They are
// shadowed while config.yaml sets routing.mode, and resurface without it.
func (s routingSettings) latentDBKeys() []string {
	var keys []string
	for _, key := range routingKeys {
		if strings.TrimSpace(s.db[key]) != "" {
			keys = append(keys, fmt.Sprintf("%s=%s", key, strings.TrimSpace(s.db[key])))
		}
	}
	return keys
}

// Run checks routing config for the town and every routed rig.
func (c *RoutingModeCheck) Run(ctx *CheckContext) *CheckResult {
	var warnings []*CheckResult

	for _, loc := range routingLocations(ctx) {
		result := c.checkRouting(loc.beadsDir, loc.name)
		switch result.Status {
		case StatusError:
			return result // a live off-rig route outranks everything else
		case StatusWarning:
			warnings = append(warnings, result)
		}
	}

	if len(warnings) > 0 {
		return warnings[0]
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Beads routing keeps every seat on its own rig store",
	}
}

// routingLocation is one beads directory to inspect.
type routingLocation struct {
	beadsDir string
	name     string
}

// routingLocations returns the town beads dir plus the beads dir of every rig
// named in routes.jsonl. Rig routes point at the seat that actually serves the
// prefix (e.g. "gastown/mayor/rig"), which is not the same directory as
// ctx.RigPath().
func routingLocations(ctx *CheckContext) []routingLocation {
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	locations := []routingLocation{{beadsDir: townBeadsDir, name: "town"}}

	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return locations
	}

	seen := map[string]bool{townBeadsDir: true}
	for _, route := range routes {
		if route.Path == "" || route.Path == "." {
			continue
		}
		beadsDir := filepath.Join(ctx.TownRoot, route.Path, ".beads")
		if seen[beadsDir] {
			continue
		}
		seen[beadsDir] = true
		locations = append(locations, routingLocation{
			beadsDir: beadsDir,
			name:     fmt.Sprintf("rig '%s'", strings.SplitN(route.Path, "/", 2)[0]),
		})
	}
	return locations
}

// checkRouting inspects one beads directory.
func (c *RoutingModeCheck) checkRouting(beadsDir, location string) *CheckResult {
	if _, err := os.Stat(beadsDir); err != nil {
		return &CheckResult{Name: c.Name(), Status: StatusOK, Message: fmt.Sprintf("No beads dir at %s", location)}
	}

	settings, err := loadRoutingSettings(beadsDir)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not read routing config at %s: %v", location, err),
		}
	}

	return c.evaluate(settings, location)
}

// evaluate turns resolved settings into a check result. Split out from IO so the
// decision table is testable.
func (c *RoutingModeCheck) evaluate(settings routingSettings, location string) *CheckResult {
	if targets := settings.offRigTargets(); len(targets) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Beads routing sends %s off-rig (mode '%s')", location, settings.mode()),
			Details: append([]string{
				"Reads AND writes from this seat resolve to the routed store, not the rig's own",
				"Beads created there are invisible to every other seat and cannot be dispatched",
				"Active routing values:",
			}, targets...),
			FixHint: "Run 'gt doctor --fix' to pin routing.mode explicit and clear routing rows from the beads database",
		}
	}

	if settings.mode() != "explicit" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("routing.mode is '%s' at %s (should be 'explicit')", settings.mode(), location),
			Details: []string{
				"Auto mode picks a routing target from a git remote heuristic",
				"A fork-backed rig resolves as 'contributor', so any routing.contributor value goes live",
				"See: https://github.com/steveyegge/beads/issues/1165",
			},
			FixHint: "Run 'gt doctor --fix' or 'bd config set routing.mode explicit'",
		}
	}

	if latent := settings.latentDBKeys(); len(latent) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Stale routing rows in the beads database at %s", location),
			Details: append([]string{
				"config.yaml currently outranks them, so routing is correct right now",
				"They go live the moment config.yaml is regenerated without routing.mode (gt-sym)",
				"'bd config get' and 'bd config unset' treat routing.* as yaml-only and cannot see or remove them",
				"Rows:",
			}, latent...),
			FixHint: "Run 'gt doctor --fix' to delete the stale rows",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("routing.mode is explicit at %s", location),
	}
}

// loadRoutingSettings reads both config layers for one beads directory.
func loadRoutingSettings(beadsDir string) (routingSettings, error) {
	yamlValues, err := readYAMLRoutingConfig(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		return routingSettings{}, err
	}
	return routingSettings{yaml: yamlValues, db: readDBRoutingConfig(beadsDir)}, nil
}

// readYAMLRoutingConfig reads routing keys from config.yaml, accepting both the
// flat form ("routing.mode: explicit") and the nested form ("routing:" with an
// indented "mode:"). A missing file is not an error.
func readYAMLRoutingConfig(configPath string) (map[string]string, error) {
	values := map[string]string{}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}

	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", configPath, err)
	}

	flat := map[string]string{}
	flattenYAML("", parsed, flat)
	for _, key := range routingKeys {
		if v, ok := flat[key]; ok {
			values[key] = v
		}
	}
	return values, nil
}

// flattenYAML flattens nested maps into dotted keys, keeping scalar values as
// strings. Keys already written in dotted form pass through unchanged.
func flattenYAML(prefix string, node map[string]interface{}, out map[string]string) {
	for key, raw := range node {
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		if child, ok := raw.(map[string]interface{}); ok {
			flattenYAML(full, child, out)
			continue
		}
		if raw == nil {
			continue
		}
		out[full] = strings.TrimSpace(fmt.Sprintf("%v", raw))
	}
}

// readDBRoutingConfig reads routing keys from the beads database config table.
// `bd config list --json` reports database-stored config only, which is exactly
// the fallback layer. An unreachable database yields no rows rather than an
// error: a down Dolt server is another check's problem.
func readDBRoutingConfig(beadsDir string) map[string]string {
	values := map[string]string{}

	cmd := exec.Command("bd", "config", "list", "--json")
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	out, err := cmd.Output()
	if err != nil {
		return values
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return values
	}
	for _, key := range routingKeys {
		if raw, ok := parsed[key]; ok && raw != nil {
			values[key] = strings.TrimSpace(fmt.Sprintf("%v", raw))
		}
	}
	return values
}

// Fix pins routing.mode to explicit in config.yaml and deletes routing rows from
// the beads database. Both halves are required: config.yaml alone is what the
// 2026-09-01 repair did, and it regressed when config.yaml was regenerated.
func (c *RoutingModeCheck) Fix(ctx *CheckContext) error {
	for _, loc := range routingLocations(ctx) {
		if _, err := os.Stat(loc.beadsDir); err != nil {
			continue
		}
		// Only write the flat key when config.yaml does not already resolve to
		// explicit; a config.yaml using the nested "routing:" form would
		// otherwise end up with two definitions of one setting.
		yamlValues, err := readYAMLRoutingConfig(filepath.Join(loc.beadsDir, "config.yaml"))
		if err != nil {
			return fmt.Errorf("reading config.yaml at %s: %w", loc.name, err)
		}
		if strings.TrimSpace(yamlValues["routing.mode"]) != "explicit" {
			if err := beads.EnsureConfigYAMLValue(loc.beadsDir, "routing.mode", "\"explicit\""); err != nil {
				return fmt.Errorf("pinning routing.mode at %s: %w", loc.name, err)
			}
		}
		if err := clearDBRoutingConfig(loc.beadsDir); err != nil {
			return fmt.Errorf("clearing routing rows at %s: %w", loc.name, err)
		}
	}
	return nil
}

// clearDBRoutingConfig deletes routing rows from the beads database config
// table. `bd config unset` cannot do this — it treats routing.* as yaml-only —
// so the delete goes through raw SQL. Nothing to delete is not an error.
func clearDBRoutingConfig(beadsDir string) error {
	stored := readDBRoutingConfig(beadsDir)
	if len(stored) == 0 {
		return nil
	}

	keys := make([]string, 0, len(stored))
	for key := range stored {
		keys = append(keys, "'"+key+"'")
	}
	sort.Strings(keys)

	query := fmt.Sprintf("DELETE FROM config WHERE `key` IN (%s)", strings.Join(keys, ", "))
	cmd := exec.Command("bd", "sql", query)
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd sql failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
