# orq-lite Product Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dejar orq-lite listo como producto: 3 flows por defecto funcionando desde `init` (factory fast + gobernanza PM/Architect/QA, pr_review con daemon/Action, issue_fix), engine de flows dinámicos endurecido, y trazabilidad completa (run IDs, prompts persistidos, diffs por agente/intento).

**Architecture:** Converger sobre el flow engine (`internal/engine`) exponiendo las capacidades del pipeline hardcoded como acciones nativas, rutear el daemon `watch` a flows, y agregar una capa de artefactos por corrida (`.orquestalite/runs/<run_id>/`) que persiste prompt/salida/diff de cada invocación de agente.

**Tech Stack:** Go 1.24 (stdlib only, sin dependencias nuevas), JSON Schema embebido, `gh` CLI para GitHub, GitHub Actions YAML.

**Assessment de referencia:** `docs/superpowers/specs/2026-07-01-product-readiness-assessment.md`

## Global Constraints

- Sin dependencias externas nuevas en `go.mod`, con **una excepción aprobada**: `modernc.org/sqlite` (driver puro Go, Task 11b) — obligatorio que siga funcionando `CGO_ENABLED=0` y el cross-compile de 6 targets del release.
- Todo evento nuevo se emite vía `eventlog.Logger.Log(Event{Type, Fields})` y debe renderizar en formato `human` y `verbose`.
- Ninguna ruta nueva del dashboard puede escribir estado (read-only).
- Compatibilidad hacia atrás: `.orquestalite/` existente de una versión previa no debe romper el binario nuevo (campos nuevos opcionales, archivos nuevos ausentes tolerados).
- Cada tarea termina con `go build ./... && go vet ./... && go test ./...` en verde y un commit.
- Convención de commits del repo: `feat(pkg): ...` / `fix(pkg): ...` / `docs: ...` / `refactor(pkg): ...`.

> **Nota de alcance (scope check):** este plan cubre 6 fases independientes. Cada fase es shippeable por separado y las fases 2 y 3 merecen su propio pase de `writing-plans` con código completo al momento de ejecutarlas. Las tareas de las fases 0 y 1 están especificadas a nivel de código; las de fases 2–5 a nivel de contrato + snippets de las piezas críticas.

---

## Fase 0 — Desbloqueos P0 (primera experiencia de uso)

### Task 1: Shippear `prompts/intake.md` embebido

`assets/team.json:36` referencia `prompts/intake.md` pero el archivo no existe en `internal/commands/assets/prompts/` → `init` scaffoldea un proyecto donde `doctor` falla e `intake`/`watch --issues` rompen.

**Files:**
- Create: `internal/commands/assets/prompts/intake.md`
- Test: `internal/commands/initcmd_test.go` (extender)

**Interfaces:**
- Produces: prompt con contrato de resultado `results.IntakeResult` (`{actionable: bool, plan: string, missing_info: []string, notes_for_memory}`) escrito en `.orquestalite/results/intake.json`.

- [ ] **Step 1: Test que falla** — en `initcmd_test.go`, agregar assert de que tras `Init(dir)` existe `prompts/intake.md` y que todo `roles[*].prompt` de `team.json` scaffoldeado existe en disco (test genérico que previene regresiones futuras de este mismo bug):

```go
func TestInitScaffoldsEveryRolePrompt(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, role := range cfg.Roles {
		p := filepath.Join(dir, role.Prompt)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("role %s: prompt %s not scaffolded: %v", name, role.Prompt, err)
		}
	}
}
```

*(Ajustar la firma real de `Init`/`InitOptions` al código actual de `initcmd.go`.)*

- [ ] **Step 2: Correr y ver fallar** — `go test ./internal/commands/ -run TestInitScaffoldsEveryRolePrompt` → FAIL por `prompts/intake.md`.
- [ ] **Step 3: Escribir el prompt** — `intake.md` con: rol (analista de intake de issues), input `{{ISSUE_BODY}}`, instrucciones de decidir accionabilidad, y contrato JSON exacto (campos `actionable`, `plan`, `missing_info`, `notes_for_memory`) escrito como **última acción** en `.orquestalite/results/intake.json`, siguiendo el estilo de los 13 prompts existentes en `assets/prompts/`.
- [ ] **Step 4: Verificar embed** — confirmar que el patrón `//go:embed assets/prompts/*.md` (`initcmd.go:14`) lo captura (lo hace, es glob). Test en verde.
- [ ] **Step 5: Commit** — `fix(commands): ship missing intake.md prompt in init assets`

### Task 2: Destrackear la config de dogfooding (assets del paquete ≠ config del repo)

