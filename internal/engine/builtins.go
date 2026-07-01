package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/factory"
)

// BuiltinActions returns the registry of native actions shipped with the
// engine. Callers may add more via WithActions.
func BuiltinActions() map[string]ActionFunc {
	return map[string]ActionFunc{
		"factory_extract_features": actionExtractFeatures,
		"format_fast_batch_prompt": actionFastBatchPrompt,
	}
}

// actionExtractFeatures parses a markdown features file into a list of feature
// maps ({id,title,plan,branch_name,description}). It uses the existing
// factory.ParseFeatures heading-splitter so the engine does not reinvent
// queue construction.
func actionExtractFeatures(_ context.Context, inputs map[string]any) (any, error) {
	path, _ := inputs["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("factory_extract_features: missing `path`")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read features %s: %w", path, err)
	}
	feats := factory.ParseFeatures(string(raw))
	out := make([]any, 0, len(feats))
	for _, f := range feats {
		out = append(out, map[string]any{
			"id":          f.ID,
			"title":       f.Title,
			"plan":        f.Plan,
			"branch_name": f.Branch,
			"description": f.Plan,
		})
	}
	return out, nil
}

// actionFastBatchPrompt concatenates a list of task maps into a single
// description suitable for one batched coder call.
func actionFastBatchPrompt(_ context.Context, inputs map[string]any) (any, error) {
	tasks, ok := inputs["tasks"]
	if !ok {
		return nil, fmt.Errorf("format_fast_batch_prompt: missing `tasks`")
	}
	seq, err := toSlice(tasks)
	if err != nil {
		return nil, fmt.Errorf("format_fast_batch_prompt: %w", err)
	}
	var b strings.Builder
	for i, t := range seq {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		title := stringFrom(m, "title")
		desc := stringFrom(m, "description")
		fmt.Fprintf(&b, "### Task %d: %s\n%s\n\n", i+1, title, desc)
	}
	return map[string]any{"batch_description": b.String()}, nil
}

func stringFrom(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}
