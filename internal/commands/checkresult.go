package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

// flowCheckResult validates a result file against a pack schema with the same
// validator a flow step applies to it, so an agent can check its JSON before it
// finishes instead of learning about a violation from a corrective retry.
//
// The schema is a path to a schema file, or a ref (`schema:name@version` or
// `name@version`) resolved against the packs installed in the project.
func flowCheckResult(projectDir string, args []string, out io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: orq-lite flow check-result <schema-ref-or-path> <result.json>")
	}
	schemaPath, err := resolveResultSchema(projectDir, args[0])
	if err != nil {
		return err
	}
	file, err := os.Open(schemaPath)
	if err != nil {
		return err
	}
	defer file.Close()
	schema, err := flow.DecodeSchema(file)
	if err != nil {
		return fmt.Errorf("schema %s: %w", schemaPath, err)
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	if err := schema.ValidateJSON(raw); err != nil {
		return fmt.Errorf("%s does not satisfy %s: %w", args[1], args[0], err)
	}
	_, err = fmt.Fprintf(out, "valid %s against %s\n", args[1], args[0])
	return err
}

func resolveResultSchema(projectDir, target string) (string, error) {
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return target, nil
	}
	name := strings.TrimPrefix(target, "schema:")
	if !strings.Contains(name, "@") || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("schema must be a file path or a schema:name@version ref, got %q", target)
	}
	matches, err := filepath.Glob(filepath.Join(projectDir, ".orquestalite", "packs", "*", "*", "schemas", name+".json"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("schema %s is not in any installed pack under .orquestalite/packs", name)
	}
	// A schema ref names an immutable contract, so any installed copy will do;
	// sorting only makes the choice deterministic.
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}
