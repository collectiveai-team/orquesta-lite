# orq-lite — Product Readiness Assessment

**Fecha:** 2026-07-01
**Objetivo del producto:** orquestador dinámico de flows multi-agente, con tres flows por defecto listos para usar — (1) factory fast con revisión final PM/Architect/QA, (2) PR review con daemon de pull de PRs (y/o GitHub Action), (3) gestión e implementación automática de issues — más trazabilidad completa (logs, diffs por agente) para dejar orq-lite listo como producto.

---

## 1. Estado actual — qué está implementado y funciona

### Núcleo del orquestador (maduro)
- **Loops anidados review → task → fix** (`internal/loops/`): parser → coder → tester → critic → verifier → reviewer, con escalation ladder, detección de "stuck", decomposición automática de tareas fallidas (ADR-0003 4b) y handoff a humano (`tasks/handoff-<id>.md`).
- **Invocación de agentes por subprocess** (`internal/runner`, `internal/providers`): 4 providers (claude, codex, gemini, opencode) con parsing de stream-JSON, detección de rate-limit con parseo de reset-time, detección de auth interactivo, y modo `cmd` legacy.
- **Fallback y salud** (`internal/fallback`, `internal/agenthealth`): cadena de fallback por rol, cooldown por rate-limit que espera en lugar de abandonar, skip de agentes tras 2 fallos consecutivos, preflight estático de binarios/credenciales.
- **Sesiones** (`internal/sessions`): resume de sesión para coder, namespaced por feature.
- **Rollback seguro** (`gitx.RollbackTo`): reset a SHA base preservando untracked pre-existentes.
- **Memoria compartida** (`.orquestalite/memory.md`) con compactación por rol `compactor`.
- **Factory mode** con integridad multi-feature (merge a base tras gate, repair loop acotado, sesiones namespaced) y squad routing (setup/full/generic + rol `generalist`). Ambos specs implementados y verificados en git log.
- **Dashboard web** (`internal/web`): SSE de eventos, tabla de tasks, diff por task (commit final), cola de factory, costo vía agtop, gameboard.
- **Distribución**: release multi-plataforma con checksums, self-update, Dockerfile 2-stage non-root, docker-compose.

### Flow engine genérico (nuevo, funcional pero incompleto)
`internal/engine/` interpreta `flows.json` con 6 tipos de step (`agent`, `command`, `action`, `loop`, `retry_until`, `eval`), interpolación `{ctx.path}`, `on_failure: continue`, y `derivePass()` que sintetiza `.pass` desde `status`. Se ejecuta con `orq-lite flow run <name> key=value`. Usa el mismo `invoke.RoleInvoker` (hereda fallback, rate-limit, sesiones).

### Los 3 flows pedidos ya existen como *ejemplos*
- `examples/fastapi-governed/` → `factory_governed` (loop de gobernanza architect+qa+pm con retry).
- `examples/pr-review/` → `pr_review` (gh pr diff → tests/lint → critic + security_auditor → review_lead → gh pr comment).
- `examples/issue-fix/` → `issue_fix` (gh issue view → triage → reproducer → retry coder/lint/test/critic → PR).
- `internal/watch/` es un daemon de polling (issues + PRs vía `gh`, cursor + processed-set persistidos) **pero llama a los pipelines hardcoded** (`commands.Intake`/`commands.Review`), no a los flows.

---

## 2. Gaps críticos respecto al objetivo

### G1. Dos vías de ejecución paralelas y divergentes (el gap arquitectónico central)
`run`/`factory` usan los loops hardcoded (con toda la calidad: decomposición, escalation, budget, repair loop, persistencia de estado, resume). `flow run` usa el engine genérico, que **no tiene**: persistencia de estado/resume, decomposición, escalation, budget, full-suite gate ni no-progress guard. El watch daemon puentea a los comandos hardcoded, no a flows. Resultado: los "flows por defecto" que pide el producto corren hoy por la vía de menor fidelidad, y hay lógica duplicada (fix loop vs retry_until; RunFastBatch vs flow factory_fast).

### G2. Engine insuficiente para flows dinámicos de usuario
- **Sin validación**: no hay JSON Schema de flows; `type` inválido y acciones desconocidas fallan en runtime, inputs `type` nunca se valida, campos desconocidos se ignoran silenciosamente.
- **Control de flujo**: no hay `when`/`if`, ni `parallel`, ni `break`, ni timeout por step, ni delay entre retries, ni gates de aprobación humana, ni composición de flows.
- **Sin estado**: crash a mitad de flow = todo perdido; no hay `flow resume`.
- **Contrato inválido no reintenta**: el fallback loop termina apenas *existe* el result file; un JSON malformado o semánticamente inválido es error duro sin probar el agente de fallback (ADR-0003 Phase 3 "invalid_contract" reservado pero no implementado — `classify.go:31`).
- `derivePass()` hardcodea {pass, approved, completed}; `dig()` no indexa arrays; solo 2 acciones nativas sin mecanismo de extensión.
- **Bug real en el ejemplo governed**: los steps `tester` y `critic` no tienen `outputs` (`examples/fastapi-governed/flows.json:55-73`) → sus resultados no se pueden gatear y el flow comitea código con tests rotos.
- Ningún flow de ejemplo tiene test de engine (solo el `flows.json` raíz).