**Aclaración importante:** hay dos conjuntos de assets distintos y el problema es que se mezclaron. (1) Los **assets del paquete** (`internal/commands/assets/`) son la fuente canónica embebida que `init` scaffoldea — están completos y sanos. (2) La **config de dogfooding** (raíz: `team.json`, `prompts/`, `schemas/`) es la config de runtime para correr orq-lite sobre su propio repo; está gitignoreada a propósito y diverge legítimamente de los assets (p. ej. `lint_command: "go vet ./..."`). El bug real es el estado híbrido: se force-addeó un snapshot viejo y parcial de esa config (9 de 14 prompts, `team.json` sin `generalist`), así que un clone fresco recibe dogfooding incompleto — lo peor de ambos mundos.

**Política elegida (decisión de usuario 2026-07-01): generada, no trackeada.** La config de dogfooding se regenera con `orq-lite init` tras clonar; la única fuente de verdad versionada son los assets embebidos.

**Files:**
- Modify: índice de git (`git rm --cached`), sin cambios en `.gitignore` (ya ignora correctamente)
- Modify: `README.md` (sección de desarrollo)
- Create: `docs/adr/0004-embedded-assets-canonical.md`

- [ ] **Step 1: Destrackear** — `git rm --cached team.json prompts/*.md schemas/*.json` (16 archivos; verificar con `git ls-files prompts/ schemas/ team.json` que quede vacío). Los archivos quedan en disco para el dogfooding local. `flows.json` raíz queda trackeado por ahora (es insumo de la Task 3; esa tarea lo mueve a assets y entonces también se destrackea y se agrega a `.gitignore`).
- [ ] **Step 2: README dev setup** — agregar a la sección de desarrollo: tras clonar, `go run ./cmd/orq-lite init` regenera `team.json`/`prompts/`/`schemas/` (init autodetecta Go → `go test ./...`; `precommit` setea `go vet`).
- [ ] **Step 3: ADR 0004** — documentar la política: `internal/commands/assets/` es canónico; la config raíz es runtime generado y nunca se trackea; los `examples/` sí shippean configs completas trackeadas (excepción existente en `.gitignore`).
- [ ] **Step 4: Verificar clone fresco** — `git clone` a un dir temporal + `go build ./... && go run ./cmd/orq-lite init && go run ./cmd/orq-lite doctor` → doctor en verde (depende de Task 1 para `intake.md`).
- [ ] **Step 5: Commit** — `fix(repo): untrack stale dogfooding config; embedded assets are the single source of truth`

### Task 3: `init` scaffoldea `flows.json` + los flows por defecto

**Files:**
- Create: `internal/commands/assets/flows.json` (con los flows `factory`, `factory_fast`, `factory_fast_governed`, `pr_review`, `issue_fix` — los tres últimos se completan en Fase 3; en esta tarea se copian los actuales de raíz + placeholders funcionales de examples)
- Modify: `internal/commands/initcmd.go` (escribir `flows.json` si no existe), embed directive
- Test: `internal/commands/initcmd_test.go`

**Interfaces:**
- Produces: tras `init`, `orq-lite flow run <name>` encuentra `flows.json` en el proyecto.

- [ ] **Step 1: Test** — `TestInitScaffoldsFlows`: tras `Init`, existe `flows.json`, parsea con `engine.LoadFlows` y contiene al menos `factory` y `factory_fast`.
- [ ] **Step 2: Implementar** — copiar `flows.json` raíz a assets, agregar al embed, escribirlo en `Init` (no sobrescribir si existe, mismo criterio que `team.json`).
- [ ] **Step 3: Commit** — `feat(commands): scaffold flows.json on init`

### Task 4: `watch` deja de tragar errores + `run` avisa con task list vacía

**Files:**
- Modify: `internal/watch/watch.go:134-171` (loop), `internal/commands/watchcmd.go`
- Modify: `internal/commands/runcmd.go:226`
- Test: `internal/watch/watch_test.go`, `internal/commands/runcmd_test.go`

**Interfaces:**
- Produces: evento `watch_tick_error{error, consecutive}` en el eventlog; `watch` sale con error tras `N=10` ticks consecutivos fallidos; `run` imprime `no tasks found (run 'orq-lite plan plan.md' first)` y sale 1 si no hay tasks.

- [ ] **Step 1: Test watch** — fake `Lister` que devuelve error; assert de que `Run` emite el error por el callback/logger y aborta tras 10 consecutivos (inyectar umbral para test).
- [ ] **Step 2: Implementar** — reemplazar `_ = tick(...)` por manejo: log a stderr + evento; contador de fallos consecutivos, reset en tick OK.
- [ ] **Step 3: Test run** — tasks.json ausente → error con mensaje, sin loop silencioso.
- [ ] **Step 4: Commit** — `fix(watch,run): surface tick errors and empty task list`

### Task 5: Archivar el todo histórico y limpiar `tasks/`

**Files:**
- Move: `tasks/todo.md` → `docs/archive/2026-05-pyorquesta-bootstrap-plan.md`
- Move: `tasks/todo-production-upgrade.md`, `todo-rollback-safety.md`, `todo-log-rendering.md` → `docs/archive/` (están completos)

- [ ] **Step 1:** `git mv` los cuatro archivos, agregar nota de una línea al inicio de cada uno ("Archivado: plan histórico completado").
- [ ] **Step 2: Commit** — `docs: archive completed historical task plans`

