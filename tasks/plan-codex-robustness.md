# Plan — Robustez de codex y manejo de tareas no resolubles

> **Para agentic workers:** REQUIRED SUB-SKILL: usar `superpowers:executing-plans` o
> `superpowers:subagent-driven-development` para implementar tarea por tarea.

## Contexto y diagnóstico

Durante una sesión de debugging (mayo 2026) detectamos que orq-lite presenta tres modos
de falla *distintos* como el mismo error genérico:

1. **Infra/config del agente** — codex defaulteaba a `sandbox: read-only` en el entorno del
   usuario, lo que hacía estructuralmente imposible escribir
   `.orquestalite/results/coder.json`. El `runner` veía `ResultExists=false` y devolvía
   `"did not write result file"`. El stderr de codex (`Not inside a trusted directory…`,
   header con `sandbox: read-only`) quedaba capturado pero nunca se mostraba.
2. **Instrucción-following del modelo** — codex hacía 36s de trabajo real, escribía código,
   y al final omitía la "última acción" de escribir el JSON del contrato. El orquestador no
   tiene cómo distinguir esto de (1) ni de (3).
3. **Capacidad/ambigüedad de la tarea** — algunas tareas (ej. el plan de
   `Extract assessment & datascience to Bundle Repo`, refactor multi-fase) son
   genuinamente irresolubles para el coder configurado. Hoy se marcan como
   `max_iterations` o `agent_repeated_failure` sin un canal claro para el humano o el
   reviewer.

Además, descubrimos que el fallback chain solo se activa con `RateLimited=true`. Cualquier
otra falla (incluyendo las tres de arriba) mata el role sin probar el siguiente agente.

## Decisiones (cerradas)

| Branch | Decisión |
|---|---|
| Visibilidad | Surface obligatorio del stderr y header de cada agente cuando hay error |
| Fallback | `ResultExists=false` activa el siguiente agente del chain, no es terminal |
| Contrato codex | Usar `-o`/`--output-schema` como mecanismo estructural, no depender de prompt-following |
| Defaults codex | `--sandbox workspace-write` + `--skip-git-repo-check` en el template de `init` |
| Team default | `init` escribe `codex_gpt5_5` como **coder primario**, `claude_sonnet` como fallback (alinear con uso real) |
| Tareas irresolubles | Tres mecanismos en cascada: enriquecer feedback, escalar modelo, descomponer; handoff al humano como último recurso |
| Backwards compat | Sin shims; el código viejo se borra al cierre de cada fase |

## Anclajes del estado actual (qué cambia)

- `internal/runner/runner.go` — `RunAgent` (signature: agregar `Spec.TemplateVars`),
  `Result` (no cambia).
- `internal/commands/runcmd.go:208-248` — `callRole`, manejo del outcome
  (`ResultExists=false` deja de ser terminal; se construye `failure_details`).
- `internal/fallback/fallback.go:65-69` — `Caller.Call` (extender para que el llamador
  decida si avanzar la cadena, no solo en `RateLimited`).
- `internal/loops/*.go` — fix loop: nueva rama "blocked-by-coder" distinta de
  "max_iterations"; escalado y descomposición.
- `internal/results/results.go` — `CoderResult.Status` ya admite `"blocked"`; agregar
  `BlockedReason` opcional y propagar.
- `internal/commands/initcmd.go` (si existe) y prompts de `init` — template `team.json`
  con los flags correctos para codex.
- Nueva carpeta `schemas/` con `coder.json`, `tester.json`, `critic.json`, `reviewer.json`,
  `parser.json` (JSON Schema, embed con `go:embed`).
- Nuevo `internal/handoff/handoff.go` para escribir `tasks/handoff-<task_id>.md` cuando
  todos los remedios automáticos se agotan.

---

## Fase 1 — Visibilidad: dejar de esconder por qué falló

Goal: el primer mensaje que ve el usuario contiene la causa real, no
`"did not write result file"`.