### G3. Trazabilidad parcial
Lo que **sí** se puede reconstruir: agent_run con role/agent/task/cycle/attempt/duración/exit/session_id + tails de 2KB, resultados JSON archivados por `(task, role, cycle, attempt)`, diff del commit final por task, costo por task (si agtop está instalado), lifecycle de tasks con failure_details.

Lo que **falta**:
1. **Sin run_id**: `run.log` es append-only compartido entre corridas, sin evento de inicio/fin de run; no se puede agrupar "qué pasó en la corrida Y".
2. **Prompts no persistidos**: imposible auditar qué se le dijo exactamente al agente en el intento 3 de T007.
3. **stdout/stderr completos descartados** (solo tails de 2KB).
4. **Sin diff por agente/intento**: el fix loop puede iterar N veces el coder; solo sobrevive el estado en el commit final. No se puede ver "qué cambió el coder en el intento 2".
5. **Eventos fantasma**: el dashboard renderiza `task_start`, `task_failed`, `cycle_start`, `cycle_end` pero **ningún código de producción los emite**.
6. `task_id` no está en todos los `agent_run` (follow-up conocido de todo-log-rendering).
7. Costo 100% dependiente de `agtop` externo (sesiones expiran → costo silenciosamente cero); codex/opencode ya emiten `EventUsage` pero no se usa; gemini no parsea usage.
8. Versión del CLI del agente no registrada; memory.md sin timestamps.

### G4. Product readiness (bloqueantes de "instalar y usar")
1. **`prompts/intake.md` no existe** ni en el repo ni en assets embebidos, pero `assets/team.json` lo referencia → `init` scaffoldea un proyecto donde `doctor` falla e `intake`/`watch` (issues) rompen. **Bloqueante del flow de issues.**
2. **`init` no scaffoldea `flows.json`** → `flow run` falla out-of-the-box; los 3 flows del producto viven en `examples/` sin prompts/roles embebidos.
3. **Config de dogfooding en estado híbrido** (no confundir con los assets del paquete): los assets embebidos (`internal/commands/assets/`) están completos y son los que reciben los usuarios vía `init`. El problema es solo del repo de orq-lite: la config de dogfooding raíz (`team.json`, `prompts/`, `schemas/`) está gitignoreada a propósito pero se force-addeó un snapshot viejo y parcial (9 de 14 prompts, `team.json` sin `generalist`) → un clone fresco recibe dogfooding incompleto y el squad generic queda silenciosamente deshabilitado en este repo. Fix: destrackear y regenerar con `init` tras clonar (decisión tomada, ver plan Task 2).
4. **watch traga errores**: `_ = tick(...)` (`watch/watch.go:165`) — un `gh` roto = daemon "sano" que no hace nada, sin log.
5. Sin ejemplo de **GitHub Action** que invoque orq-lite (los workflows del repo son para el desarrollo de orq-lite, no plantillas).
6. `run` sin tasks sale en silencio; `review` no expone `--team`; `--serve` bindea loopback (inaccesible en server); `update` sin comparación semver; `flow run factory` usa el splitter de headings (no el planner LLM) — divergencia no documentada.
7. Docs desactualizadas: CONTEXT.md no menciona el flow engine ni `flow`/`doctor`/`cost`/`log`/`watch`; `docs/features.md` marca como "planned" cosas ya implementadas; `tasks/todo.md` es el plan histórico de "pyorquesta" (3.6k líneas) que confunde.
8. Bugs conocidos de codebase-analysis sin resolver: `coder.blocked` ignorado en fix loop, reviewer no gateado contra verifier failures, `FullSuite` no soporta pipelines de shell, `tasks.Save`/`factory.Save` no atómicos, `CheckpointResidue` ausente en el path de merge-conflict, guard de `liveDeps.currentTask`.

### G5. Recovery framework + rol medic
Spec aprobado (2026-06-30) y no implementado: hoy un merge conflict en factory aborta el merge y bloquea la feature; el diseño de `internal/recovery` + rol `medic` resolvería conflictos automáticamente con verificación por full suite.

---

## 3. Evaluación: PR review vía GitHub Action con orq-lite en un server