---

## Fase 1 — Trazabilidad completa

### Task 6: Run ID + directorio por corrida

**Files:**
- Create: `internal/runid/runid.go`, `internal/runid/runid_test.go`
- Modify: `internal/commands/runcmd.go` (`newLiveDeps`), `factorycmd.go`, `flowcmd.go`, `plancmd.go`
- Modify: `internal/eventlog/eventlog.go`

**Interfaces:**
- Produces: `runid.New() string` → `"r20260701T193000Z-4f2a"` (UTC compacto + 2 bytes random hex); `eventlog.Logger.SetRunID(id string)` que estampa `run_id` en todo evento; eventos `run_start{run_id, command, args, version}` y `run_end{run_id, status, duration_s}`; directorio `.orquestalite/runs/<run_id>/` creado al inicio con `manifest.json` (`{run_id, command, started_at, orq_version, team_hash}`).

```go
// internal/runid/runid.go
package runid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func New() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("r%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b))
}
```

- [ ] **Step 1: Test runid** — formato con regex `^r\d{8}T\d{6}Z-[0-9a-f]{4}$`, dos llamadas ≠.
- [ ] **Step 2: Test eventlog** — tras `SetRunID("rX")`, todo `Log()` persiste `run_id: "rX"` en el JSONL.
- [ ] **Step 3: Implementar** — campo `runID string` en `Logger`; en `Log()` agregarlo junto a `ts`/`event` (`eventlog.go:84`).
- [ ] **Step 4: Wire** — en cada comando que abre el eventlog (`newLiveDeps`, `RunFlow`, factory): generar run_id, `SetRunID`, emitir `run_start` al comienzo y `run_end` en defer (status `ok|error|interrupted`), crear `.orquestalite/runs/<id>/manifest.json`.
- [ ] **Step 5: Commit** — `feat(eventlog): run IDs, run_start/run_end events, per-run manifest`

### Task 7: Emitir los eventos fantasma del dashboard

`task_start`, `task_failed`, `cycle_start`, `cycle_end` se renderizan en `index.html:219-223` y en el `log` viewer pero nunca se emiten. Además de arreglar el dashboard, estos eventos son los delimitadores estructurales que la proyección SQLite (Task 11b) necesita para armar timelines por task/ciclo consultables por la app acompañante.

**Files:**
- Modify: `internal/loops/task.go` (task_start al tomar la task, task_failed en fallo), `internal/loops/review.go` (cycle_start/cycle_end), `internal/commands/runcmd.go` (threading del logger si hace falta)
- Test: `internal/loops/*_test.go` con fake event sink

**Interfaces:**
- Produces: `task_start{task_id, title, squad, attempt}`, `task_failed{task_id, failure_reason, attempts}`, `cycle_start{cycle}`, `cycle_end{cycle, tasks_done, tasks_failed, should_stop}`.

- [ ] **Step 1: Test** — fake sink acumula eventos; correr un task loop con deps fake y assert de la secuencia `cycle_start → task_start → ... → task_done|task_failed → cycle_end`.
- [ ] **Step 2: Implementar** los 4 emit points.
- [ ] **Step 3: Renderers** — casos `human` para los 4 tipos en el renderer del eventlog y en `logcmd.go`.
- [ ] **Step 4: Commit** — `feat(loops): emit task/cycle lifecycle events`

### Task 8: Persistir artefactos por invocación (prompt + salida completa)

**Files:**
- Create: `internal/artifacts/artifacts.go`, `internal/artifacts/artifacts_test.go`
- Modify: `internal/invoke/role.go` (punto donde se construye el prompt final y donde vuelve `runner.Result`)

**Interfaces:**
- Produces:

```go
package artifacts

type Store struct{ Root string } // Root = .orquestalite/runs/<run_id>

// Dir devuelve runs/<id>/agents/<taskKey>/<role>.c<cycle>.a<attempt>[.rN]/
func (s *Store) Dir(taskKey, role string, cycle, attempt int) (string, error)

// SaveInvocation escribe prompt.md, stdout.log, stderr.log, meta.json
// (agent, model, provider, duration_s, exit_code, session_id, cmd_line).
func (s *Store) SaveInvocation(dir string, inv Invocation) error
```

- `invoke.RoleInvoker` gana campo opcional `Artifacts *artifacts.Store`; si es nil, no-op (compat).
- El evento `agent_run` gana campo `artifacts_dir` (ruta relativa) para que el dashboard/log los encuentre.

- [ ] **Step 1: Tests del store** — escribe y lee los 4 archivos; colisión de rerun agrega sufijo `.r2` (mismo criterio que `rerunArchivePath` en results).
- [ ] **Step 2: Wire en role.go** — capturar el prompt final ya interpolado (memoria + vars + skills incluidos) justo antes de `inv.run()`, y `Result.Stdout/Stderr` completos al volver; escribir vía Store. Guardar solo si `Artifacts != nil`.
- [ ] **Step 3: Retención** — `Store.Prune(keepRuns int)` que borra los runs más viejos cuando hay más de `keepRuns` (default 20, configurable en `team.json` `limits.keep_runs`); llamado en `run_start`.
- [ ] **Step 4: Commit** — `feat(invoke): persist full prompt/stdout/stderr artifacts per agent invocation`