- [ ] En `runner.Result`, agregar campo `LastStderrTail string` (últimos 2 KB de stderr).
      Ya lo tenemos como `Stderr` completo; exponer un helper `Tail(n int)` evita pasar
      megabytes al log.
- [ ] En `runcmd.callRole`, cuando devolvamos error por `ResultExists=false`,
      incluir `res.Tail(2048)` y el `ExitCode` en el mensaje:
      `agent %q (role %q) did not write %s: exit=%d; stderr tail: %s`.
- [ ] En el `agent_run` event del JSONL log, agregar:
  - `stderr_tail` (2 KB)
  - `stdout_tail` (2 KB) — útil para detectar el caso codex-no-cumplió-contrato
  - `cmd_line` (la línea ya con `{{PROMPT}}` reemplazado por `<elided>`) — para auditar
    flags efectivos
- [ ] Si el agente es codex, parsear el header (`workdir`, `model`, `sandbox`,
      `approval`) del stdout y loggearlo como `agent_run.codex_header`. Esto detecta
      configs problemáticas (ej. `sandbox: read-only`) al primer run.
- [ ] **Verify**: provocar un fallo de codex en sandbox read-only, confirmar que el
      mensaje en la terminal y el evento en `run.log` contienen el header con
      `sandbox: read-only`. El usuario debe poder diagnosticar sin pedir ayuda.

## Fase 2 — Fallback que cubre todas las fallas, no solo rate-limit

Goal: si el coder primario falla por *cualquier* causa no terminal, probar el siguiente
agente del chain antes de marcar el role como roto.

- [ ] Renombrar `fallback.Outcome.RateLimited` a un campo más general: agregar
      `Outcome.ShouldFallback bool` y `Outcome.FallbackReason string`
      (`"rate_limit"|"result_missing"|"agent_crashed"|"invalid_contract"`).
      Mantener `RateLimited` para no romper tests, marcarlo deprecated.
- [ ] En `Caller.Call`, avanzar el chain cuando `out.ShouldFallback`. El cooldown solo se
      aplica si `FallbackReason == "rate_limit"`. Las demás razones avanzan sin cooldown.
- [ ] En `runcmd.callRole`, construir `Outcome.ShouldFallback=true` con la razón
      correspondiente cuando `ResultExists=false`, `ExitCode!=0` sin rate limit, o el
      result file existe pero `ParseXxx` falla.
- [ ] Cada intento sigue emitiendo su `agent_run` event con su `failure_reason`. El usuario
      verá la cadena completa de intentos en el log, no un solo intento opaco.
- [ ] Si la cadena se agota sin éxito, ahí sí marcar el role como roto con
      `failure_reason: "all_agents_failed"` y un `failure_details` que liste por agente la
      razón individual.
- [ ] **Verify**: con `coder.agents = ["codex_gpt5_5", "claude_sonnet"]` y codex configurado
      para fallar (sandbox read-only), el run debe completarse vía `claude_sonnet` y el log
      debe mostrar dos `agent_run` events con el primero marcando `result_missing` y el
      segundo `completed`.

## Fase 3 — Contrato estructural con codex (no depender de prompt-following)

Goal: que codex *no pueda* terminar exitosamente sin escribir el contrato. Usar las
features nativas del CLI en vez del bloque "tu última acción es escribir este JSON" en el
prompt.

- [ ] En `runner.Spec`, agregar `TemplateVars map[string]string`. En `RunAgent`, además de
      `{{PROMPT}}`, reemplazar todas las claves del mapa en cada token del `Cmd`.
      Pasarlo desde `runcmd` con `{"RESULT_PATH": role.ResultPath, "ROLE": roleName}`.
- [ ] Crear `schemas/{coder,tester,critic,reviewer,parser}.json` (JSON Schema 2020-12).
      Embed con `//go:embed schemas/*.json` en un paquete nuevo `internal/schemas`.
