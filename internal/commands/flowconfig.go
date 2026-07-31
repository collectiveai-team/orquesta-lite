package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

// gateConfigKeys is the explicit whitelist of team.json keys reachable from a
// flow through the read-only `config.<key>` namespace. Adding a key here is a
// deliberate act: everything else in team.json stays invisible to flow JSON.
//
// These are argv arrays, not shell strings. `allowShell` is false by policy and
// the scheduler rejects the shell form of command.run/gate.run outright, so the
// string-valued `lint_command` / `full_test_command` keys are intentionally not
// exposed here.
var gateConfigKeys = []string{"lint_argv", "test_argv"}

// loadGateConfig reads the whitelisted keys out of team.json. A missing or
// unreadable team.json yields an empty map: validateConfigReferences is what
// turns that into a clear failure, and only for flows that actually reference
// a config key.
func loadGateConfig(teamPath string) map[string]any {
	config := map[string]any{}
	raw, err := os.ReadFile(teamPath)
	if err != nil {
		return config
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		return config
	}
	for _, key := range gateConfigKeys {
		if value, ok := document[key]; ok {
			config[key] = value
		}
	}
	return config
}

// validateConfigReferences fails fast, before a run starts, on any `config.*`
// reference the project cannot satisfy. A gate whose argv explodes twenty
// minutes into a run is unacceptable, and an empty argv is treated as a failure
// on purpose: a gate that silently no-ops is worse than one that fails loudly.
func validateConfigReferences(ir *flow.IR, config map[string]any) error {
	allowed := make(map[string]bool, len(gateConfigKeys))
	for _, key := range gateConfigKeys {
		allowed[key] = true
	}
	for _, path := range ir.ReferencePaths() {
		if path != "config" && !strings.HasPrefix(path, "config.") {
			continue
		}
		parts := strings.Split(path, ".")
		if len(parts) != 2 {
			return fmt.Errorf("flow references %q: the config namespace is flat, use config.<key>", path)
		}
		key := parts[1]
		if !allowed[key] {
			return fmt.Errorf("flow references config.%s, which is not exposed to flows (available: %s)", key, strings.Join(gateConfigKeys, ", "))
		}
		value, ok := config[key]
		if !ok {
			return fmt.Errorf("flow references config.%s but team.json does not declare %q", key, key)
		}
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("team.json %q must be an array of strings for config.%s", key, key)
		}
		if len(items) == 0 {
			return fmt.Errorf("team.json %q is an empty array; a gate that silently runs nothing is worse than one that fails", key)
		}
		for index, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("team.json %q[%d] must be a string, got %T", key, index, item)
			}
		}
	}
	return nil
}