### Task 9: Diff por agente/intento

**Files:**
- Modify: `internal/gitx/gitx.go` (agregar `DiffWorktree(dir string) (string, error)` = `git add -A -N && git diff HEAD` — `-N` para incluir untracked nuevos sin stagearlos)
- Modify: `internal/loops/fix.go` (tras cada invocación de coder/generalist exitosa), `internal/engine/runners.go` (tras cada agent step cuyo rol esté en un set `writesCode`)
- Test: `internal/gitx/gitx_test.go`

**Interfaces:**
- Produces: `attempt.diff` dentro del directorio de artefactos de la invocación (Task 8); evento `agent_diff{task_id, role, attempt, files_changed, insertions, deletions, artifacts_dir}`.

- [ ] **Step 1: Test gitx** — repo temporal, archivo modificado + archivo nuevo untracked → `DiffWorktree` contiene ambos; worktree limpio → string vacío.
- [ ] **Step 2: Wire fix loop** — después de cada `coder` que retorna `completed`, capturar diff contra el snapshot base de la task (`taskBaseSHA` ya existe en `captureRollbackBase`, `runcmd.go:342`): `git diff <taskBaseSHA>` da el acumulado y `DiffWorktree` el pendiente; guardar el acumulado como `attempt.diff`.
- [ ] **Step 3: Dashboard** — endpoint `GET /api/attempt-diff/{task}/{role}/{cycle}/{attempt}` que lee el artefacto (whitelist de path components, sin traversal); link desde el modal de task existente.
- [ ] **Step 4: Commit** — `feat(trace): per-attempt diffs captured as artifacts and served in dashboard`

### Task 10: Costo first-party (sin depender de agtop)

**Files:**
- Modify: `internal/providers/claude.go`, `internal/providers/gemini.go` (parsear usage del stream: claude emite `usage` en el result event del stream-json; gemini en su formato análogo)
- Modify: `internal/invoke/role.go` (campos `input_tokens`, `output_tokens` en `agent_run` cuando el provider los emite)
- Modify: `internal/cost/cost.go` (Rollup usa tokens del run.log cuando existen; agtop pasa a enriquecimiento para USD, con tabla de precios embebida `internal/cost/prices.go` como fallback)
- Test: `internal/providers/*_test.go` con fixtures JSONL reales, `internal/cost/cost_test.go`

- [ ] **Step 1: Fixtures** — capturar líneas JSONL reales de `claude --output-format stream-json` y gemini con usage; tests de `ParseLine` → `EventUsage`.
- [ ] **Step 2: Wire** — tokens al evento `agent_run`; `orq-lite cost` muestra tokens siempre y USD cuando hay precio (embebido o agtop).
- [ ] **Step 3: Warning** — cuando agtop no está y hay runs sin precio, `cost` lo dice explícitamente (ya existe la nota; extender a "install agtop or update embedded prices").
- [ ] **Step 4: Commit** — `feat(cost): first-party token capture from provider streams`

### Task 11: `task_id` en todos los `agent_run` + `log` agrupado por task

Follow-up conocido de `todo-log-rendering.md`.

**Files:**
- Modify: `internal/invoke/role.go:446-477` (asegurar `task_id` presente en roles per-cycle: parser/reviewer/planner usan clave sintética `"-"` o el feature ID), `internal/commands/logcmd.go` (`--task T003` filtro + agrupación visual)
- Test: `internal/commands/logcmd_test.go`

- [ ] **Step 1: Test** — log fixture con 3 tasks → `log --task T002` muestra solo esos agent_runs.
- [ ] **Step 2: Implementar + commit** — `feat(log): task filter and grouped rendering`

### Task 11b: Proyección SQLite de eventos + API de consulta en `serve`

**Contexto y decisión (2026-07-01):** orq-lite va acompañado de otra app que necesita consultar historia entre corridas. Decisión del usuario: el contrato de consumo es la **HTTP API de `serve`**; el schema SQLite queda interno y libre de evolucionar. Arquitectura: `run.log` (JSONL) sigue siendo la única fuente de verdad de escritura — crash-safe, append-only, sin locks en el hot loop —; SQLite es un **modelo de lectura derivado e incremental, reconstruible desde el log** en cualquier momento (elimina dual-write y migraciones: drop + rebuild).

**Files:**
- Create: `internal/eventdb/eventdb.go`, `internal/eventdb/ingest.go`, `internal/eventdb/eventdb_test.go`
- Modify: `internal/web/server.go` (endpoints de consulta), `cmd/orq-lite/main.go` (`orq-lite index [--rebuild]`), `go.mod` (`modernc.org/sqlite`)
- Create: `docs/query-api.md` (contrato para la app acompañante)

