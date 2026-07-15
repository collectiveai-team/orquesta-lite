# orq-lite Durable Dynamic Workflow Runtime — Implementation Plan

**Goal:** Reemplazar gradualmente los runtimes especializados de `internal/loops` y `internal/factory` por un único runtime durable y genérico que ejecute flows y subflows declarativos, dinámicos y versionados sobre activities tipadas.

**Resultado final:** `orq-lite` sabe cargar, validar, ejecutar, pausar, recuperar y observar workflows. No sabe qué significa coder/tester/critic, una task de desarrollo, una branch de factory o un PR. Ese conocimiento vive en un development pack externo.

**Estrategia:** migración strangler. El runtime actual queda disponible mientras se construye el nuevo. Los comandos se enrutan uno por uno y los packages especializados se eliminan solamente después de alcanzar paridad verificable.

**Este plan reemplaza:** las Tasks 12–21 de `docs/superpowers/plans/2026-07-01-product-readiness.md`. En particular, reemplaza la idea de envolver `loops.RunTaskLoopWithContext` como una activity nativa: eso escondería el runtime no genérico en una activity opaca y mantendría la duplicación arquitectónica.

## Decisiones cerradas

1. **Flows y subflows son datos.** Se cargan en runtime y se pueden cambiar sin recompilar `orq-lite`.
2. **Activities son efectos acotados.** `agent.invoke`, `command.run` o `git.commit` son activities; `implement_ticket` es un subflow.
3. **Flow v2 usa JSON canónico en el MVP.** YAML será un adapter posterior. El dinamismo no depende del formato y así no se agrega una dependencia sólo por sintaxis.
4. **El engine v1 no se extiende con nuevas features.** Permanece como compatibilidad hasta el cutover.
5. **El estado durable usa una DB separada.** `.orquestalite/workflows.db` es fuente de verdad operativa. `.orquestalite/orq.db` continúa como read-model reconstruible de `run.log`.
6. **El event log se conserva.** El runtime usa un outbox transaccional para no perder eventos entre el commit de estado y `run.log`.
7. **No se reintentan efectos inciertos a ciegas.** Cada activity declara `pure`, `idempotent`, `reconcilable`, `at_most_once` o `unsafe`.
8. **El MVP es secuencial.** Primero se prueba durability y resume. `parallel` llega después con workspaces aislados.
9. **Los runs fijan sus dependencias.** Definición compilada, hash, policy, pack y versiones de activities quedan snapshotteados al iniciar.
10. **El development pack termina fuera de este repositorio.** Durante el cutover puede existir un fixture pack dentro de `testdata`, pero no se incorporará otro runtime de desarrollo al core.

## Arquitectura objetivo

```text
cmd/orq-lite
    ↓
internal/commands              CLI delgada
    ↓
internal/flow                  spec v2, loader, compiler, IR, refs, expressions
    ↓
internal/workflow              scheduler, state machine, resume, policies
    ↓
internal/activity              contracts, registry, process protocol
    ↓
agent.invoke / command.run / gate.run / artifact.capture

External development pack
    flows + subflows + policies + prompts + schemas
    git/github/tracker/browser activities
```

Los packages existentes siguen este destino:

| Package actual | Destino |
| --- | --- |
| `internal/engine` | Compatibilidad v1; se elimina al terminar el cutover. |
| `internal/invoke`, `fallback`, `providers`, `runner`, `agenthealth` | Se conservan y se generalizan detrás de `agent.invoke`. |
| `internal/eventlog`, `artifacts`, `runid` | Se conservan y pasan a ser usados por `workflow`. |
| `internal/eventdb` | Se conserva como proyección de observabilidad; no almacena checkpoints operativos. |
| `internal/loops`, `factory`, `tasks`, `results` | Se eliminan cuando el development pack alcance paridad. |
| `internal/gitx`, `handoff`, `preflight` | Se mueven al development pack o se reemplazan por activities externas. |
| `internal/commands/{run,factory,review,intake,plan}cmd.go` | Se convierten en aliases de flow y finalmente se simplifican/eliminan. |

## Flow v2 mínimo

El formato debe ser declarativo y estricto. Las referencias son valores estructurados; no se dejan tokens sin resolver dentro de strings.

```json
{
  "apiVersion": "orq.dev/v2",
  "kind": "Flow",
  "metadata": {
    "name": "issue-fix",
    "version": "1.0.0"
  },
  "inputs": {
    "issue": {"schema": "schema:IssueRef@1"}
  },
  "steps": [
    {
      "id": "intake",
      "uses": "subflow:intake@1",
      "with": {
        "issue": {"$ref": "inputs.issue"}
      }
    },
    {
      "id": "implement",
      "uses": "subflow:implement-ticket@1",
      "foreach": {
        "items": {"$ref": "steps.intake.output.tickets"},
        "as": "ticket"
      },
      "with": {
        "ticket": {"$ref": "item"}
      },
      "retry": "policy:implementation-retry@1"
    }
  ],
  "outputs": {
    "tickets": {"$ref": "steps.implement.output"}
  }
}
```

