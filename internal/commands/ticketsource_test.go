package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// workflow-state@3 es workflow-state@2 más tres campos. Se escribe entero a
// mano porque el validador de schemas del runtime no implementa $ref, y una
// copia a mano es una copia que se desincroniza: este test es lo que lo impide.
func TestWorkflowStateV3IsV2PlusTheSourceFields(t *testing.T) {
	two := readSchemaProperties(t, "workflow-state@2.json")
	three := readSchemaProperties(t, "workflow-state@3.json")

	for name := range two {
		if _, ok := three[name]; !ok {
			t.Errorf("workflow-state@3 perdió la propiedad %q de @2", name)
		}
	}
	for _, added := range []string{"spec_path", "store", "ticket_refs"} {
		if _, ok := three[added]; !ok {
			t.Errorf("workflow-state@3 no declara %q", added)
		}
		if _, ok := two[added]; ok {
			t.Errorf("%q ya estaba en @2; este test asume que es nuevo", added)
		}
	}
	if extra := len(three) - len(two); extra != 3 {
		t.Errorf("@3 agrega %d propiedades sobre @2; se esperaban exactamente 3", extra)
	}
}

// Un campo declarado pero no requerido es un campo que el agente puede omitir,
// y nadie lo notaría hasta que un consumidor lo lea y encuentre nada.
func TestWorkflowStateV3RequiresTheSourceFields(t *testing.T) {
	required := map[string]bool{}
	for _, name := range readSchemaRequired(t, "workflow-state@3.json") {
		required[name] = true
	}
	for _, name := range []string{"spec_path", "store", "ticket_refs"} {
		if !required[name] {
			t.Errorf("workflow-state@3 declara %q pero no lo exige", name)
		}
	}
}

func TestTicketStoreSchemaAllowsExactlyLocalAndGithub(t *testing.T) {
	var schema struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	}
	readSchema(t, "ticket-store@1.json", &schema)
	if schema.Type != "string" {
		t.Errorf("type=%q; se esperaba string", schema.Type)
	}
	want := []string{"local", "github"}
	if len(schema.Enum) != len(want) {
		t.Fatalf("enum=%v; se esperaba %v", schema.Enum, want)
	}
	for i, value := range want {
		if schema.Enum[i] != value {
			t.Errorf("enum[%d]=%q; se esperaba %q", i, schema.Enum[i], value)
		}
	}
}

func readSchema(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(builtinPackTestRoot(), "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func readSchemaProperties(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	readSchema(t, name, &schema)
	return schema.Properties
}

func readSchemaRequired(t *testing.T, name string) []string {
	t.Helper()
	var schema struct {
		Required []string `json:"required"`
	}
	readSchema(t, name, &schema)
	return schema.Required
}