- [ ] `orq-lite init` materializa `schemas/` al disco del workspace y los referencia desde
      el `team.json` generado.
- [ ] **Actualizar `internal/commands/assets/team.json`** (el template que `init` materializa)
      para incluir `codex_gpt5_5` como coder primario y dejar `claude_sonnet` como
      fallback. Este es el shape exacto que escribe `init`:
      ```json
      {
        "agents": {
          "claude_sonnet": {
            "cmd": ["claude", "-p", "{{PROMPT}}", "--model", "claude-sonnet-4-6"],
            "rate_limit_pattern": "rate_?limit|429|quota"
          },
          "claude_opus": {
            "cmd": ["claude", "-p", "{{PROMPT}}", "--model", "claude-opus-4-7"]
          },
          "codex_gpt5_5": {
            "cmd": ["codex", "exec", "{{PROMPT}}",
                    "--model", "gpt-5.5",
                    "--skip-git-repo-check",
                    "--sandbox", "workspace-write",
                    "-o", "{{RESULT_PATH}}",
                    "--output-schema", "schemas/{{ROLE}}.json"],
            "rate_limit_pattern": "(?i)(429|usage limit reached|rate_?limit_exceeded|quota.*exceeded|usage limit)"
          }
        },
        "roles": {
          "parser":   { "agents": ["claude_opus"],                          "prompt": "prompts/parser.md",   "result_path": ".orquestalite/results/parser.json",   "timeout_seconds": 300 },
          "coder":    { "agents": ["codex_gpt5_5", "claude_sonnet"],        "prompt": "prompts/coder.md",    "result_path": ".orquestalite/results/coder.json",    "timeout_seconds": 900 },
          "tester":   { "agents": ["claude_sonnet", "codex_gpt5_5"],        "prompt": "prompts/tester.md",   "result_path": ".orquestalite/results/tester.json",   "timeout_seconds": 600 },
          "critic":   { "agents": ["claude_opus", "codex_gpt5_5"],          "prompt": "prompts/critic.md",   "result_path": ".orquestalite/results/critic.json",   "timeout_seconds": 300 },
          "reviewer": { "agents": ["claude_opus", "codex_gpt5_5"],          "prompt": "prompts/reviewer.md", "result_path": ".orquestalite/results/reviewer.json", "timeout_seconds": 600 }
        },
        "limits": { "max_review_cycles": 3, "max_fix_iterations": 5 },
        "rate_limit_backoff": { "initial_seconds": 30, "factor": 2, "max_seconds": 1800, "default_pattern": "rate_?limit|429|quota|too many requests" },
        "full_test_command": "go test ./..."
      }
      ```
- [ ] **Detección de codex disponible**: si `codex` no está en `PATH` al ejecutar `init`,
      escribir un warning a stdout (`"codex CLI not found; primary coder will fail until
      installed — see https://…"`) pero **no** cambiar el template. La elección de coder
      primario es una decisión deliberada que el usuario corrige con `which codex` o
      editando team.json.
- [ ] **Actualizar `initcmd_test.go`** para verificar el shape nuevo: existencia de
      `codex_gpt5_5` en `agents`, orden `[codex_gpt5_5, claude_sonnet]` en `coder.agents`,
      y presencia de los flags `--sandbox`, `--skip-git-repo-check`, `-o`,
      `--output-schema`.
- [ ] Ajustar `prompts/*.md` de codex (si se diferencian) para confiar en
      `--output-schema` en vez de repetir el schema en prosa. El bloque de "Output
      contract" pasa a ser una nota corta: *"Tu mensaje final será validado contra el
      schema en `{{RESULT_PATH}}`."*
- [ ] **Verify**: ejecutar el coder con la nueva config sobre una tarea trivial; el
      archivo `.orquestalite/results/coder.json` debe existir incluso si el prompt no
      contiene instrucciones de output, porque codex lo escribe vía `-o`.