**Interfaces:**

```go
package eventdb

// Open abre/crea .orquestalite/orq.db (WAL mode, PRAGMA user_version=1).
func Open(path string) (*DB, error)

// Ingest lee run.log desde el offset guardado en ingest_state y materializa
// filas nuevas. Idempotente; tolera rotación (offset > tamaño → reprocesa
// también los run-*.log.gz no vistos) . Nunca se llama desde el hot loop.
func (d *DB) Ingest(logPath string) (added int, err error)

// Rebuild borra todas las tablas y reingesta desde cero.
func (d *DB) Rebuild(logPath string) error
```

Schema v1 (interno, no contrato):
- `runs(run_id TEXT PK, command, started_at, ended_at, status, orq_version)` ← eventos `run_start`/`run_end` (Task 6)
- `events(id INTEGER PK, run_id, ts, type, task_id, role, agent, fields_json)` ← todo evento, con los campos comunes promovidos a columnas indexadas y el resto en `fields_json`
- `agent_runs(id INTEGER PK, run_id, ts, task_id, role, agent, provider, model, cycle, attempt, duration_s, exit_code, session_id, input_tokens, output_tokens, fallback_reason, artifacts_dir)` ← proyección tipada de `agent_run` (consume Tasks 6, 8, 10)
- `ingest_state(file TEXT PK, offset INTEGER)`

Endpoints nuevos en `serve` (read-only, el contrato público — documentado en `docs/query-api.md`):
- `GET /api/runs` (lista paginada), `GET /api/runs/{id}` (manifest + resumen)
- `GET /api/runs/{id}/events?type=&task_id=` (timeline)
- `GET /api/agent-runs?run_id=&task_id=&role=&agent=`
- `GET /api/stats/cost?by=run|task|agent`

Trigger de ingesta: ticker de 1s dentro de `serve` (mismo patrón que `logTail`, fuera del proceso del run) + comando manual `orq-lite index [--rebuild]` para uso headless/CI.

- [ ] **Step 1: Test de schema+ingest** — fixture JSONL con `run_start`, `agent_run`, `task_done`, `run_end` → `Ingest` puebla las 3 tablas; segunda llamada a `Ingest` con el mismo archivo no duplica filas (offset).
- [ ] **Step 2: Test de rotación y rebuild** — simular log truncado + `.log.gz` rotado → `Ingest` no pierde eventos; `Rebuild` reproduce el mismo contenido.
- [ ] **Step 3: Implementar eventdb** — `modernc.org/sqlite`, WAL, `user_version=1`; verificar `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` (constraint del release).
- [ ] **Step 4: Endpoints en serve** — tests con `httptest` (filtros, paginación, 404 de run inexistente); wire del ticker de ingesta.
- [ ] **Step 5: `orq-lite index`** — comando + test; `doctor` reporta la DB (existencia, versión, filas).
- [ ] **Step 6: docs/query-api.md** — documentar cada endpoint con ejemplos de respuesta para la app acompañante.
- [ ] **Step 7: Commit** — `feat(eventdb): sqlite read-model projection of run.log + query API in serve`

---

## Fase 2 — Endurecimiento del engine (flows dinámicos)

> Esta fase merece su propio plan detallado al ejecutarla. Contratos y decisiones cerradas:

### Task 12: Validación de flows al cargar

**Files:**
- Create: `internal/engine/validate.go`, `internal/engine/validate_test.go`
- Modify: `internal/commands/flowcmd.go` (validar post-load, error antes de ejecutar nada)

**Interfaces:**
- Produces: `engine.Validate(f Flow, known KnownSet) []error` donde `KnownSet{Roles map[string]bool, Actions map[string]bool}`. Reglas: (1) `type` ∈ {agent,command,action,loop,retry_until,eval}; (2) `agent` referencia rol existente en team.json; (3) `action` ∈ BuiltinActions; (4) exactamente uno de `command`/`args` en command steps; (5) `loop` requiere `iterator` y `as`; (6) `retry_until` requiere `condition`; (7) inputs requeridos del flow (`InputSpec.Required bool` nuevo) presentes o con default; (8) `on_failure` ∈ {"", "continue"}; (9) warning (no error) por steps `agent` sin `outputs` cuyo resultado se referencia después — y **error** si un `eval`/`retry_until` referencia un path que ningún `outputs` anterior produce (análisis estático de tokens `{...}` contra outputs declarados + inputs).
- La regla 9 habría atrapado el bug de `fastapi-governed` (tester/critic sin outputs).

- [ ] Tests tabla-driven por regla → implementar → validar los flows bundled y examples contra el validador → commit `feat(engine): static flow validation at load time`.

### Task 13: `invalid_contract` dentro del fallback loop (ADR-0003 Phase 3)

