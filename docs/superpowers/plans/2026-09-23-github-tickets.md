# Tickets en GitHub como fuente opcional — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que un run de orq-lite pueda tomar su spec de un issue de GitHub y sus tickets de issues de GitHub, con la elección declarada en el pack, sin romper el camino local.

**Architecture:** un subflow nuevo, `tickets-source@1`, concentra tres trabajos —desambiguar de dónde sale la spec, resolver los tickets (leerlos de GitHub o planearlos), y verificar contra `gh` que existen— y devuelve un `workflow-state@3` que incluye `spec_path`. Los cinco flows que hoy tienen un step `plan_tickets` pasan a invocar ese subflow, y los consumidores aguas abajo leen `spec_path` de su salida en vez del input `features_path`. El flow nunca inspecciona la forma de un string: discrimina por vacío/no-vacío, que es lo único que el lenguaje de `if` soporta.

**Tech Stack:** Go 1.x (runtime, `internal/commands`, `internal/doctor`), JSON de pack v2 (`orq.dev/v2`), `gh` CLI, tests con `go test` sobre `installFixturePack` + `FlowCLI`.

**Spec:** `docs/superpowers/specs/2026-09-16-github-tickets-design.md`

## Global Constraints

- **Rama base:** este plan asume el árbol posterior al PR #66, o sea el pack en
  `packs/development/pack/`. La rama de trabajo es
  `lionelchamorro/github-tickets-impl`, creada sobre
  `lionelchamorro/split-builtin-pack-from-examples`.
- **Después de CUALQUIER edición dentro de `packs/development/pack/`**, correr
  `python3 packs/regen-digests.py`. `flow validate` falla con un manifiesto
  viejo, y `governedpack_test.go` es un gate de eso.
- **`packs/development/pack-v5/` no se toca nunca.** Es la copia congelada que
  mantiene resolviendo los refs pinneados a `development@5`.
- **`schema:path@1` es `{"type":"string","minLength":1}`**: no puede contener
  `""`. Todo input que admita vacío usa `schema:text@1`.
- **El lenguaje de `if`** (`internal/flow/expr.go`) soporta `&& || == != >= <= > <`,
  referencias y literales. No hay `startsWith`, `contains` ni `matches`.
- **El validador de schemas** (`internal/flow/schema.go`) es un subset a mano
  sin `$ref`, `pattern` ni `oneOf`. Los schemas nuevos se escriben inline.
- **`command.run@1` con `shell` está denegado** por `development@3` (no declara
  `allowShell`). Todo comando se escribe como `argv`.
- Correr los tests como los corre CI: `go test ./... -count=1 -race`.

---

## Desviación del spec que necesita tu visto bueno

El spec dice que el `replan` dentro de `develop-ticket@1` también usa
`tickets-source@1`. Este plan **sí lo hace** (Task 4), y para lograrlo el
subflow declara defaults literales para los inputs de passthrough
(`state`, `implementation`, `verification`) — un objeto válido mínimo por cada
uno, declarado una sola vez en el subflow, no replicado en los cinco flows.

El spec también lista un step `fetch_issue_spec` (`command.run` con
`gh issue view`, condicionado a `spec_issue != ""`) dentro del subflow. Este
plan **no lo incluye**: el agente tiene que escribir `.orquestalite/spec.md` de
todos modos, así que hacer que además lo traiga evita un step redundante cuyo
único aporte sería pasarle por `context` un cuerpo que igual va a reescribir.
Si preferís que el fetch sea determinístico y del runtime en vez del agente,
decilo y lo agrego como step 3 del subflow — el costo es un `context` más y un
`gh` más por run.

Lo que **queda afuera de este plan y pasa a fase 2**, tal como el spec ya lo
declara fuera de alcance: cerrar el issue cuando el ticket se completa y
comentar el resultado de la verificación. Un `advance` en modo github relee y
actualiza el estado, pero no escribe en GitHub.

---

## Estructura de archivos

| Archivo | Responsabilidad |
|---|---|
| `packs/development/pack/schemas/ticket-store@1.json` | el enum `local`\|`github`, nada más |
| `packs/development/pack/schemas/workflow-state@3.json` | el estado del plan, ahora cargando también de dónde salió la spec y qué issues lo representan |
| `packs/development/pack/subflows/tickets-source@1.json` | el único lugar que sabe que GitHub existe |
| `packs/development/pack/prompts/ticket-planner.md` | sección `## Ticket store`: qué hacer con `gh`, y el contrato del ancla |
| `packs/development/pack/flows/*.json` | declaran los inputs y delegan; no ramifican |
| `internal/commands/ticketsource_test.go` | e2e sobre pack fixture + tests estructurales del subflow real |
| `internal/doctor/doctor.go` | el check de `gh` |
| `cmd/orq-lite/main.go`, `internal/commands/aliases.go` | los flags de CLI |

---

### Task 1: Los dos schemas nuevos

**Files:**
- Create: `packs/development/pack/schemas/ticket-store@1.json`
- Create: `packs/development/pack/schemas/workflow-state@3.json`
- Create: `internal/commands/ticketsource_test.go`
- Modify: `packs/development/pack/pack.json` (vía script)

**Interfaces:**
- Produces: `schema:ticket-store@1` (string, enum `local`/`github`) y
  `schema:workflow-state@3` (todo `workflow-state@2` más `spec_path` string
  minLength 1, `store` enum, `ticket_refs` array de string; los tres requeridos).
  Task 2 y Task 4 los referencian por esos nombres exactos.

- [ ] **Step 1: Escribir el test que falla**

Crear `internal/commands/ticketsource_test.go`:

```go
package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// workflow-state@3 es workflow-state@2 más tres campos. Se escribe a mano
// porque el validador no tiene $ref, y una copia a mano es una copia que se
// desincroniza: este test es lo que lo impide.
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
// y el runtime no lo notaría hasta que alguien lo lea y encuentre nil.
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
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/commands/ -run 'TestWorkflowStateV3|TestTicketStore' -count=1 -v`
Expected: FAIL — `no such file or directory` sobre `workflow-state@3.json`.

- [ ] **Step 3: Crear `ticket-store@1.json`**

```json
{"type": "string", "enum": ["local", "github"]}
```

- [ ] **Step 4: Crear `workflow-state@3.json`**

Copiar `workflow-state@2.json` entero y agregarle las tres propiedades y los
tres `required`. El archivo completo:

```json
{
  "type": "object",
  "properties": {
    "status": {"type": "string", "enum": ["active", "complete"]},
    "revision": {"type": "number"},
    "summary": {"type": "string", "minLength": 1},
    "iteration_budget": {"type": "integer", "minimum": 1, "maximum": 200},
    "spec_path": {"type": "string", "minLength": 1},
    "store": {"type": "string", "enum": ["local", "github"]},
    "ticket_refs": {"type": "array", "items": {"type": "string"}},
    "next_ticket": {
      "type": ["object", "null"],
      "properties": {
        "id": {"type": "string", "minLength": 1},
        "title": {"type": "string", "minLength": 1},
        "objective": {"type": "string", "minLength": 1},
        "acceptance_criteria": {
          "type": "array",
          "items": {"type": "string", "minLength": 1},
          "minItems": 1
        },
        "dependencies": {"type": "array", "items": {"type": "string"}},
        "files_hint": {"type": "array", "items": {"type": "string"}}
      },
      "required": ["id", "title", "objective", "acceptance_criteria", "dependencies", "files_hint"],
      "additionalProperties": false
    },
    "pending": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "minLength": 1},
          "title": {"type": "string", "minLength": 1},
          "objective": {"type": "string", "minLength": 1},
          "acceptance_criteria": {
            "type": "array",
            "items": {"type": "string", "minLength": 1},
            "minItems": 1
          },
          "dependencies": {"type": "array", "items": {"type": "string"}},
          "files_hint": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["id", "title", "objective", "acceptance_criteria", "dependencies", "files_hint"],
        "additionalProperties": false
      }
    },
    "completed": {"type": "array", "items": {"type": "string"}},
    "blocked": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {"type": "string"}},
    "history": {"type": "array", "items": {"type": ["string", "object"]}}
  },
  "required": ["status", "revision", "summary", "iteration_budget", "spec_path", "store", "ticket_refs", "next_ticket", "pending", "completed", "blocked", "risks", "history"],
  "additionalProperties": false
}
```

- [ ] **Step 5: Regenerar digests y correr los tests**

```bash
python3 packs/regen-digests.py
go test ./internal/commands/ -run 'TestWorkflowStateV3|TestTicketStore' -count=1 -v
```
Expected: PASS los tres.

- [ ] **Step 6: Correr la suite entera**

Run: `go test ./... -count=1`
Expected: PASS. Todavía nadie referencia `@3`, así que nada más cambia.

- [ ] **Step 7: Commit**

```bash
git add packs/development/pack/schemas/ticket-store@1.json \
        packs/development/pack/schemas/workflow-state@3.json \
        packs/development/pack/pack.json \
        internal/commands/ticketsource_test.go
git commit -m "feat(pack): schemas ticket-store@1 y workflow-state@3

workflow-state@3 es @2 más spec_path, store y ticket_refs, los tres
requeridos. Se escribe entero en vez de referenciar @2 porque el validador
de schemas del runtime no implementa \$ref; el test que compara las
propiedades de @2 y @3 es lo que impide que las dos copias se
desincronicen.

@2 queda intacto: pack-v5 lo usa y los refs pinneados tienen que seguir
resolviendo.

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
```

---

### Task 2: `tickets-source@1`, camino local y los dos tripwires

**Files:**
- Create: `packs/development/pack/subflows/tickets-source@1.json`
- Modify: `internal/commands/ticketsource_test.go`
- Modify: `packs/development/pack/pack.json` (vía script)

**Interfaces:**
- Consumes: `schema:ticket-store@1` y `schema:workflow-state@3` de Task 1.
- Produces: el subflow `tickets-source@1` con inputs `spec_path`, `spec_issue`,
  `features_path`, `ticket_store`, `publish_tickets`, `mode`, `state`,
  `implementation`, `verification`, `triage`, `append`; y outputs `state`
  (`workflow-state@3`) y `spec_path` (string). Task 4 invoca exactamente
  esos nombres.

**Por qué dos tripwires y no un `gate.assert` de exclusión mutua:** `gate.assert@1`
sólo compara igualdad (`internal/activity/builtin/gate_assert.go`). No puede
expresar "exactamente uno de dos". Lo que sí se puede es un step que **sólo
corre cuando la condición mala se cumple** y falla siempre: `if` con la
condición del error, y `value: true, equals: false`. Dos steps, dos mensajes
distintos, cero lógica nueva en el runtime.

**Por qué `spec_path` y `features_path` son inputs distintos:** `features_path`
tiene default `"features.md"` en tres de los cinco flows. Si el tripwire mirara
`features_path`, pasar `spec_issue` sería siempre "ambiguo" porque el default
llenó el otro campo. Con un `spec_path` propio que default ea `""`, "los dos
puestos" es detectable de verdad, y `features_path` queda como el fallback
legacy que se usa sólo cuando los otros dos están vacíos.

- [ ] **Step 1: Escribir los tests que fallan**

Agregar a `internal/commands/ticketsource_test.go`:

