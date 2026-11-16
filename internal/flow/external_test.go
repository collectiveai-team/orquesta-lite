package flow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func TestExternalCompilationValidatesContractsAndPinsDeliverableSchema(t *testing.T) {
	for _, invalid := range []string{"", "employee", "timeout", "route", "schema", "unknown"} {
		t.Run(invalid, func(t *testing.T) {
			with := map[string]any{"adapter": "paperclip", "employee": "alice", "instructions": "work", "completionCondition": "done with evidence", "timeoutSeconds": 3600, "outputSchema": "schema:deliverable@1"}
			switch invalid {
			case "employee":
				delete(with, "employee")
			case "timeout":
				with["timeoutSeconds"] = 0
			case "route":
				with["routes"] = map[string]string{"failed": "success"}
			case "schema":
				with["outputSchema"] = map[string]string{"$ref": "inputs.schema"}
			case "unknown":
				with["employe"] = "typo"
			}
			raw, _ := json.Marshal(map[string]any{"apiVersion": APIVersionV2, "kind": "Flow", "metadata": map[string]string{"name": "external", "version": "1"}, "steps": []any{map[string]any{"id": "work", "uses": "activity:external.dispatch@1", "with": with}}})
			doc, err := Decode(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			catalog := NewMemoryCatalog()
			catalog.Activities["activity:external.dispatch@1"] = activity.Spec{Name: "external.dispatch", Version: "1", Effect: activity.EffectAtMostOnce}
			catalog.Schemas["schema:deliverable@1"] = &Schema{Type: Types{"object"}}
			ir, diags := Compile(doc, catalog)
			if invalid != "" {
				if !diags.HasErrors() {
					t.Fatalf("invalid %s accepted", invalid)
				}
				return
			}
			if diags.HasErrors() || ir.Schemas["schema:deliverable@1"] == nil || ir.Resources["schema:deliverable@1"] == "" {
				t.Fatalf("schema not pinned: %+v %+v", ir, diags)
			}
		})
	}
}
