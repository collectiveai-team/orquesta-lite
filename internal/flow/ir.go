package flow

import (
	"encoding/json"
	"sort"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type IR struct {
	APIVersion string                     `json:"apiVersion"`
	Kind       Kind                       `json:"kind"`
	Metadata   Metadata                   `json:"metadata"`
	Inputs     map[string]InputSpec       `json:"inputs,omitempty"`
	Steps      []IRStep                   `json:"steps"`
	Outputs    map[string]Value           `json:"outputs,omitempty"`
	Resources  map[string]Digest          `json:"resources"`
	Schemas    map[string]*Schema         `json:"schemas,omitempty"`
	Policies   map[string]json.RawMessage `json:"policies,omitempty"`
	Pack       *PackSnapshot              `json:"pack,omitempty"`
	Digest     Digest                     `json:"-"`
}

type IRStep struct {
	ID         string           `json:"id"`
	Uses       ResourceRef      `json:"uses"`
	With       map[string]Value `json:"with,omitempty"`
	If         string           `json:"if,omitempty"`
	Foreach    *ForeachSpec     `json:"foreach,omitempty"`
	While      *WhileSpec       `json:"while,omitempty"`
	Retry      *ResourceRef     `json:"retry,omitempty"`
	Activity   *activity.Spec   `json:"activity,omitempty"`
	Subflow    *IR              `json:"subflow,omitempty"`
	OnError    *IRHandler       `json:"onError,omitempty"`
	OnCancel   *IRHandler       `json:"onCancel,omitempty"`
	Compensate *IRHandler       `json:"compensate,omitempty"`
}

type IRHandler struct {
	Uses     ResourceRef      `json:"uses"`
	With     map[string]Value `json:"with,omitempty"`
	Activity *activity.Spec   `json:"activity,omitempty"`
	Subflow  *IR              `json:"subflow,omitempty"`
}

// ReferencePaths returns every structured reference path the compiled IR uses,
// including nested subflows and handlers. It exists so a host can validate a
// whole namespace (for example `config.*`) before a run starts instead of
// discovering a broken reference mid-flight.
//
// "Every" includes the paths read by `if` and `while.condition`. Those are
// expression strings rather than Values, so walking the value tree cannot see
// them — and a namespace check that skipped them would leave the exact hole it
// was written to close, since a gate is at least as likely to be guarded by a
// config reference as to be parameterised by one.
func (ir *IR) ReferencePaths() []string {
	seen := map[string]bool{}
	var paths []string
	record := func(ref string) {
		if !seen[ref] {
			seen[ref] = true
			paths = append(paths, ref)
		}
	}
	visit := func(value Value) {
		_ = walkRefs(value, func(ref string) error {
			record(ref)
			return nil
		})
	}
	visitExpr := func(expression string) {
		if expression == "" {
			return
		}
		for _, ref := range ExprReferences(expression) {
			record(ref)
		}
	}
	var walk func(*IR)
	walk = func(current *IR) {
		if current == nil {
			return
		}
		for _, step := range current.Steps {
			for _, value := range step.With {
				visit(value)
			}
			visitExpr(step.If)
			if step.Foreach != nil {
				visit(step.Foreach.Items)
				if step.Foreach.IsolationKey != nil {
					visit(*step.Foreach.IsolationKey)
				}
			}
			if step.While != nil {
				visit(step.While.Initial)
				visit(step.While.MaxIterations)
				visitExpr(step.While.Condition)
			}
			for _, handler := range []*IRHandler{step.OnError, step.OnCancel, step.Compensate} {
				if handler == nil {
					continue
				}
				for _, value := range handler.With {
					visit(value)
				}
				walk(handler.Subflow)
			}
			walk(step.Subflow)
		}
		for _, value := range current.Outputs {
			visit(value)
		}
	}
	walk(ir)
	sort.Strings(paths)
	return paths
}