## Fase 4 — Tareas que el coder no puede resolver: cascada de remedios

Goal: cuando una tarea está bien definida pero el coder no la resuelve, intentar tres
remedios *antes* de declarar fallo y, si todos fallan, dejarle al humano un handoff
accionable.

### 4.1 — Enriquecer feedback entre iteraciones

- [ ] Cuando el fix loop pierde una iteración, el orquestador ya inyecta
      `tester_feedback` y `critic_feedback` en el siguiente prompt del coder.
      Agregar también:
  - `previous_attempt_summary`: el `summary` que el coder devolvió la iteración anterior.
  - `files_changed_so_far`: lista acumulada de archivos tocados.
  - `iteration_count` explícito.
- [ ] En `prompts/coder.md` agregar bloque "Iteración previa" condicional a `attempt > 1`
      con esos campos.

### 4.2 — Detección de "atascado" sin gastar todo `max_fix_iterations`

- [ ] Calcular hash de `tester.failures` y `critic.concerns` por iteración. Si dos
      iteraciones consecutivas tienen el mismo hash, es `stuck`. Esto ya existe como
      `agent_repeated_failure`, pero solo dispara al final; ahora debe disparar el remedio
      4.3 en cuanto se detecta, no esperar a `max_iterations`.

### 4.3 — Escalado de modelo

- [ ] En `team.json`, permitir `roles[role].escalation_ladder: ["agent_name", …]` opcional.
      Cuando se detecta `stuck` (4.2), invocar el siguiente agente del ladder con todo el
      historial de la tarea inyectado: descripción + intentos previos + último tester +
      último critic.
- [ ] El escalado es ortogonal al fallback de Fase 2: fallback maneja "agente no funciona",
      escalation maneja "tarea es difícil, necesita más capacidad".
- [ ] Default razonable que ofrece `init`: `escalation_ladder` vacío para todos los roles;
      el usuario opta por activarlo. Documentar la decisión.

### 4.4 — Descomposición automática

- [ ] Si la escalada también queda `stuck`, invocar al `parser` con prompt especial:
      `prompts/parser-decompose.md`. Recibe la tarea original + historial + feedback
      acumulado. Output esperado: 2-5 subtareas que reemplazan la original en `tasks.json`.
- [ ] La tarea original se marca `status: "decomposed"` (nuevo valor, no `failed`).
      Las subtareas se procesan en la misma cycle si hay tiempo, o quedan para la próxima.
- [ ] El reviewer al cierre del cycle ve `decomposed` distinto de `failed` y decide.

### 4.5 — Handoff al humano

- [ ] Cuando 4.3 y 4.4 también fallan, escribir `tasks/handoff-<task_id>.md` con:
  - Descripción original de la tarea.
  - Resumen de cada intento (agente, modelo, summary, tester failures, critic concerns).
  - Archivos tocados en total (de todos los intentos).
  - Hipótesis sugerida por el reviewer sobre por qué falló.
  - Comando exacto para reproducir el último intento manualmente.
- [ ] La tarea queda `status: "needs_human"`. El run sigue con las demás tareas.
- [ ] `orq-lite status` muestra una sección "Tareas que requieren intervención humana" con
      los paths a los handoff files.

### 4.6 — `failure_details` enriquecido para distinguir las tres causas

- [ ] Agregar al schema de `tasks.json` un campo `failure_details` (struct) cuando
      `status == "failed" | "needs_human"`:
      ```go
      type FailureDetails struct {
          Reason         string       // enum existente
          AgentChain     []AgentRun   // intentos por agente
          ConfigSuspect  bool         // true si vimos sandbox read-only, model not found, etc.
          ModelSuspect   bool         // true si el modelo respondió pero no cumplió contrato
          TaskSuspect    bool         // true si tester/critic feedback consistente
          LastStderrTail string
      }
      ```
- [ ] Los tres bool son hipótesis del orquestador. El reviewer los lee y decide la
      remediación (proponer fix de config, escalar modelo, descomponer tarea).