```go
// El subflow real no se puede correr en un test: invoca un agente. Lo que sí
// se puede es (a) ejecutar su forma contra un pack fixture con command.run en
// lugar del agente, que es donde viven los bugs de cableado, y (b) atar esa
// forma al archivo real con tests estructurales. Sin (b), el fixture y el
// subflow pueden divergir y los tests seguirían verdes.
const ticketSourceShape = `{
 "apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"probe-source","version":"1"},
 "inputs":{
  "spec_path":{"schema":"schema:text@1","default":""},
  "spec_issue":{"schema":"schema:text@1","default":""},
  "features_path":{"schema":"schema:text@1","default":""}
 },
 "steps":[
  {"id":"spec_is_ambiguous","uses":"activity:gate.assert@1",
   "if":"inputs.spec_path != \"\" && inputs.spec_issue != \"\"",
   "with":{"value":true,"equals":false,
           "message":"spec_path y spec_issue son mutuamente excluyentes: vino una spec local y una de GitHub"}},
  {"id":"spec_is_missing","uses":"activity:gate.assert@1",
   "if":"inputs.spec_path == \"\" && inputs.spec_issue == \"\" && inputs.features_path == \"\"",
   "with":{"value":true,"equals":false,
           "message":"falta la spec: pasá spec_path o spec_issue"}},
  {"id":"resolved","uses":"activity:command.run@1","with":{"argv":["/usr/bin/true"]}}
 ],
 "outputs":{"exit":{"$ref":"steps.resolved.output.exitCode"}}}`

func installTicketSourceProbe(t *testing.T, dir string) {
	t.Helper()
	installFixturePack(t, dir, "probe", "1", map[string]string{
		"flows/probe-source@1.json": ticketSourceShape,
		"schemas/text@1.json":       `{"type": "string"}`,
	})
}

func TestSpecPathAloneRuns(t *testing.T) {
	dir := t.TempDir()
	installTicketSourceProbe(t, dir)
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-source@1", `spec_path="features.md"`}, &out); err != nil {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "status=succeeded") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestSpecIssueAloneRuns(t *testing.T) {
	dir := t.TempDir()
	installTicketSourceProbe(t, dir)
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-source@1", `spec_issue="#123"`}, &out); err != nil {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "status=succeeded") {
		t.Fatalf("out=%s", out.String())
	}
}

// Las dos puestas es error del operador, y tiene que decir cuál es el error.
func TestBothSpecInputsIsAmbiguousAndStopsTheRun(t *testing.T) {
	dir := t.TempDir()
	installTicketSourceProbe(t, dir)
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-source@1", `spec_path="features.md"`, `spec_issue="#123"`}, &out)
	if err == nil {
		t.Fatalf("pasar las dos specs tiene que cortar el run: out=%s", out.String())
	}
	if !strings.Contains(err.Error(), "mutuamente excluyentes") {
		t.Fatalf("el mensaje del gate tiene que llegar al operador: %v", err)
	}
}

// Ninguna puesta es un error distinto y merece un texto distinto.
func TestNoSpecAtAllStopsTheRunWithItsOwnMessage(t *testing.T) {
	dir := t.TempDir()
	installTicketSourceProbe(t, dir)
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-source@1"}, &out)
	if err == nil {
		t.Fatalf("un run sin spec tiene que cortar: out=%s", out.String())
	}
	if !strings.Contains(err.Error(), "falta la spec") {
		t.Fatalf("el mensaje del gate tiene que llegar al operador: %v", err)
	}
}

// Ata el fixture de arriba al subflow real: si alguien cambia una condición en
// tickets-source@1, este test lo ve aunque los e2e sigan verdes sobre el probe.
func TestTicketSourceDeclaresBothTripwiresWithTheSameConditions(t *testing.T) {
	doc := readPackDoc(t, filepath.Join(builtinPackTestRoot(), "subflows", "tickets-source@1.json"))
	conditions := map[string]string{}
	for _, step := range doc.Steps {
		conditions[step.ID] = step.If
	}
	want := map[string]string{
		"spec_is_ambiguous": `inputs.spec_path != "" && inputs.spec_issue != ""`,
		"spec_is_missing":   `inputs.spec_path == "" && inputs.spec_issue == "" && inputs.features_path == ""`,
	}
	for id, expected := range want {
		got, ok := conditions[id]
		if !ok {
			t.Fatalf("tickets-source@1 no declara el step %q", id)
		}
		if got != expected {
			t.Errorf("step %q: if=%q; se esperaba %q", id, got, expected)
		}
	}
}
```

Agregar los imports que faltan al archivo (`bytes`, `context`, `strings`).

Si `packDoc` (declarado en `subflowinputs_test.go`) no expone `If` por step,
agregarle el campo: `If string \`json:"if"\`` dentro de su struct de step.

- [ ] **Step 2: Correr y verificar que falla**

Run: `go test ./internal/commands/ -run 'TestSpec|TestBothSpec|TestNoSpec|TestTicketSourceDeclares' -count=1 -v`
Expected: los cuatro e2e fallan por el pack fixture recién creado o pasan
trivialmente; el estructural falla con "no such file" sobre `tickets-source@1.json`.

- [ ] **Step 3: Crear `tickets-source@1.json`**

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Subflow",
  "metadata": {"name": "tickets-source", "version": "1"},
  "inputs": {
    "spec_path": {"schema": "schema:text@1", "default": ""},
    "spec_issue": {"schema": "schema:text@1", "default": ""},
    "features_path": {"schema": "schema:text@1", "default": ""},
    "ticket_store": {"schema": "schema:ticket-store@1", "default": "local"},
    "publish_tickets": {"schema": "schema:flag@1", "default": false},
    "mode": {"schema": "schema:text@1", "default": "initial"},
    "append": {"schema": "schema:flag@1", "default": false},
    "triage": {"schema": "schema:text@1", "default": ""},
    "state": {
      "schema": "schema:workflow-state@3",
      "default": {
        "status": "active", "revision": 0, "summary": "sin estado previo",
        "iteration_budget": 1, "spec_path": "features.md", "store": "local",
        "ticket_refs": [], "next_ticket": null, "pending": [],
        "completed": [], "blocked": [], "risks": [], "history": []
      }
    },
    "implementation": {
      "schema": "schema:ticket-implementation@1",
      "default": {
        "ticket_id": "none", "complete": false, "summary": "sin implementación previa",
        "files_changed": [], "gates": [], "remaining": []
      }
    },
    "verification": {
      "schema": "schema:ticket-verification@1",
      "default": {
        "ticket_id": "none", "approved": false, "summary": "sin verificación previa",
        "findings": [], "gates": []
      }
    }
  },
  "steps": [
    {
      "id": "spec_is_ambiguous",
      "uses": "activity:gate.assert@1",
      "if": "inputs.spec_path != \"\" && inputs.spec_issue != \"\"",
      "with": {
        "value": true,
        "equals": false,
        "message": "spec_path y spec_issue son mutuamente excluyentes: vino una spec local y una de GitHub"
      }
    },
    {
      "id": "spec_is_missing",
      "uses": "activity:gate.assert@1",
      "if": "inputs.spec_path == \"\" && inputs.spec_issue == \"\" && inputs.features_path == \"\"",
      "with": {
        "value": true,
        "equals": false,
        "message": "falta la spec: pasá spec_path o spec_issue"
      }
    },
    {
      "id": "resolve",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "ticket_planner",
        "outputSchema": "schema:workflow-state@3",
        "vars": {
          "MODE": {"$ref": "inputs.mode"},
          "SPEC_PATH": {"$ref": "inputs.spec_path"},
          "SPEC_ISSUE": {"$ref": "inputs.spec_issue"},
          "FEATURES_PATH": {"$ref": "inputs.features_path"},
          "TICKET_STORE": {"$ref": "inputs.ticket_store"},
          "TRIAGE": {"$ref": "inputs.triage"}
        },
        "context": {
          "PUBLISH_TICKETS": {"$ref": "inputs.publish_tickets"},
          "APPEND": {"$ref": "inputs.append"},
          "CURRENT_STATE": {"$ref": "inputs.state"},
          "IMPLEMENTATION": {"$ref": "inputs.implementation"},
          "VERIFICATION": {"$ref": "inputs.verification"}
        }
      }
    }
  ],
  "outputs": {
    "state": {"$ref": "steps.resolve.output"},
    "spec_path": {"$ref": "steps.resolve.output.spec_path"}
  }
}
```

- [ ] **Step 4: Regenerar digests, validar el subflow y correr los tests**

```bash
python3 packs/regen-digests.py
go run ./cmd/orq-lite flow validate packs/development/pack/subflows/tickets-source@1.json
go test ./internal/commands/ -run 'TestSpec|TestBothSpec|TestNoSpec|TestTicketSourceDeclares' -count=1 -v
```
Expected: `valid tickets-source@1 <digest>` y los cinco tests en PASS.

- [ ] **Step 5: Commit**

```bash
git add packs/development/pack/subflows/tickets-source@1.json \
        packs/development/pack/pack.json internal/commands/ticketsource_test.go