**Files:**
- Modify: `internal/invoke/role.go` (mover parse+validación adentro del ciclo de agentes), `internal/invoke/classify.go` (nueva razón `"invalid_contract"`), `internal/fallback/fallback.go` (contarla como fallo elegible para fallback)
- Test: `internal/invoke/role_test.go` — agente primario escribe JSON inválido → se invoca el fallback; ambos inválidos → `ErrAllAgentsFailed` con razón `invalid_contract`.

- [ ] TDD → commit `feat(invoke): route contract validation failures through fallback chain`.

### Task 14: Control de flujo mínimo viable: `when`, `break_if`, `timeout`, `sleep` entre retries

**Files:**
- Modify: `internal/engine/engine.go` (campos `When string`, `TimeoutSeconds int` en `Step`; `BreakIf string` en loop; `RetryDelaySeconds int` en retry_until), `internal/engine/runners.go`
- Test: `internal/engine/engine_test.go`

**Interfaces:**
- `when`: expresión evaluada con `evalExpr` antes del step; falsa → skip con evento `step_skipped`. Aplica a todos los tipos.
- `break_if`: evaluada al final de cada iteración del loop; verdadera → corta.
- `timeout_seconds` por step: `context.WithTimeout` alrededor del step (los agentes ya tienen timeout propio; esto cubre `command`).
- `retry_delay_seconds`: sleep entre intentos de `retry_until`.
- **Decisión: NO agregar `parallel` en v1** (los agentes compiten por el mismo worktree; paralelismo requiere worktrees aislados — anotado como futuro).

- [ ] TDD por primitiva → actualizar validador (Task 12) → commit `feat(engine): when/break_if/timeout/retry_delay control flow`.

### Task 15: Persistencia de estado del flow + `flow resume`

**Files:**
- Create: `internal/engine/state.go` (checkpoint del Context + program counter jerárquico `[]int` tras cada step top-level e iteración de loop, escrito atómicamente a `.orquestalite/flow-state.json` con `{flow, run_id, pc, context}`)
- Modify: `internal/commands/flowcmd.go` (`flow resume` recarga y salta al pc guardado; `flow run` con estado previo del mismo flow pregunta o exige `--force`)
- Test: engine test que mata la ejecución a mitad (step que retorna error) y resume desde el siguiente.

**Constraint:** el Context serializado solo contiene `map[string]any` JSON-safe (ya es así: resultados de agentes y command outputs).

- [ ] TDD → commit `feat(engine): flow state checkpointing and resume`.

### Task 16: Acciones nativas de alta fidelidad (puente loops → engine)

Es la tarea que cierra el gap G1 sin reescribir `loops/`.

**Files:**
- Modify: `internal/engine/builtins.go` + `internal/commands/flowcmd.go` (inyectar acciones que necesitan `liveDeps`)
- Test: engine tests con deps fake.

**Interfaces (acciones nuevas):**
- `run_task_loop{tasks_path}` → ejecuta `loops.RunTaskLoopWithContext` con toda la maquinaria (squads, decomposición, escalation, full-suite, commits); output `{tasks_done, tasks_failed, failed_ids}`.
- `plan_tasks{plan_text, append}` → parser + escritura de tasks.json (reutiliza plancmd).
- `full_suite{}` → corre `full_test_command` con la semántica corregida `sh -c` (ver Task 22); output `{pass, output_tail}`.
- `git_branch{name, base}`, `git_merge_to_base{branch, base}` (reutilizan `gitx` con checkpoint-residue).
- `extract_features_llm{features_text}` → usa el planner LLM (elimina la divergencia heading-splitter vs planner entre `flow run factory` y `orq-lite factory`).

- [ ] Una sub-tarea TDD por acción → actualizar `flows.json` bundled `factory`/`factory_fast` para usarlas → commit por acción.

---

## Fase 3 — Los 3 flows por defecto, de `init` a producción

### Task 17: Flow bundled `factory_fast_governed` (factory fast + PM/Architect/QA)

**Files:**
- Modify: `internal/commands/assets/flows.json` (flow nuevo), `internal/commands/assets/team.json` (roles `pm`, `architect`, `qa` con fallbacks)
- Create: `internal/commands/assets/prompts/pm.md`, `architect.md`, `qa.md` (portar y pulir desde `examples/fastapi-governed/prompts/`), `internal/commands/assets/schemas/governance.json` (`{status: approved|changes_requested, findings[], new_tasks[], notes_for_memory}`)
- Test: `internal/engine/flows_bundled_test.go` con `AgentRunner` fake

**Diseño del flow (corrige los bugs del ejemplo):**
1. `extract_features_llm` → loop de features con `run_task_loop` (acción de Task 16 — hereda toda la calidad del pipeline).
2. Al final: `retry_until {architect_res.pass} && {qa_res.pass} && {pm_res.pass}` (max 3, delay 0):
   - `agent architect` (inputs: diff acumulado de la corrida, features text) → `outputs: {architect_res: "."}`
   - `agent qa` (corre la suite vía `full_suite` primero, recibe su output) → `outputs: {qa_res: "."}`
   - `agent pm` (valida contra features.md) → `outputs: {pm_res: "."}`
   - `when: "!({architect_res.pass} && {qa_res.pass} && {pm_res.pass})"` → `agent coder` con el feedback de los tres concatenado.