## Fase 5 — Pre-flight de tareas (opcional, mejora preventiva)

Goal: cazar tareas mal formadas *antes* de gastar el primer fix loop.

- [ ] Validador liviano corrido al inicio del fix loop por cada tarea:
  - ¿Mencionan archivos que no existen? (Si la descripción cita `src/foo.py`, comprobar.)
  - ¿Mencionan paquetes/imports cuyas dependencies no están en `go.mod`/`pyproject.toml`?
  - ¿Descripción tiene < 50 caracteres? (Heurística de under-specified.)
- [ ] Si falla, marcar `status: "needs_clarification"`, escribir el motivo y pasar al
      reviewer para que la complete o descomponga.
- [ ] **Esta fase es opt-in.** Default off; el usuario activa con
      `limits.preflight_enabled: true`.

---

## Plan de implementación incremental

| Fase | Esfuerzo | Riesgo | Valor inmediato |
|---|---|---|---|
| 1 (Visibilidad) | ~80 LOC + tests | Bajo | Alto — diagnostica casi todo solo |
| 2 (Fallback) | ~120 LOC + tests | Medio (toca el caller) | Alto — desbloquea runs ya configurados |
| 3 (Contrato codex) | ~150 LOC + schemas + tests | Medio (cambia signature de `Spec`) | Medio — solo aplica si usás codex |
| 4 (Cascada) | ~400 LOC + nuevo `handoff` paquete + tests | Alto — más estado, más prompts | Alto pero diferido |
| 5 (Pre-flight) | ~150 LOC + tests | Bajo | Bajo si la calidad de planes es alta |

## Riesgos y verificación

- **Cambio de signature en `runner.Spec`** (Fase 3) rompe tests existentes; mitigar con
  campo opcional y default a `{}`.
- **`Outcome.ShouldFallback` semantics** (Fase 2): equivocarse acá puede crear loops
  infinitos de fallback. Capear con `max_fallback_attempts_per_role` (default = len(chain)).
- **Decomposición automática** (Fase 4.4) puede llenar `tasks.json` de subtareas si el
  parser es agresivo. Capear con `max_decomposition_depth: 2` para que una subtarea
  decompuesta no se vuelva a decomponer infinitamente.
- **Handoff files no commiteados**: agregarlos a `.gitignore` por defecto vía `init`, igual
  que `.orquestalite/`. El usuario decide si los quiere en VCS.
- **Schemas embedded** (Fase 3): hay que verificar que `codex exec --output-schema`
  acepta el path tal cual lo escribimos. Probar con un schema mínimo en isolation antes de
  hacer el plumbing.

## Sugerencia de primer corte (la rebanada que más valor da)

**Fase 1 + Fase 2 juntas** = la diferencia entre "no entiendo por qué falló" y "el log dice
exactamente qué pasó y probó el fallback". Esto solo ya hubiera ahorrado las 5 iteraciones
de debugging que tuvimos hoy.

Fase 3 después, en cuanto querramos seguir usando codex en producción. Fase 4 cuando los
planes que metés sean lo bastante complejos como para que el coder se atasque seguido (el
ejemplo del Bundle Repo es ese caso).

## Notas para la sesión de implementación

- TDD recomendado: para Fase 2 hay tests de `fallback` que sirven de espejo; copiar el
  estilo. Para Fase 3 los tests de runner ya cubren `{{PROMPT}}`; replicar con
  `{{RESULT_PATH}}`.
- Verificar con un `team.json` real y `orq-lite run` end-to-end al cierre de cada fase. No
  solo unit tests.
- ADR a escribir antes de mergear Fase 4: la decisión "escalation vs decomposition vs
  handoff" merece su propio ADR (`docs/adr/0003-task-failure-remediation.md`) porque
  agrega vocabulario nuevo (`decomposed`, `needs_human`, `escalation_ladder`).