Pregunta del producto: ¿se puede conectar una Action con orq-lite corriendo en un server? Sí — hay tres modos de despliegue viables y **no son excluyentes**; recomiendo shippear los tres documentados:

| Modo | Cómo | Pros | Contras |
|---|---|---|---|
| **A. Action efímera** (recomendado como default) | Workflow `pull_request` que instala el binario de releases + CLIs de agentes, corre `orq-lite flow run pr_review pr_number=${{ github.event.number }} publish=true` con `ANTHROPIC_API_KEY`/`OPENAI_API_KEY` en secrets | Cero infraestructura, trigger instantáneo, aislamiento por PR, logs en el job | Costo de setup por run (~1-2 min instalando CLIs; mitigable con imagen Docker publicada), requiere API keys (no OAuth) |
| **B. Server con daemon `watch`** (ya existe) | `orq-lite watch --prs` en un server/container con credenciales OAuth montadas | Ya implementado, sirve para GitHub/GitLab/self-hosted sin permisos de Actions, usa credenciales de suscripción (más barato que API) | Polling (latencia ≤ interval, `--limit 100` puede perder items en repos de alto volumen), requiere supervisión (systemd/k8s), hoy traga errores |
| **C. Action → server (dispatch)** | La Action solo notifica: `repository_dispatch` o webhook HTTP a un endpoint del server; orq-lite procesa | Trigger instantáneo + credenciales del server | Requiere endpoint HTTP nuevo en orq-lite (`serve` hoy es solo dashboard read-only) y exposición de red |

**Recomendación:** implementar A (plantilla de workflow + `init --github`) y endurecer B (watch → flows, logging de errores, unit file/compose de ejemplo). C queda como extensión futura (agregar un receptor `POST /api/trigger/<flow>` autenticado al `serve` es barato una vez que watch rutea por flows, pero no es necesario para v1).

---

## 4. Qué haría distinto (decisiones de diseño)

1. **Converger en el engine, sin reescribir los loops.** No portar toda la lógica de `loops/` a JSON: exponer las capacidades de alta fidelidad como **acciones nativas** del engine (`run_task_loop`, `full_suite`, `merge_to_base`, `create_branch`...). Los flows por defecto componen esas acciones; el engine gana la calidad del pipeline hardcoded sin duplicarla. A largo plazo `run`/`factory` se vuelven azúcar sobre flows bundled.
2. **Watch dispara flows, no comandos.** `watch --pr-flow pr_review --issue-flow issue_fix` con mapeo en config. Un solo camino de ejecución = una sola semántica que testear y trazar.
3. **Directorio por corrida**: `.orquestalite/runs/<run_id>/` con `run.log`, `manifest.json` y artefactos por invocación (prompt, stdout/stderr completos, result, diff). `run.log` global puede quedar como stream agregado para el dashboard, pero la fuente de verdad es por run.
4. **Diff por intento con git**: snapshot barato (`git add -A && git stash create` o `git diff HEAD` a archivo) después de cada invocación de rol que escribe código. Sin worktrees ni complejidad: un diff textual por artefacto.
5. **Costo first-party**: consumir `EventUsage` de los providers (codex/opencode ya lo emiten; agregar parsing a claude/gemini) y tabla de precios embebida; agtop pasa a ser enriquecimiento opcional, no dependencia.
6. **Validación temprana en todo**: schema de flows al cargar, contrato inválido dentro del fallback loop, squad values validados.
7. **Un solo origen de assets**: los archivos embebidos en `internal/commands/assets/` son canónicos; la config de dogfooding raíz es runtime generado por `init` y no se trackea (decisión 2026-07-01).
8. **Proyección SQLite para consumo externo** (decisión 2026-07-01): orq-lite va acompañado de otra app. `run.log` JSONL sigue siendo la única fuente de verdad de escritura; una DB SQLite (`modernc.org/sqlite`, puro Go, compatible con los builds `CGO_ENABLED=0`) se materializa incrementalmente desde el log como modelo de lectura reconstruible, y el contrato público para la app acompañante es la HTTP API de `serve` (plan Task 11b).

---

## 5. Veredicto

La base es sólida y el trabajo duro (invocación robusta de agentes, fallback, factory con integridad multi-feature) ya está hecho. Lo que falta para "producto listo" no es investigación sino **convergencia y acabado**: unificar las dos vías de ejecución, endurecer el engine para flows dinámicos, hacer que los 3 flows por defecto salgan de `init` funcionando y testeados, cerrar el círculo de trazabilidad (run_id + prompts + diffs por intento), y eliminar los bloqueantes de primera experiencia (intake.md, flows.json scaffolding, watch silencioso, Action de ejemplo).

Plan de implementación detallado: `docs/superpowers/plans/2026-07-01-product-readiness.md`.