3. **Todo step de agente tiene `outputs`** (regla del validador de Task 12).

- [ ] Test del flow completo con fakes (aprueba a la 2da vuelta; verifica que el coder recibió el feedback) → wire en assets/init → probar `orq-lite flow run factory_fast_governed` end-to-end en un repo de juguete → commit `feat(flows): bundled factory_fast_governed with PM/Architect/QA gate`.

### Task 18: Flow bundled `pr_review` + watch ruteado a flows

**Files:**
- Modify: `internal/commands/assets/flows.json` (portar `examples/pr-review/flows.json` corregido: `on_failure` explícito en checkout; test/lint leyendo `{inputs.test_command}` con default desde team.json en vez de `go test` hardcodeado)
- Create: `internal/commands/assets/prompts/security_auditor.md`, `review_lead.md` + schemas
- Modify: `internal/commands/watchcmd.go` — flags `--pr-flow <name>` (default `pr_review`) y `--issue-flow <name>` (default `issue_fix`); los triggers llaman `RunFlow(name, inputs)` en lugar de `commands.Review`/`Intake`. Mantener `--legacy` para el comportamiento anterior durante una versión.
- Test: `internal/commands/watchcmd_test.go` con Lister fake → assert de que se invoca RunFlow con `pr_number`.

- [ ] TDD → commit `feat(watch): route PR/issue triggers through the flow engine`.

### Task 19: Plantilla de GitHub Action + docs de despliegue en server

**Files:**
- Create: `internal/commands/assets/github/orq-pr-review.yml`, comando `orq-lite init --github` que lo escribe a `.github/workflows/`
- Create: `docs/deploy.md` (los 3 modos del assessment §3: Action efímera, daemon en server con systemd unit + compose de ejemplo, dispatch futuro)

**Contenido de la Action (completo):**

```yaml
name: orq-lite PR review
on:
  pull_request:
    types: [opened, synchronize, reopened]
permissions:
  contents: read
  pull-requests: write
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Install orq-lite
        run: |
          VERSION=v0.2.0  # pin
          curl -sSL -o orq.tar.gz \
            "https://github.com/lionelchamorro/orquesta-lite/releases/download/${VERSION}/orq-lite-${VERSION}-linux-amd64.tar.gz"
          tar -xzf orq.tar.gz && sudo mv orq-lite-*/orq-lite /usr/local/bin/
      - name: Install agent CLIs
        run: npm install -g @anthropic-ai/claude-code @openai/codex
      - name: Scaffold config
        run: orq-lite init --lang auto
      - name: Run PR review flow
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
          GH_TOKEN: ${{ github.token }}
        run: orq-lite flow run pr_review pr_number=${{ github.event.pull_request.number }} publish=true
      - name: Upload trace artifacts
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: orq-trace
          path: .orquestalite/runs/
```

- [ ] Escribir Action + `init --github` con test de scaffolding → `docs/deploy.md` con systemd unit y compose para `watch` → commit `feat(ci): shipped GitHub Action template and server deployment docs`.

### Task 20: Flow bundled `issue_fix`

**Files:**
- Modify: `internal/commands/assets/flows.json` (portar `examples/issue-fix/flows.json` con fixes), assets prompts `triage.md`, `reproducer.md` + schemas
- Fix del gap del reproducer: tras el step `reproducer`, agregar step `command` que corre el test nuevo y `eval` que **exige que falle** (`{repro_run.pass} == false`) antes de entrar al retry del coder; si pasa de entrada, `agent triage` re-evalúa o el flow termina con comentario "no reproducible".
- Test: engine test con fakes cubriendo: issue no accionable (work_queue vacío → solo comenta), issue accionable happy path, reproducer que no reproduce.

- [ ] TDD → commit `feat(flows): bundled issue_fix flow with reproduction gate`.

### Task 21: `flow list` + docs de flows

**Files:**
- Modify: `internal/commands/flowcmd.go` (`flow list` tabular: nombre, descripción, inputs con defaults, roles requeridos + estado del preflight de cada rol), `cmd/orq-lite/main.go` (usage)
- Create: `docs/flows.md` — referencia completa del schema de flows.json (steps, interpolación, outputs, when/break_if, validación, cómo escribir un flow propio, cómo registrar roles custom en team.json)
- Test: `flowcmd_test.go`

- [ ] TDD → commit `feat(flow): flow list command and flows.json reference docs`.

---

## Fase 4 — Recovery framework + medic (spec ya aprobado)

### Task 22: Implementar `internal/recovery` + rol `medic` según `docs/superpowers/specs/2026-06-30-recovery-framework-medic-design.md`

**Files:** los del spec — Create: `internal/recovery/recovery.go` (+test), `internal/commands/assets/prompts/medic.md`, schema medic; Modify: `internal/gitx/gitx.go` (`ErrMergeConflict` sentinel), `internal/factory/engine.go` (branch de merge-failure ~línea 164).