No habrá un context map global mutable. Cada step ve solamente:

- Inputs del flow/subflow.
- Outputs declarados de steps anteriores.
- Variables de control como `item`, `index` y `attempt`.
- Metadata de sólo lectura bajo `run`.

## Modelo durable

```text
WorkflowRun
  pending → running → succeeded
                    ↘ failed
                    ↘ cancelled
                    ↘ needs_human

StepRun
  pending → ready → running → succeeded
                         ↘ failed
                         ↘ outcome_unknown
                         ↘ needs_human

Attempt
  scheduled → running → succeeded|failed|outcome_unknown
```

Tablas de `.orquestalite/workflows.db`:

- `workflow_runs`: identidad, flow ref, definición/IR snapshot, hashes, inputs, policy, status y timestamps.
- `step_runs`: `run_id`, `step_id`, `scope_path`, `foreach_key`, inputs, outputs, status y error final.
- `attempts`: número, activity ref, idempotency key, status, error class, receipt y timestamps.
- `artifacts`: metadata y URI de evidencia; los bytes siguen en `.orquestalite/runs/<run_id>/`.
- `outbox_events`: evento JSON pendiente de publicar en `run.log`.
- `approvals`: requests y decisiones humanas.

La clave lógica de un step dinámico es:

```text
run_id / scope_path / step_id / foreach_key
```

El número de intento no forma parte de la idempotency key.

## Restricciones globales

- Mantener `CGO_ENABLED=0` y el driver `modernc.org/sqlite` existente.
- No modificar ni reutilizar el schema de `.orquestalite/orq.db` para estado operacional.
- Toda escritura durable debe ocurrir en una transacción.
- Todo evento derivado de una transición durable debe entrar al outbox en esa misma transacción.
- Strict JSON decoding: campos desconocidos producen error en flow specs, policies y activity manifests.
- Un flow inválido no puede crear un `WorkflowRun`.
- Un output inválido no puede completar un `StepRun`.
- Ningún loop puede ser ilimitado: siempre tiene budget de intentos, tiempo o costo.
- Un run reanudado usa el IR y la policy snapshotteados, aunque los archivos hayan cambiado.
- El proceso activity externo recibe argv estructurado; nunca se construye mediante concatenación de shell.
- Cada PR termina con `go build ./...`, `go vet ./...`, `go test ./...` y los tests focalizados indicados.
- No editar los cambios locales actuales de `benchmark/`, `features.md` o `tasks/todo-benchmark-hybrid-eval.md`.

## Execution tracker

- [x] 0.1 ADR del runtime único.
- [x] 0.2 Matriz de paridad ejecutable.
- [x] 1.1 Modelo y strict loader.
- [x] 1.2 Schemas y catálogo.
- [x] 1.3 Compiler e IR inmutable.
- [x] 1.4 Expresiones y resolución de values.
- [x] 2.1 Contrato, registry y error taxonomy.
- [x] 2.2 Generic raw agent invocation.
- [x] 2.3 Built-ins universales.
- [x] 2.4 Protocolo de activities externas.
- [x] 3.1 Workflow DB y migraciones.
- [x] 3.2 Transiciones y outbox.
- [x] 3.3 Scheduler secuencial.
- [x] 3.4 Resume, retry y outcome reconciliation.
- [x] 3.5 Policies y budgets.
- [x] 4.1 Flow facade y comandos CLI.
- [x] 4.2 Config genérica y roles dinámicos.
- [x] 4.3 Run lifecycle unificado.
- [x] 4.4 API web genérica.
- [x] 5.1 While acotado y handlers.
- [x] 5.2 Parallel + join sobre isolation key.
- [x] 5.3 Pack manifest y pinning.
- [x] 6.1 Aliases con routing gradual.
- [x] 6.2 Coexistencia de estado legacy.
- [x] 6.3 Watch dispara flows.
- [ ] 6.4 Gates para borrar código especializado.
- [ ] 6.5 Eliminación y cleanup.

> Estado al 2026-07-14: el core y el routing de coexistencia están
> implementados. 6.4 y 6.5 permanecen abiertos porque dependen de hechos
> externos que este branch no puede fabricar: development pack con paridad,
> benchmark N≥3, canary dogfooded, rollback probado y ausencia de runs legacy
> activos. Eliminar el runtime especializado antes de esos gates contradiría
> explícitamente la estrategia strangler de este mismo plan.