git commit -m "feat(pack): subflow tickets-source@1 con los tripwires de spec

gate.assert@1 sólo compara igualdad, así que 'exactamente uno de dos' no se
puede expresar como un gate. Se expresa como dos steps que sólo corren
cuando su condición de error se cumple y entonces fallan siempre: uno para
las dos specs puestas, otro para ninguna. Cada uno con su mensaje, porque
son dos errores distintos del operador.

spec_path es un input propio y no reusa features_path justamente porque
features_path tiene default en tres flows: con el default puesto, pasar
spec_issue sería siempre ambiguo.

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
```

---

### Task 3: Los steps de GitHub y el commit del ledger

**Files:**
- Modify: `packs/development/pack/subflows/tickets-source@1.json`
- Modify: `internal/commands/ticketsource_test.go`
- Modify: `packs/development/pack/pack.json` (vía script)

**Interfaces:**
- Consumes: el subflow de Task 2.
- Produces: los steps `verify_tickets_exist`, `tickets_are_real` y
  `commit_spec` dentro del mismo subflow. La salida no cambia de forma.

**Nota sobre `verify_tickets_exist`:** verifica **un** issue, el primero de
`ticket_refs`, con `gh issue view <ref>`. Verificar los N requeriría `foreach`,
que este pack nunca ejecutó (ver restricción 3 del spec) y que este plan no
estrena. Un ancla que resuelve prueba que el agente habló de verdad con GitHub,
que es el fraude que el gate tiene que atrapar; verificar los N es una mejora
de fase 2, no un requisito para que el gate sirva.

- [ ] **Step 1: Escribir los tests que fallan**

Agregar a `internal/commands/ticketsource_test.go`:

```go
// writeFakeGH deja un `gh` ejecutable al frente del PATH del test. Es la única
// forma de ejercitar el camino github sin red y sin credenciales.
func writeFakeGH(t *testing.T, dir, script string) string {
	t.Helper()
	binDir := filepath.Join(dir, "fakebin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(binDir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir
}

const ticketGateShape = `{
 "apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"probe-gate","version":"1"},
 "inputs":{"ticket_store":{"schema":"schema:text@1","default":"local"},
           "ref":{"schema":"schema:text@1","default":""}},
 "steps":[
  {"id":"verify_tickets_exist","uses":"activity:command.run@1",
   "if":"inputs.ticket_store == \"github\"",
   "with":{"argv":["gh","issue","view",{"$ref":"inputs.ref"}]}},
  {"id":"tickets_are_real","uses":"activity:gate.assert@1",
   "if":"inputs.ticket_store == \"github\"",
   "with":{"value":{"$ref":"steps.verify_tickets_exist.output.exitCode"},"equals":0,
           "message":"el plan declaró tickets en GitHub que no resuelven"}}
 ]}`

// El gate existe para atrapar a un planner que dice haber creado issues que no
// creó. Un gate que nunca se vio fallar no es un gate.
func TestTicketsAreRealGateStopsTheRunWhenTheIssueDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	writeFakeGH(t, dir, `echo "no such issue" >&2; exit 1`)
	installFixturePack(t, dir, "probe", "1", map[string]string{
		"flows/probe-gate@1.json": ticketGateShape,
		"schemas/text@1.json":     `{"type": "string"}`,
	})
	var out bytes.Buffer
	err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-gate@1", `ticket_store="github"`, `ref="#999"`}, &out)
	if err == nil {
		t.Fatalf("un ticket_ref que no resuelve tiene que cortar el run: out=%s", out.String())
	}
	if !strings.Contains(err.Error(), "no resuelven") {
		t.Fatalf("el mensaje del gate tiene que llegar al operador: %v", err)
	}
}