- [ ] Ejecutar el plan del spec tal cual está escrito (ya tiene el diseño cerrado; backward-compatible si no hay rol `medic` configurado).
- [ ] Incluir el fix relacionado: `CheckpointResidue` también en el path de merge-conflict (gap §5.9 de codebase-analysis).
- [ ] Commit — `feat(recovery): merge-conflict recovery with medic role`

---

## Fase 5 — Bugs conocidos + pulido final

### Task 23: Batch de correctness bugs de codebase-analysis

Uno por commit, cada uno con test primero:

- [ ] `coder.blocked` → el fix loop no avanza al tester; marca la task `needs_clarification` con el summary del coder (`loops/fix.go:148`).
- [ ] Reviewer gateado contra verifier: si el verifier del ciclo falló, `should_stop=true` del reviewer no detiene (`loops/review.go:71-73`).
- [ ] `FullSuite` con `sh -c` para soportar pipelines (`runcmd.go:367-391`), consistente con trust-but-verify.
- [ ] `tasks.Save`/`factory.Save` atómicos (tmp + rename, mismo patrón que `sessions.go:88`).
- [ ] Guard de `liveDeps.currentTask` no encontrado (`runcmd.go`, §6 codebase-analysis).
- [ ] `ErrDecomposeInvalidCount` separado de `ErrNoDecomposer`.
- [ ] Validación de `squad` del parser contra valores conocidos (default `full` + warning).
- [ ] Detección de auth de opencode en `authPromptRe` + credencial env var si existe.
- [ ] `review --team` flag; `serve`/`--serve` con hint de `0.0.0.0` en help; semver compare en `update`.

### Task 24: Refresh de documentación

- [ ] CONTEXT.md: flow engine, comandos completos (`flow`, `watch`, `doctor`, `cost`, `log`, `update`), estados de task nuevos, `lint_failed`, quitar `run plan.md`/`--force` no implementados (o implementarlos — decisión: quitarlos de la doc, YAGNI).
- [ ] README: sección "Default flows" (los 3 con quick start), `flow run`, Action template, link a docs/flows.md y docs/deploy.md.
- [ ] `docs/features.md` / `factory-features.md`: marcar estados reales (implemented/superseded-by-flows).
- [ ] `docs/codebase-analysis.md`: nota de header "hallazgos resueltos en <este plan>".
- [ ] Commit — `docs: sync documentation with flow engine and default flows`

### Task 25: Verificación de release

- [ ] `docker build` + compose smoke test (pendiente de todo-production-upgrade; pinear versiones de CLIs en Dockerfile con build args con default explícito, no `latest`).
- [ ] Dry-run end-to-end de los 3 flows en un repo de juguete (Python y Go) documentando el resultado en `docs/superpowers/specs/2026-07-01-product-readiness-assessment.md` §6 (verificación).
- [ ] `orq-lite doctor` valida `flows.json` (existencia + `engine.Validate`).
- [ ] Tag `v0.2.0`.

---

## Orden de ejecución y dependencias

```
Fase 0 (Tasks 1-5)  → sin dependencias, empezar ya. Desbloquea demo básica.
Fase 1 (Tasks 6-11b)→ Task 8 depende de 6; Task 9 depende de 8; Task 11b depende de 6 y
                      se beneficia de 7/8/10 (columnas tokens/artifacts_dir pueden quedar
                      NULL hasta que esas tareas landeen). Resto independiente.
Fase 2 (Tasks 12-16)→ Task 16 depende de 12; 14-15 independientes entre sí.
Fase 3 (Tasks 17-21)→ depende de Fase 2 (validador + acciones nativas). Task 19 solo depende de 18.
Fase 4 (Task 22)    → independiente de Fases 1-3 (puede paralelizarse).
Fase 5 (Tasks 23-25)→ Task 23 independiente (paralelizable desde el día 1); 24-25 al final.
```

## Self-Review (checklist ejecutado)

- **Cobertura vs objetivo:** factory fast + PM/Architect/QA → Task 17; PR review + daemon + Action/server → Tasks 18-19; issues automáticos → Tasks 1, 18, 20; flows dinámicos → Tasks 12-16, 21; trazabilidad/logs/diffs → Tasks 6-11; consulta para la app acompañante → Task 11b; "qué haría distinto" aplicado → Tasks 16 (convergencia), 18 (watch→flows), 8-9 (artefactos), 10 (costo first-party). Sin gaps detectados.
- **Placeholders:** las fases 2-3 declaran explícitamente que requieren su propio pase de writing-plans al ejecutar; los contratos (firmas, campos, semántica) están definidos aquí.
- **Consistencia de tipos:** `artifacts.Store` (Task 8) es el que consume Task 9; `engine.Validate`/`KnownSet` (Task 12) es lo que usa Task 21 y doctor (Task 25); acciones de Task 16 son las que referencian los flows de Tasks 17-20.