---

## Milestone 0 — Baseline y límites de la migración

### Task 0.1: ADR del runtime único

**Files:**

- Create: `docs/adr/0005-durable-dynamic-workflow-runtime.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `tasks/todo.md`

**Trabajo:**

- Registrar las decisiones de este documento.
- Marcar engine v1 y runtimes especializados como legacy/frozen.
- Documentar `workflows.db` vs `orq.db`.
- Reemplazar las Tasks 12–21 del tracker por un enlace a este plan.
- Agregar un diagrama temporal de coexistencia v1/v2.

**Acceptance:** un contribuidor puede determinar qué package acepta features nuevas y cuál está en mantenimiento.

### Task 0.2: Matriz de paridad ejecutable

**Files:**

- Create: `docs/workflow-runtime-parity.md`
- Create: `internal/legacytest/scenarios.go`
- Create: `internal/legacytest/scenarios_test.go`
- Reuse/Modify: `internal/commands/e2e_scenarios_test.go`

**Trabajo:**

- Inventariar comportamiento de `run`, `factory`, `factory --fast`, `review`, `intake`, `plan` y `watch`.
- Extraer fixtures sin efectos externos para: success, invalid contract, lint/test/critic fail, full suite fail, decomposition, repeated failure, handoff, residue checkpoint, merge conflict, budget exhausted, interrupt y resume.
- Hacer que cada scenario declare `legacy_expected` y más adelante `v2_expected`.
- No ejecutar engine viejo y nuevo sobre el mismo worktree mutable; usar fakes o repos temporales separados.

**Acceptance:** toda invariant que será eliminada de `loops`/`factory` tiene un scenario o una decisión de descarte explícita.

---

## Milestone 1 — Flow spec v2 y compiler

### Task 1.1: Modelo y strict loader

**Files:**

- Create: `internal/flow/spec.go`
- Create: `internal/flow/decode.go`
- Create: `internal/flow/decode_test.go`
- Create: `internal/flow/testdata/valid/*.json`
- Create: `internal/flow/testdata/invalid/*.json`

**Interfaces:**

```go
type Document struct {
    APIVersion string
    Kind       Kind
    Metadata   Metadata
    Inputs     map[string]InputSpec
    Steps      []StepSpec
    Outputs    map[string]Value
}

func Decode(r io.Reader) (*Document, error)
func Load(path string) (*Document, error)
```

**Trabajo:**

- Implementar `json.Decoder.DisallowUnknownFields`.
- Validar formato de name/version, IDs únicos y campos requeridos.
- Definir `Value` como literal, objeto/lista recursiva o `Ref`; no interpolar braces.
- Aceptar solamente `apiVersion=orq.dev/v2` y `kind=Flow|Subflow`.
- No tocar `internal/engine.LoadFlows`; v1 y v2 coexisten.

**Tests:** unknown fields, duplicate IDs, invalid refs syntax, invalid version, empty uses, literal `$` y fixtures round-trip.

### Task 1.2: Schemas y catálogo

**Files:**

- Create: `internal/flow/schema.go`
- Create: `internal/flow/schema_test.go`
- Create: `internal/flow/catalog.go`
- Create: `internal/flow/catalog_test.go`

**Decisión:** implementar un subconjunto explícito de JSON Schema suficiente para los contratos actuales: `type`, `properties`, `required`, `additionalProperties`, `items`, `enum`, `minItems`, `minLength`. Un keyword desconocido falla cerrado. Evaluar una librería pure-Go completa en un ADR separado antes de ampliar el subset.

**Interfaces:**

```go
type Catalog interface {
    ResolveFlow(Ref) (*Document, Digest, error)
    ResolveSubflow(Ref) (*Document, Digest, error)
    ResolveSchema(Ref) (*Schema, Digest, error)
    ResolvePolicy(Ref) (*Policy, Digest, error)
    ResolveActivity(Ref) (*activity.Spec, error)
}
```

**Orden de discovery:** pack fijado explícitamente → `.orquestalite/packs/` → embedded core catalog. No descargar nada de red desde el loader.

### Task 1.3: Compiler e IR inmutable

**Files:**

- Create: `internal/flow/ir.go`
- Create: `internal/flow/compiler.go`
- Create: `internal/flow/compiler_test.go`

**Interfaces:**

```go
func Compile(ctx context.Context, doc *Document, catalog Catalog) (*IR, Diagnostics)
```

**Trabajo:**

- Resolver recursivamente subflows y detectar ciclos.
- Resolver/pinnear versions de schemas, policies y activities.
- Validar references contra el scope y orden de steps.
- Validar paths conocidos contra output schemas.
- Expandir defaults sin ejecutar código.
- Producir JSON canónico y SHA-256 del IR completo.
- Mantener boundaries de subflow en `scope_path` para observabilidad y resume.
- Separar warnings de errores; cualquier error impide ejecutar.

**Acceptance:** el mismo catálogo y documento siempre producen el mismo IR/hash, independientemente del orden de maps Go.

### Task 1.4: Expresiones y resolución de values

**Files:**

- Create: `internal/flow/expr.go`
- Create: `internal/flow/expr_test.go`
- Create: `internal/flow/value.go`
- Create: `internal/flow/value_test.go`
- Reuse logic from: `internal/engine/eval.go`

**Trabajo:**

- Portar el parser booleano actual sin interpolar strings primero.
- Agregar acceso tipado a refs, `null`, comparaciones numéricas y string, `contains` y `exists` solamente si un caso real lo exige.
- Resolver `Value` preservando tipos JSON.
- Un path ausente es error, no string sin reemplazar ni zero value.
- Limitar tamaño/profundidad de inputs resueltos.

**No hacer:** lenguaje de scripting, funciones arbitrarias, acceso al filesystem o environment desde expressions.

---

## Milestone 2 — Activity model genérico

### Task 2.1: Contrato, registry y error taxonomy

**Files:**

- Create: `internal/activity/spec.go`
- Create: `internal/activity/activity.go`
- Create: `internal/activity/registry.go`
- Create: `internal/activity/errors.go`
- Create: `internal/activity/activity_test.go`

**Interfaces:**

```go
type EffectMode string // pure|idempotent|reconcilable|at_most_once|unsafe

type Executor interface {
    Spec() Spec
    Execute(context.Context, Request) (Result, error)
}

type Reconciler interface {
    Reconcile(context.Context, ReconcileRequest) (Result, error)
}

type Compensator interface {
    Compensate(context.Context, CompensateRequest) (Result, error)
}
```

Errores estables: `transient`, `rate_limited`, `timeout`, `invalid_contract`, `gate_failed`, `conflict`, `permission_denied`, `cancelled`, `permanent`, `outcome_unknown`.

**Acceptance:** registry rechaza versiones duplicadas y el dispatcher valida input/output con el schema de la activity.

### Task 2.2: Generic raw agent invocation

**Files:**

- Modify: `internal/invoke/role.go`
- Modify: `internal/invoke/classify.go`
- Modify: `internal/fallback/fallback.go`
- Create: `internal/invoke/raw.go`
- Create: `internal/invoke/raw_test.go`
- Create: `internal/activity/builtin/agent.go`
- Create: `internal/activity/builtin/agent_test.go`
- Preserve: typed `invoke.Role[T]` as legacy wrapper

**Trabajo:**

- Crear `invoke.Raw` que devuelve JSON genérico y metadata de la invocación.
- Pasar un validator callback por invocation; parse/schema failure se clasifica `invalid_contract` dentro de la fallback chain.
- El siguiente agente se prueba cuando el anterior escribió output inválido.
- `agent.invoke` recibe `role`, vars/context y output schema ref; no contiene nombres coder/tester/critic.
- Conservar fallback, health, rate-limit wait, sessions, memory y artifacts.
- Remover de la ruta v2 la inferencia `derivePass`; el flow debe comparar campos definidos por el schema.

**Acceptance:** una role custom no declarada en código puede ejecutarse y ser validada; dos agentes inválidos terminan en error `invalid_contract` agregado.

### Task 2.3: Built-ins universales

**Files:**

- Create: `internal/activity/builtin/command.go`
- Create: `internal/activity/builtin/gate.go`
- Create: `internal/activity/builtin/artifact.go`
- Create: `internal/activity/builtin/approval.go`
- Create tests beside each file

**Semántica:**

- `command.run`: argv obligatorio por default; shell sólo con capability/policy explícita. Effect mode default `at_most_once`.
- `gate.run`: command argv, output `{passed, exit_code, stdout_artifact, duration}`; exit no cero es resultado `gate_failed`, no success silencioso.
- `artifact.capture`: persiste bytes/text/file permitido y devuelve digest/URI.
- `human.wait_for_approval`: crea approval durable y deja el step/run en `needs_human`.

**No incluir:** Git, GitHub, tracker, browser, task planner o factory actions.

### Task 2.4: Protocolo de activities externas

**Files:**

- Create: `internal/activity/process/manifest.go`
- Create: `internal/activity/process/executor.go`
- Create: `internal/activity/process/protocol.go`
- Create: `internal/activity/process/executor_test.go`
- Create: `internal/activity/process/testdata/helper/`
- Create: `docs/activity-protocol.md`

**Protocolo:** JSON request/result versionado por stdin/stdout, un proceso por operación en v1. Operaciones: `describe`, `execute`, `reconcile`, `compensate`.

**Seguridad:** argv definido en manifest, timeout/cancelación, tamaño máximo de mensajes, stdout reservado al frame de resultado y stderr capturado como artifact. No interpolación de shell.

**Acceptance:** un helper process de test implementa execute/reconcile, sobrevive rerun con la misma idempotency key y demuestra compatibilidad sin importar packages Go.

---

## Milestone 3 — Store durable y scheduler secuencial

### Task 3.1: Workflow DB y migraciones

**Files:**

- Create: `internal/workflow/store.go`
- Create: `internal/workflow/sqlite.go`
- Create: `internal/workflow/migrations.go`
- Create: `internal/workflow/store_test.go`

**Trabajo:**

- Crear `.orquestalite/workflows.db` con WAL, busy timeout y `user_version` propio.
- Implementar las seis tablas del modelo durable e índices por status/run/step.
- Guardar IR, inputs y policy JSON canónicos en `workflow_runs`.
- APIs transaccionales explícitas; no exponer `*sql.DB` al scheduler.
- Aplicar migraciones forward, testeadas desde cada versión soportada.

**Acceptance:** open/create/reopen/migrate son idempotentes y `orq.db` no cambia.

### Task 3.2: Transiciones y outbox

**Files:**

- Create: `internal/workflow/model.go`
- Create: `internal/workflow/transitions.go`
- Create: `internal/workflow/outbox.go`
- Create: `internal/workflow/transitions_test.go`
- Modify: `internal/eventlog/eventlog.go` only if an append interface is needed

**Trabajo:**

- Centralizar todas las transiciones permitidas.
- En la misma transacción: cambiar estado + insertar evento outbox.
- Dispatcher append a `run.log` y marca delivered. La entrega es at-least-once: cada evento lleva un event ID estable y los consumidores deduplican por ese ID.
- Actualizar `eventdb` para deduplicar `events` por event ID antes de activar outbox replay.
- Eventos mínimos: `workflow_started`, `step_ready`, `attempt_started`, `activity_completed`, `activity_failed`, `step_completed`, `workflow_completed`, `approval_requested`.

**Acceptance:** crash después del commit y antes del append termina publicando el evento al reabrir, sin perderlo ni duplicarlo en la proyección.

### Task 3.3: Scheduler secuencial

**Files:**

- Create: `internal/workflow/scheduler.go`
- Create: `internal/workflow/scheduler_test.go`
- Create: `internal/workflow/context.go`
- Create: `internal/workflow/context_test.go`

**Trabajo:**

- Materializar el siguiente step `ready` desde IR y estado durable.
- Resolver inputs y validar schemas inmediatamente antes de schedule.
- Persistir attempt `running` antes de llamar a la activity.
- Validar output, persistir artifacts/receipt y completar el step.
- Propagar outputs solamente por references explícitas.
- Implementar `if`, subflow call y `foreach` secuencial.
- Terminar en `needs_human` cuando una operación lo requiere.
- Inyectar clock/ID generator/executor para tests deterministas.

**Acceptance:** un flow completo con subflow y foreach corre sobre activities fake y su historial se reconstruye sólo desde DB.

### Task 3.4: Resume, retry y outcome reconciliation

**Files:**

- Create: `internal/workflow/recovery.go`
- Create: `internal/workflow/retry.go`
- Create: `internal/workflow/recovery_test.go`
- Create: `internal/workflow/chaos_test.go`

**Reglas:**

- `pure|idempotent`: puede reejecutarse con la misma key según policy.
- `reconcilable`: primero `Reconcile`; sólo ejecuta de nuevo si responde `not_applied`.
- `at_most_once`: `running` tras crash pasa a `outcome_unknown` y requiere reconciler o humano.
- `unsafe`: requiere approval antes del primer execute y nunca tiene retry automático.
- `invalid_contract`, `permission_denied`, `permanent`: no retry salvo policy explícita y autorizada.
- `transient`, `rate_limited`, `timeout`: backoff/jitter/budget.

**Tests de crash:** antes de execute, durante execute, después del efecto/antes del result, después del result/antes del step complete, después del DB commit/antes del event append.

**Acceptance:** ninguna prueba duplica el efecto contador del fake activity y cada caso incierto queda observable.

### Task 3.5: Policies y budgets

**Files:**

- Create: `internal/workflow/policy.go`
- Create: `internal/workflow/policy_test.go`
- Create: `internal/workflow/budget.go`
- Create: `internal/workflow/budget_test.go`

**Campos MVP:** retries por error class, timeout, max attempts, max duration, max agent attempts, max cost USD, shell capability y approval por effect mode/activity.

**Acceptance:** el agente/flow no puede incrementar su budget y el snapshot de policy queda inmutable durante resume.

---

## Milestone 4 — CLI, config y observabilidad v2

### Task 4.1: Flow facade y comandos CLI

**Files:**

- Create: `internal/commands/workflowcmd.go`
- Create: `internal/commands/workflowcmd_test.go`
- Modify: `cmd/orq-lite/main.go`
- Preserve: `internal/commands/flowcmd.go` for v1

**Comandos:**

```text
orq-lite flow validate <flow-ref-or-path>
orq-lite flow inspect <flow-ref-or-path>
orq-lite flow list
orq-lite flow run <flow-ref> key=value...
orq-lite flow status <run-id>
orq-lite flow resume <run-id>
orq-lite flow cancel <run-id>
orq-lite flow approve <run-id> <approval-id> --decision approve|reject
orq-lite flow events <run-id>
```

**Trabajo:**

- Resolver ref/path, compilar y validar antes de crear el run.
- Inputs CLI se parsean según schema, no todos como string.
- Detectar v1/v2 por `apiVersion`; mantener `flow run <v1-name>` durante deprecación.
- Imprimir `run_id`, flow ref/hash, pack/policy y ruta de artifacts al iniciar.
- `resume` recibe solamente run ID; nunca una definición nueva.

### Task 4.2: Config genérica y roles dinámicos

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/commands/assets/team.json`

**Trabajo:**

- Remover `orchestratedRoles` de la ruta v2; resolver todas las roles declaradas uniformemente.
- Validar solamente las roles referenciadas por el IR del flow a ejecutar.
- Introducir sección `runtime` para defaults universales: retention, artifact limits y provider backoff.
- Mantener `Limits`, verifier mode, full/lint commands y campos factory mientras viva legacy.
- Emitir warnings de deprecación sólo cuando se usa flow v2 con config legacy.

**Acceptance:** un team con una única role custom puede ejecutar un flow v2 aunque no declare parser/coder/tester/critic/reviewer.

### Task 4.3: Run lifecycle unificado

**Files:**

- Modify: `internal/commands/runcmd.go` to extract generic setup
- Create: `internal/commands/runtime_deps.go`
- Modify: `internal/runid/runid.go`
- Modify: `internal/artifacts/artifacts.go`

**Trabajo:**

- Extraer creación de logger, invoker, artifacts, health y cleanup de `newLiveDeps` sin importar loops/tasks.
- El workflow runtime pasa a ser dueño de `workflow_started/completed`; evitar doble `run_start/run_end`.
- Manifest de run incluye flow ref/hash, policy hash, pack digests y activities.
- Pruning nunca elimina artifacts de un run no terminal.

### Task 4.4: API web genérica

**Files:**

- Create: `internal/web/workflows.go`
- Create: `internal/web/workflows_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/static/index.html`
- Modify: `docs/query-api.md`

**Endpoints:**

- `GET /api/workflows`
- `GET /api/workflows/{run_id}`
- `GET /api/workflows/{run_id}/steps`
- `GET /api/workflows/{run_id}/approvals`

Las mutations siguen en CLI/control plane; `serve` permanece read-only.

**Compatibilidad:** `/api/tasks` y `/api/factory` se mantienen mientras existan legacy runs. La UI nueva no asume roles ni estados de desarrollo.

---

## Milestone 5 — Dinamismo adicional y pack contract

### Task 5.1: While acotado y handlers

**Files:**

- Modify: `internal/flow/spec.go`, `ir.go`, `compiler.go`
- Modify: `internal/workflow/scheduler.go`
- Add focused tests

**Trabajo:**

- `while` requiere `maxIterations`, timeout o budget ref.
- Handlers declarativos `onError`, `onCancel` y `compensate` llaman subflows/activities normales.
- Compensación recorre solamente effects confirmados y con compensator.
- Un handler no puede borrar la causa original; queda en el resultado del run.

### Task 5.2: Parallel + join sobre isolation key

**Prerequisito:** el development pack debe proveer un workspace/worktree distinto por item.

**Files:**

- Modify: flow IR/compiler and workflow scheduler/store
- Create: `internal/workflow/parallel_test.go`

**Trabajo:**

- `foreach.maxConcurrency > 1` exige `isolationKey` y policy limit.
- Persistir cada item como StepRun independiente.
- Cancelación estructurada y join modes `all|all_success|quorum` sólo cuando haya casos de uso aprobados.
- Detectar claves de aislamiento duplicadas antes de despachar.

**No implementar paralelismo** antes de que resume secuencial y chaos tests estén verdes.

### Task 5.3: Pack manifest y pinning

**Files:**

- Create: `internal/flow/pack.go`
- Create: `internal/flow/pack_test.go`
- Create: `docs/pack-format.md`
- Create: `internal/flow/testdata/packs/development-fixture/`

**Manifest contiene:** name/version, flows, subflows, schemas, policies, prompts, external activity manifests y digests.

**Trabajo:**

- Resolver todo localmente y verificar digest.
- Copiar manifest y definiciones resueltas al run snapshot.
- Rechazar cambio de contenido bajo la misma version en un resume.
- No implementar marketplace/download; Orquesta será responsable de instalar packs.

---

## Milestone 6 — Migración de comandos y cutover

Este milestone depende de un development pack externo que implemente los flows equivalentes. Los cambios siguientes sí ocurren en `orquesta-lite`.

### Task 6.1: Aliases con routing gradual

**Files:**

- Create: `internal/commands/aliases.go`
- Create: `internal/commands/aliases_test.go`
- Modify: `cmd/orq-lite/main.go`
- Modify: `internal/commands/{review,intake,plan,run,factory}cmd.go`

**Mapping objetivo:**

```text
plan     → development/plan-tickets@1
intake   → development/issue-fix@1, con stop policy si --no-run
review   → development/pr-review@1
run      → development/task-list@1
factory  → development/factory-governed@1
```

**Routing:**

1. Flag temporal `--engine=legacy|v2`, inicialmente legacy.
2. Canary CI y dogfooding con v2.
3. Default v2 cuando el flow alcanza su gate de paridad.
4. Flag legacy disponible un ciclo de release adicional.

Cada alias imprime el flow ref/hash real que ejecuta.

### Task 6.2: Coexistencia de estado legacy

**Files:**

- Create: `internal/commands/legacy_state.go`
- Create: `internal/commands/legacy_state_test.go`
- Modify: factory/run aliases

**Decisión:** no convertir automáticamente `factory.json`/`tasks.json` incompletos a StepRuns. Un estado legacy unfinished se termina con legacy; los runs nuevos usan v2.

**Reglas:**

- Si existe `factory.json` no terminal y no se especifica engine, route legacy con warning.
- `flow resume <run-id>` nunca lee `factory.json` o `tasks.json`.
- `--engine=v2` con estado legacy unfinished exige confirmación/`--force-new-run` y no borra archivos.
- Dashboard muestra ambos modelos durante la ventana de compatibilidad.

### Task 6.3: Watch dispara flows, no commands especializados

**Files:**

- Modify: `internal/watch/watch.go`
- Modify: `internal/commands/watchcmd.go`
- Modify tests

**Trabajo:**

- Watch produce un trigger genérico `{flow_ref, inputs, idempotency_key}`.
- Idempotency key incluye provider/item/update identity para no crear runs duplicados.
- Issue/PR mapping se mueve a config o al development pack.
- Mantener legacy trigger detrás del flag de compatibilidad.

### Task 6.4: Gates para borrar código especializado

Antes de eliminar packages:

- Todos los scenarios de paridad relevantes verdes en v2.
- N≥3 de benchmark con igual modelo/config y cero regresiones críticas.
- Chaos suite completa sin efectos duplicados.
- Un release canary dogfooded con v2 default.
- Rollback a la versión anterior probado.
- Ningún run legacy activo en los proyectos controlados.
- Development pack versionado, instalado y resoluble offline.

### Task 6.5: Eliminación y cleanup

**Files to delete after gates:**

- `internal/loops/`
- `internal/factory/`
- `internal/tasks/`
- `internal/results/`
- `internal/handoff/`
- `internal/preflight/`
- `internal/engine/`
- Built-in factory flow assets and role-specific schemas/prompts

**Files to move out or delete after pack parity:**

- `internal/gitx/`
- Development-specific portions of `internal/commands/runcmd.go`, `factorycmd.go`, `reviewcmd.go`, `intakecmd.go`, `plancmd.go`, `testdetect.go`
- `/api/tasks`, `/api/factory` and role-specific dashboard views after their deprecation window
- Config fields `MaxReviewCycles`, `MaxFixIterations`, verifier modes, factory budgets/retries, full/lint commands and decompose prompts

**Final checks:**

```bash
rg -n 'coder|tester|critic|reviewer|factory|tasks.json' internal --glob '*.go'
go build ./...
go vet ./...
go test ./...
go test -race ./internal/flow/... ./internal/activity/... ./internal/workflow/...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

Las coincidencias restantes deben ser únicamente tests/fixtures de compatibilidad explícitos o documentación de migración.

---

## Secuencia propuesta de PRs

Cada PR debe ser mergeable y no cambiar el default de producción hasta el PR 15.

1. ADR + parity matrix + freeze v1.
2. Flow v2 strict loader y specs.
3. Schema subset + catalog.
4. Compiler/IR/subflows/refs.
5. Activity contracts y registry.
6. `invoke.Raw` + `invalid_contract` dentro de fallback.
7. Built-ins universales.
8. External activity process protocol.
9. Workflow SQLite store + migrations.
10. Transitions + transactional outbox.
11. Scheduler secuencial + `if`/subflow/foreach.
12. Resume/reconcile/retry + chaos tests.
13. Policies/budgets/approvals.
14. CLI v2 + generic web API + config generalization.
15. First canary flow from external development pack (`pr-review`).
16. `task-list` cutover.
17. `factory-fast`, then `factory-governed` cutover.
18. Watch routing and v2 default.
19. Deprecation release.
20. Delete specialized runtime after gates.

## MVP para el primer corte usable

El MVP termina en el PR 14 y debe demostrar:

- Flow/subflow v2 dinámico cargado desde disco.
- Strict validation y hash reproducible.
- Activities in-process y externas.
- `agent.invoke`, `command.run`, `gate.run`, `artifact.capture`, approval.
- Scheduler secuencial con `if`, `foreach`, subflows y retry tipado.
- SQLite durable, outbox, artifacts y resume.
- Kill tests sin duplicar efectos.
- CLI run/status/resume/cancel/approve.
- API read-only de workflows.
- Team con roles completamente dinámicas.

No incluye:

- Parallel execution.
- Worker remoto o distributed leases.
- YAML.
- Marketplace/descarga de packs.
- Factory cutover.
- Eliminación del runtime legacy.

## Verification matrix

| Riesgo | Prueba requerida |
| --- | --- |
| Flow ambiguo | Strict decode + compiler diagnostics golden tests. |
| Reference rota | Compile error antes de crear run. |
| Output de agente inválido | Fallback a otro agente; error `invalid_contract` si todos fallan. |
| Crash antes del effect | Resume ejecuta una vez. |
| Crash después del effect | Reconcile confirma o marca `outcome_unknown`; nunca retry ciego. |
| Event log desincronizado | Outbox replay + dedupe por event ID. |
| Flow cambia en disco | Resume usa IR snapshot/hash original. |
| Pack cambia bajo misma version | Digest mismatch y fail closed. |
| Loop infinito | Compiler/policy exige límite; runtime aplica budget. |
| Shell injection | argv estructurado; shell requiere capability explícita. |
| Legacy state existente | Routing legacy o error seguro; ningún archivo se borra. |
| Regressión de comportamiento | Parity scenarios + benchmark N≥3. |

## Riesgos y mitigaciones

### Duplicar el engine durante demasiado tiempo

Mitigación: freeze de v1, owners y fecha/gate de deprecación. Ninguna feature nueva entra en `internal/engine`, `loops` o `factory` salvo corrección crítica.

### Crear un mini-Temporal demasiado grande

Mitigación: MVP local, secuencial, SQLite y un proceso. No workers distribuidos, leases ni server central en este plan.

### Llamar “activity” a un runtime oculto

Mitigación: review rule: una activity no agenda otros steps, no mantiene su propio retry loop y no decide el workflow. Si necesita hacerlo, debe ser subflow.

### Idempotencia falsa en comandos arbitrarios

Mitigación: `command.run` es `at_most_once`; un crash con outcome incierto escala. Sólo activities de dominio con reconciler pueden reintentar efectos externos.

### Acoplar el core al primer development pack

Mitigación: los tests del core usan fixture packs con nombres neutrales (`produce`, `verify`, `publish`) y un helper activity; no coder/tester/factory.

### Partir el repositorio demasiado pronto

Mitigación: estabilizar el activity/pack protocol con fixtures locales. El development pack real evoluciona en repo separado, pero el core no depende de publicarlo para completar el MVP.

## Definition of done del programa

- Existe un único scheduler en el binario.
- Ninguna role o flow de desarrollo está hardcodeada en config/runtime.
- Flows y subflows se cambian sin recompilar.
- Activities externas se agregan sin importar código Go en `orq-lite`.
- Todos los runs se pueden observar y los elegibles se pueden reanudar.
- Los efectos inciertos nunca se repiten silenciosamente.
- Los aliases históricos ejecutan flows versionados o fueron retirados tras deprecación.
- `internal/loops`, `internal/factory` e `internal/engine` ya no existen.
- El benchmark no muestra regresiones críticas respecto del runtime gobernado actual.