func TestTicketsAreRealGatePassesWhenTheIssueResolves(t *testing.T) {
	dir := t.TempDir()
	writeFakeGH(t, dir, `exit 0`)
	installFixturePack(t, dir, "probe", "1", map[string]string{
		"flows/probe-gate@1.json": ticketGateShape,
		"schemas/text@1.json":     `{"type": "string"}`,
	})
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-gate@1", `ticket_store="github"`, `ref="#1"`}, &out); err != nil {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

// En modo local ni se materializa el step, así que `gh` puede no existir.
func TestLocalStoreNeverCallsGH(t *testing.T) {
	dir := t.TempDir()
	writeFakeGH(t, dir, `echo "gh no debería haberse llamado" >&2; exit 97`)
	installFixturePack(t, dir, "probe", "1", map[string]string{
		"flows/probe-gate@1.json": ticketGateShape,
		"schemas/text@1.json":     `{"type": "string"}`,
	})
	var out bytes.Buffer
	if err := FlowCLI(context.Background(), dir, []string{"run", "probe/probe-gate@1", `ticket_store="local"`}, &out); err != nil {
		t.Fatalf("el camino local no debe tocar gh: err=%v out=%s", err, out.String())
	}
}

// El step existe para que la sección ## Tickets no viaje dentro del commit del
// primer ticket. Si alguien lo borra, este test lo dice.
func TestTicketSourceCommitsTheSpecItself(t *testing.T) {
	doc := readPackDoc(t, filepath.Join(builtinPackTestRoot(), "subflows", "tickets-source@1.json"))
	for _, step := range doc.Steps {
		if step.ID == "commit_spec" {
			if step.Uses != "activity:git.commit@1" {
				t.Fatalf("commit_spec usa %q; se esperaba activity:git.commit@1", step.Uses)
			}
			if step.If != `inputs.publish_tickets == true` {
				t.Fatalf("commit_spec: if=%q", step.If)
			}
			return
		}
	}
	t.Fatal("tickets-source@1 no declara commit_spec: la sección ## Tickets viajaría dentro del commit del primer ticket")
}
```

- [ ] **Step 2: Correr y verificar que falla**

Run: `go test ./internal/commands/ -run 'TestTicketsAreReal|TestLocalStoreNever|TestTicketSourceCommits' -count=1 -v`
Expected: los de gate pasan (validan la forma en el fixture); el de `commit_spec` FALLA con "no declara commit_spec".

- [ ] **Step 3: Agregar los tres steps al subflow**

En `packs/development/pack/subflows/tickets-source@1.json`, después del step
`resolve`, agregar:

```json
    {
      "id": "verify_tickets_exist",
      "uses": "activity:command.run@1",
      "if": "inputs.ticket_store == \"github\"",
      "with": {
        "argv": ["gh", "issue", "view", {"$ref": "steps.resolve.output.ticket_refs.0"}]
      }
    },
    {
      "id": "tickets_are_real",
      "uses": "activity:gate.assert@1",
      "if": "inputs.ticket_store == \"github\"",
      "with": {
        "value": {"$ref": "steps.verify_tickets_exist.output.exitCode"},
        "equals": 0,
        "message": "el plan declaró tickets en GitHub que no resuelven: el planner dijo haberlos creado y gh no los encuentra"
      }
    },
    {
      "id": "commit_spec",
      "uses": "activity:git.commit@1",
      "with": {
        "enabled": {"$ref": "inputs.publish_tickets"},
        "type": "chore",
        "scope": "spec",
        "subject": {"$ref": "steps.resolve.output.summary"},
        "body": "Registra en la spec los issues que la implementan."
      }
    }
```

**Ojo con `commit_spec`:** usa `enabled` (dato) y no `if` (condición), igual que
`commit_ticket` en `develop-ticket@1`. El comentario de `gitCommitInput.Enabled`
en `internal/activity/builtin/gitcommit.go` explica por qué: un step salteado
resuelve a nil, `&&` no corta, y cualquier step posterior que referencie su
salida mataría el run. Si el test estructural de Step 1 exige `if`, corregir el
test a `enabled` — **verificar cuál de las dos formas usa `commit_ticket` hoy y
copiarla**, no inventar una tercera.

- [ ] **Step 4: Regenerar, validar y correr**

```bash
python3 packs/regen-digests.py
go run ./cmd/orq-lite flow validate packs/development/pack/subflows/tickets-source@1.json
go test ./internal/commands/ -count=1 -race
```
Expected: `valid` y toda la package en PASS.

- [ ] **Step 5: Commit**

```bash
git add packs/development/pack/subflows/tickets-source@1.json \
        packs/development/pack/pack.json internal/commands/ticketsource_test.go
git commit -m "feat(pack): verificación contra gh y commit propio del ledger

tickets_are_real corta el run cuando el planner declara ticket_refs que gh
no encuentra. Es el gate que separa 'creó los issues' de 'dijo que los
creó', y el test lo ejercita fallando, no sólo pasando.

commit_spec commitea la sección ## Tickets en el mismo pase que la escribe.
Sin él viaja dentro del commit del primer ticket, porque git.commit@1 hace
git add -A, y un run caído entre la creación de issues y ese commit deja la
spec sucia sin que nadie la haya tocado.

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
```

---

### Task 4: Reconectar los flows y los consumidores de `features_path`

**Files:**
- Modify: `packs/development/pack/flows/plan-tickets@1.json`
- Modify: `packs/development/pack/flows/task-list@1.json`
- Modify: `packs/development/pack/flows/factory-fast@1.json`
- Modify: `packs/development/pack/flows/factory-governed@2.json`
- Modify: `packs/development/pack/flows/issue-fix@1.json`
- Modify: `packs/development/pack/subflows/develop-ticket@1.json`
- Modify: `packs/development/pack/subflows/integrated-review@1.json`
- Modify: `internal/commands/ticketsource_test.go`

**Interfaces:**
- Consumes: `subflow:tickets-source@1` con los inputs de Task 2.
- Produces: cada flow expone `spec_path`, `spec_issue`, `ticket_store` y
  `publish_tickets`; Task 6 los pasa desde la CLI con esos nombres.

En cada flow, el step `plan_tickets` pasa de `agent.invoke@1` a
`subflow:tickets-source@1`, y todo `{"$ref": "inputs.features_path"}` aguas
abajo pasa a `{"$ref": "steps.plan_tickets.output.spec_path"}`.

`develop-ticket@1` cambia en dos lugares: su input `state` pasa a
`schema:workflow-state@3`, su input `features_path` se reemplaza por
`spec_path`, y su step `update_ticket_plan` pasa a invocar
`subflow:tickets-source@1` con `mode: "advance"`.

- [ ] **Step 1: Escribir el test que falla**

```go
// Ningún flow puede quedarse leyendo features_path directamente: la spec real
// del run es la que tickets-source@1 resolvió, y puede ser un archivo que el
// planner materializó desde un issue.
func TestNoFlowReadsFeaturesPathAfterTheSourceResolvesIt(t *testing.T) {
	packRoot := builtinPackTestRoot()
	for _, dir := range []string{"flows", "subflows"} {
		entries, err := os.ReadDir(filepath.Join(packRoot, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() == "tickets-source@1.json" {
				continue // el único que legítimamente lo recibe
			}
			raw, readErr := os.ReadFile(filepath.Join(packRoot, dir, entry.Name()))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if bytes.Contains(raw, []byte(`"inputs.features_path"`)) {
				t.Errorf("%s/%s todavía lee inputs.features_path; tiene que leer el spec_path que resolvió tickets-source@1", dir, entry.Name())
			}
		}
	}
}

// El input tiene que existir en todos los flows que planean, o pasarlo por CLI
// falla con "unknown input" en vez de hacer lo que el operador pidió.
func TestEveryPlanningFlowDeclaresTheTicketStoreInputs(t *testing.T) {
	packRoot := builtinPackTestRoot()
	for _, name := range []string{"plan-tickets@1.json", "task-list@1.json", "factory-fast@1.json", "factory-governed@2.json", "issue-fix@1.json"} {
		doc := readPackDoc(t, filepath.Join(packRoot, "flows", name))
		for _, input := range []string{"spec_issue", "ticket_store", "publish_tickets"} {
			if _, ok := doc.Inputs[input]; !ok {
				t.Errorf("%s no declara el input %q", name, input)
			}
		}
	}
}
```

Si `packDoc` no expone `Inputs`, agregarle
`Inputs map[string]json.RawMessage \`json:"inputs"\`` en `subflowinputs_test.go`.

- [ ] **Step 2: Correr y verificar que falla**

Run: `go test ./internal/commands/ -run 'TestNoFlowReads|TestEveryPlanningFlow' -count=1 -v`
Expected: FAIL, listando los flows que todavía leen `inputs.features_path` y
los inputs que faltan.

- [ ] **Step 3: Reescribir `plan-tickets@1.json`**

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {"name": "plan-tickets", "version": "1", "policy": "policy:development@3"},
  "inputs": {
    "plan_path": {"schema": "schema:text@1", "default": "features.md"},
    "spec_issue": {"schema": "schema:text@1", "default": ""},
    "ticket_store": {"schema": "schema:ticket-store@1", "default": "local"},
    "publish_tickets": {"schema": "schema:flag@1", "default": false},
    "append": {"schema": "schema:flag@1", "default": false}
  },
  "steps": [
    {
      "id": "plan_tickets",
      "uses": "subflow:tickets-source@1",
      "with": {
        "mode": "initial",
        "spec_path": {"$ref": "inputs.plan_path"},
        "spec_issue": {"$ref": "inputs.spec_issue"},
        "ticket_store": {"$ref": "inputs.ticket_store"},
        "publish_tickets": {"$ref": "inputs.publish_tickets"},
        "append": {"$ref": "inputs.append"}
      }
    }
  ],
  "outputs": {
    "state": {"$ref": "steps.plan_tickets.output.state"},
    "spec_path": {"$ref": "steps.plan_tickets.output.spec_path"}
  }
}
```

`plan_path` pasa de `schema:path@1` a `schema:text@1` porque ahora puede venir
vacío cuando el operador usa `spec_issue`. El default `"features.md"` se
mantiene, así que `orq-lite plan` sin argumento sigue comportándose igual.

**Ojo:** con `plan_path` defaulteado a `"features.md"`, pasar `spec_issue`
dispararía el tripwire de ambigüedad. Por eso la CLI (Task 6) manda
`plan_path=""` cuando el operador pasa `--spec-issue`. Verificar esto al
correr Task 6 y no antes.

- [ ] **Step 4: Aplicar el mismo patrón a los otros cuatro flows**

Para `task-list@1`, `factory-fast@1`, `factory-governed@2` e `issue-fix@1`:

1. Agregar los inputs `spec_issue`, `ticket_store` y `publish_tickets` con los
   mismos schemas y defaults que arriba.
2. Cambiar el input de path (`features_path` / `issue_path`) de
   `schema:path@1` a `schema:text@1`.
3. Reemplazar el step `plan_tickets` (o `plan_fix` en `issue-fix@1`) por
   `"uses": "subflow:tickets-source@1"` con el bloque `with` de arriba,
   mapeando el input de path del flow a `spec_path`.
4. Reemplazar cada `{"$ref": "inputs.features_path"}` por
   `{"$ref": "steps.plan_tickets.output.spec_path"}` — en las llamadas a
   `subflow:integrated-review@1`, `subflow:develop-ticket@1` y
   `subflow:fast-batch@1`.
5. Donde el flow referencia `steps.plan_tickets.output` como estado (el
   `initial` del `while`, los `gate.assert` de plan completo), pasar a
   `steps.plan_tickets.output.state`.

`issue-fix@1` mantiene su step `read_issue` con `cat` y su `intake`: el triage
sigue leyendo el archivo local. Lo que cambia es que `plan_fix` delega en el
subflow.

- [ ] **Step 5: Actualizar `integrated-review@1.json`**

Renombrar su input `features_path` a `spec_path` (mismo schema `schema:text@1`)
y actualizar los cuatro `{"$ref": "inputs.features_path"}` de los roles `qa`,
`adversary`, `critic` y `visual_verifier` a `{"$ref": "inputs.spec_path"}`.

- [ ] **Step 6: Actualizar `develop-ticket@1.json`**

1. Input `features_path` → `spec_path` (`schema:text@1`).
2. Input `state`: `schema:workflow-state@2` → `schema:workflow-state@3`.
3. Los cuatro `{"$ref": "inputs.features_path"}` de los steps
   `implement_ticket`, `verify_ticket` y `repair_commit` → `inputs.spec_path`.
4. El step `update_ticket_plan` pasa de `agent.invoke@1` a:

```json
    {
      "id": "update_ticket_plan",
      "uses": "subflow:tickets-source@1",
      "with": {
        "mode": "advance",
        "spec_path": {"$ref": "inputs.spec_path"},
        "ticket_store": {"$ref": "inputs.ticket_store"},
        "publish_tickets": {"$ref": "inputs.publish_tickets"},
        "state": {"$ref": "inputs.state"},
        "implementation": {"$ref": "steps.implement_ticket.output"},
        "verification": {"$ref": "steps.verify_ticket.output"}
      }
    }
```

5. Agregar a `develop-ticket@1` los inputs `ticket_store` y `publish_tickets`
   (mismos schemas y defaults), y pasarlos desde cada flow que lo invoca.
6. Su output `state` pasa a `{"$ref": "steps.update_ticket_plan.output.state"}`.

- [ ] **Step 7: Regenerar, validar todos los flows y correr la suite**

```bash
python3 packs/regen-digests.py
for f in packs/development/pack/flows/*.json packs/development/pack/subflows/*.json; do
  go run ./cmd/orq-lite flow validate "$f" || echo "FALLÓ: $f"
done
go test ./... -count=1 -race
```
Expected: `valid` por cada archivo, sin líneas `FALLÓ`, y la suite en PASS.
`subflowinputs_test.go` valida los call sites de subflow: si un `with` no
coincide con los inputs declarados, falla ahí.

- [ ] **Step 8: Commit**

```bash
git add packs/development/pack internal/commands/ticketsource_test.go
git commit -m "feat(pack): los flows toman sus tickets de tickets-source@1

Los cinco flows que planeaban con agent.invoke pasan a invocar el subflow,
y todo consumidor aguas abajo lee el spec_path que el subflow resolvió en
vez del input features_path. Esa indirección es lo que hace que una spec que
vino de un issue de GitHub funcione sin tocar qa, adversary, critic ni
visual_verifier: reciben un path como siempre, sólo que ahora puede ser el
archivo que el planner materializó.

develop-ticket@1 pasa a workflow-state@3 y su replan también delega en el
subflow, así que el modo advance relee el frontier desde GitHub.

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
```

---

### Task 5: La sección `## Ticket store` del prompt

**Files:**
- Modify: `packs/development/pack/prompts/ticket-planner.md`
- Modify: `internal/commands/ticketsource_test.go`

**Interfaces:**
- Consumes: las vars `SPEC_PATH`, `SPEC_ISSUE`, `TICKET_STORE` y los context
  `PUBLISH_TICKETS`, `CURRENT_STATE`, `IMPLEMENTATION`, `VERIFICATION` que
  declara el step `resolve` de Task 2.

`TestEveryPromptPlaceholderIsSuppliedByEveryStepThatUsesTheRole`
(`ticketcommit_test.go`) ya exige que todo `{{PLACEHOLDER}}` del prompt lo
provea cada step que usa el rol. Ese test **va a fallar** apenas se agregue un
placeholder nuevo si Task 2 no lo declaró: es el gate de este task, y no hay
que escribirlo.

- [ ] **Step 1: Correr el test existente para ver el estado actual**

Run: `go test ./internal/commands/ -run TestEveryPromptPlaceholder -count=1 -v`
Expected: PASS (todavía no hay placeholders nuevos).

- [ ] **Step 2: Agregar los placeholders y la sección al prompt**

En el encabezado de `prompts/ticket-planner.md`, junto a las líneas de `Mode:` y
`Canonical contract:`, agregar:

```markdown
Spec file: {{SPEC_PATH}}
Spec issue: {{SPEC_ISSUE}}
Ticket store: {{TICKET_STORE}}
Publish tickets: {{PUBLISH_TICKETS}}
```

Y antes de `## iteration_budget`, la sección:

```markdown
## Ticket store

`{{TICKET_STORE}}` dice dónde viven los tickets. Exactamente una de
`{{SPEC_PATH}}` y `{{SPEC_ISSUE}}` viene no vacía — el flow ya lo garantizó, así
que no tenés que decidir cuál es cuál ni desempatar nada.

Con `SPEC_ISSUE` no vacío, leé el issue con
`gh issue view <ref> --json title,body`, escribí su cuerpo a
`.orquestalite/spec.md`, y emitilo como `spec_path`. Con `SPEC_PATH` no vacío,
`spec_path` es ese mismo path: no copies nada.

### `store: "local"`

Como siempre: los tickets viven en el estado, sus ids son `T1..Tn`, y
`ticket_refs` es `[]`.

### `store: "github"`

La spec declara sus tickets. Leé la spec (el archivo, o el cuerpo del issue) y
buscá una sección con este formato:

```markdown
## Tickets

- [ ] #123 — Persistir el objetivo del run
- [x] #124 — Exponer el objetivo en el dashboard
```

- **Si la sección existe**, esos issues **son** el plan. Leélos con
  `gh issue view <n> --json number,title,body,state` y construí el estado desde
  ellos en vez de descomponer de cero. El `id` de cada ticket es el número de
  issue como string (`"123"`), no `T1`. Un issue cerrado va a `completed`.
- **Si la sección no existe y `PUBLISH_TICKETS` es `true`**, descomponé la spec
  como siempre, creá un issue por ticket con
  `gh issue create --title "..." --body "..."`, y escribí la sección de vuelta
  en la spec: si la spec es un archivo, editalo; si es un issue, usá
  `gh issue edit`. El body de cada issue lleva el objetivo y los criterios de
  aceptación del ticket, más una línea `Blocked by:` con los issues de los que
  depende, o "None".
- **Si la sección no existe y `PUBLISH_TICKETS` es `false`**, no inventes nada y
  no planees local por lo bajo: emitì `status: "complete"` con un `summary` que
  diga exactamente que falta la sección `## Tickets` en la spec y que
  `publish_tickets` está en `false`. El operador tiene que enterarse de por qué
  no pasó nada.

`ticket_refs` lleva los números de issue del plan, como strings, empezando por
el del `next_ticket`. El runtime verifica el primero contra `gh` antes de
seguir: si declarás un número que no existe, el run se corta. No declares lo
que no creaste.

En `advance` con `store: "github"`, releé los issues antes de elegir el
frontier: alguien pudo cerrar, editar o agregar uno entre pases, y eso manda
sobre lo que diga el estado que traés.

Emitì siempre `spec_path`, `store` y `ticket_refs`, en los dos modos y en los
dos stores. Son requeridos por el contrato.
```

Las reglas de vertical slice, blocking edges e `iteration_budget` no cambian:
son propiedades del plan, no del lugar donde se guarda.

- [ ] **Step 3: Regenerar digests y correr el test de placeholders**

```bash
python3 packs/regen-digests.py
go test ./internal/commands/ -run TestEveryPromptPlaceholder -count=1 -v
```
Expected: PASS. Si falla nombrando un placeholder, el step `resolve` de
`tickets-source@1` no lo declara — agregarlo ahí, no sacarlo del prompt.

- [ ] **Step 4: Correr la suite**

Run: `go test ./... -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packs/development/pack/prompts/ticket-planner.md packs/development/pack/pack.json
git commit -m "feat(pack): el planner sabe leer y escribir tickets en GitHub

La spec declara sus tickets en una sección ## Tickets, y esa sección es la
única fuente de descubrimiento. Eso hace que buscar los tickets sea la misma
operación sea la spec un features.md o un issue, y que el ancla sobreviva a
que alguien toque labels o milestones.

Sin la sección y con publish_tickets en false el planner para y lo dice, en
vez de planear local por lo bajo: un run que no hizo lo que le pidieron
tiene que decirlo.

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
```

---

### Task 6: CLI y doctor

**Files:**
- Modify: `cmd/orq-lite/main.go`
- Modify: `internal/doctor/doctor.go`
- Create: `internal/commands/ticketsourcecli_test.go`

**Interfaces:**
- Consumes: los inputs de flow de Task 4 (`spec_issue`, `ticket_store`,
  `publish_tickets`) con esos nombres exactos.

- [ ] **Step 1: Escribir el test que falla**

Crear `internal/commands/ticketsourcecli_test.go`:

```go
package commands

import "testing"

// Pasar --spec-issue tiene que vaciar el path, o el default "features.md"
// dispara el tripwire de ambigüedad y el operador recibe un error por haber
// usado el flag correctamente.
func TestSpecIssueClearsTheDefaultPath(t *testing.T) {
	inputs := SpecInputs("plan_path", "features.md", "#123", "github", false)
	if inputs["plan_path"] != "" {
		t.Errorf("plan_path=%q; con spec_issue puesto tiene que ir vacío", inputs["plan_path"])
	}
	if inputs["spec_issue"] != "#123" {
		t.Errorf("spec_issue=%v", inputs["spec_issue"])
	}
}

func TestSpecPathKeepsItsValueWhenNoIssueIsGiven(t *testing.T) {
	inputs := SpecInputs("features_path", "features.md", "", "local", false)
	if inputs["features_path"] != "features.md" {
		t.Errorf("features_path=%v", inputs["features_path"])
	}
	if inputs["spec_issue"] != "" {
		t.Errorf("spec_issue=%v; sin --spec-issue tiene que ir vacío", inputs["spec_issue"])
	}
}

// La clave del path cambia entre flows (plan_path en plan-tickets,
// features_path en factory). La regla de exclusión no.
func TestSpecInputsUsesTheCallersPathKey(t *testing.T) {
	inputs := SpecInputs("features_path", "spec.md", "", "local", false)
	if _, ok := inputs["plan_path"]; ok {
		t.Error("SpecInputs no debe inventar claves que el flow no declara")
	}
	if inputs["features_path"] != "spec.md" {
		t.Errorf("features_path=%v", inputs["features_path"])
	}
}
```

- [ ] **Step 2: Correr y verificar que falla**

Run: `go test ./internal/commands/ -run TestSpecIssue -count=1 -v`
Expected: FAIL, `undefined: planInputs`.

- [ ] **Step 3: Implementar `planInputs` en `internal/commands/aliases.go`**

```go
// SpecInputs arma los inputs de spec que comparten todos los flows que planean.
// Vive acá y no en main.go porque la regla que importa —spec_issue y el path
// son excluyentes, así que uno vacía al otro— es la misma que el tripwire del
// pack aplica, y tiene que ser testeable sin levantar la CLI. Si las dos se
// desincronizan, el operador recibe un error de ambigüedad por haber usado el
// flag correctamente, que es el peor error posible de dar.
//
// pathKey es el nombre que cada flow le da a su input de path: plan-tickets@1
// lo llama plan_path y el resto features_path. La regla no cambia con el
// nombre, así que el nombre es un parámetro y no una copia de la función.
func SpecInputs(pathKey, specPath, specIssue, ticketStore string, publishTickets bool) map[string]any {
	if specIssue != "" {
		specPath = ""
	}
	if ticketStore == "" {
		ticketStore = "local"
	}
	return map[string]any{
		pathKey:           specPath,
		"spec_issue":      specIssue,
		"ticket_store":    ticketStore,
		"publish_tickets": publishTickets,
	}
}
```

`ticket_store` **no se infiere** de los otros flags. Pasar `--publish-tickets`
sin `--ticket-store=github` es un error del operador y el pack lo va a tratar
como tal; adivinarlo acá escondería la intención en un lugar donde nadie la
busca.

- [ ] **Step 4: Correr los tests**

Run: `go test ./internal/commands/ -run TestSpecIssue -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Cablear los flags en `main.go`**

En el case `plan`, agregar junto a los flags existentes:

```go
specIssue := fs.String("spec-issue", "", "usar un issue de GitHub como spec en lugar de un archivo")
ticketStore := fs.String("ticket-store", "local", "dónde viven los tickets: local o github")
publishTickets := fs.Bool("publish-tickets", false, "crear los tickets como issues de GitHub y registrarlos en la spec")
```

y reemplazar el mapa literal por:

```go
inputs := commands.SpecInputs("plan_path", fs.Arg(0), *specIssue, *ticketStore, *publishTickets)
inputs["append"] = *appendFlag
exit(commands.RunDevelopmentAlias(ctx, ".", "plan", inputs, os.Stdout))
```

En el case `factory`, los mismos tres flags y:

```go
inputs := commands.SpecInputs("features_path", featuresPath, *specIssue, *ticketStore, *publishTickets)
inputs["fast"] = *fast
inputs["create_pr"] = *createPR
exit(commands.RunDevelopmentAlias(runCtx, ".", "factory", inputs, os.Stdout))
```

- [ ] **Step 6: Agregar el check de `gh` al doctor**

**`gh` ya se chequea** en `internal/doctor/doctor.go:224-228`, pero sólo su
presencia en PATH y con el mensaje "PR creation available". Un `gh` instalado y
sin autenticar pasa ese check y después rompe el run. Reemplazar ese bloque
por:

```go
	if _, err := exec.LookPath("gh"); err != nil {
		add(StatusWarn, "binary:gh", "not on PATH — factory --pr and ticket_store=github disabled")
	} else if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		// Instalado pero sin credenciales: el check de presencia pasaba y el
		// run moría después, al primer `gh issue view`.
		add(StatusWarn, "binary:gh", "on PATH but not authenticated — run `gh auth login`; ticket_store=github will fail")
	} else {
		add(StatusOK, "binary:gh", "PR creation and ticket_store=github available")
	}
```

`exec` ya está importado en ese archivo.

- [ ] **Step 7: Correr todo**

```bash
go build ./...
gofmt -l ./internal ./cmd ./packs
go test ./... -count=1 -race
```
Expected: build limpio, `gofmt -l` vacío, suite en PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/orq-lite/main.go internal/commands/aliases.go \
        internal/commands/ticketsourcecli_test.go internal/doctor/doctor.go
git commit -m "feat(cli): --spec-issue y --publish-tickets, y el check de gh

La regla de que spec_issue vacía el path vive en planInputs y no en main.go
para poder testearla sin levantar la CLI: es la misma exclusión que el
tripwire del pack aplica, y si las dos se desincronizan el operador recibe
un error de ambigüedad por haber usado el flag bien.

doctor reporta gh como reporta agent-browser, así que ticket_store=github
falla en doctor y no a mitad de un run.

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
```

---

### Task 7: Verificación end-to-end y documentación

**Files:**
- Modify: `guide.md`
- Modify: `packs/development/README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Correr un `init` limpio y validar el pack instalado**

```bash
rm -rf /tmp/orq-gh-check && mkdir -p /tmp/orq-gh-check
go run ./cmd/orq-lite init --lang python /tmp/orq-gh-check
cd /tmp/orq-gh-check
go run <repo>/cmd/orq-lite pack list
go run <repo>/cmd/orq-lite flow validate development@6/factory-governed@2
go run <repo>/cmd/orq-lite flow inspect development@6/factory-governed@2
```
Expected: ambos packs instalados, `valid`, y el `inspect` muestra el step
`plan_tickets` resolviendo a `subflow:tickets-source@1`.

- [ ] **Step 2: Documentar en `guide.md`**

En la sección donde hoy se explica `features.md` como contrato, agregar cómo
usar un issue como spec y cómo declarar los tickets con la sección
`## Tickets`, con el formato exacto del prompt.

- [ ] **Step 3: Actualizar `packs/development/README.md`**

Agregar `tickets-source@1` a la tabla de subflows con una línea de qué hace.

- [ ] **Step 4: Entrada de CHANGELOG**

Bajo `Unreleased`, describir la capacidad nueva, el default (`local`, sin
cambio de comportamiento) y que las escrituras requieren `--publish-tickets`.

- [ ] **Step 5: Commit y PR**

```bash
git add guide.md packs/development/README.md CHANGELOG.md
git commit -m "docs: tickets en GitHub

Claude-Session: https://claude.ai/code/session_01MbA8QHAvhLdkpiZk2aShkx"
git push -u origin lionelchamorro/github-tickets-impl
```

Abrir el PR con base `lionelchamorro/split-builtin-pack-from-examples` (o
`main` si #65 y #66 ya mergearon).

---

## Notas para quien ejecute

- **El riesgo real de este plan no es que el JSON compile.** `factory-governed@2`
  non-fast salió roto en v0.7.0 con todos los tests en verde porque nadie lo
  ejecutó nunca. Por eso cada task que agrega una rama de flow trae un test que
  la **corre** contra un pack fixture, y por eso los tests estructurales atan el
  fixture al archivo real.
- **Un gate que nunca se vio fallar no es un gate.** `tickets_are_real` tiene su
  test de falla en Task 3, Step 1. Si en algún momento hay que relajarlo, ese
  test es el que lo dice.
- Si un step necesita condicionarse y su salida la lee un step posterior, usar
  el patrón `enabled` (dato) en vez de `if` (condición), como hace
  `commit_ticket`. El comentario en `gitcommit.go` explica el porqué.
