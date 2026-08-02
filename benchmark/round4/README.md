# Ronda 4 — re-baseline limpio

Escrito **antes** de lanzar ninguna corrida (a diferencia de rondas
anteriores, donde el protocolo se reconstruyó post-hoc a partir de la
memoria del proyecto). Objetivo: aislar el **modelo** como única variable,
sobre 2 specs ya validados, con `orq-lite` y el pack reconstruidos desde
cero para esta ronda.

## Por qué esta ronda

Las rondas 1–3 acumularon deuda operativa: timeouts subidos en caliente a
mitad de corrida, policies reseteadas manualmente tras abortar, y un bug de
wiring en `adversary.md` descubierto y corregido recién en la ronda 3b. Al
mismo tiempo, `orq-lite` (el binario) siguió cambiando durante todo ese
período — los binarios pinneados en las carpetas de ronda 2/3 son de
mediados de julio, anteriores al merge de "bootstrap-and-governed-pack".
Ronda 4 fija todo lo operativo desde el lanzamiento en vez de parchearlo a
mitad de corrida, y usa un binario recién compilado desde HEAD actual.

## 2 specs × 3 configuraciones = 6 corridas

No son 3 tareas — son 2 (Taskflow, Hookrelay), cada una corrida bajo 3
configuraciones de modelo:

| Config | Coder / Tester / Critic | Todo lo demás (parser, reviewer, ticket_planner, ticket_qa, qa, adversary, integrator, gov_reviewer) |
|---|---|---|
| **A — one-shot** | Sonnet 5 + superpowers, sesión única, sin team.json | n/a |
| **B — ticketed-gpt-sol** | `openai/gpt-5.6-sol` vía opencode | Opus (`claude-opus-4-8`) |
| **C — ticketed-qwen** | `opencode-go/qwen3.7-plus` vía opencode | Opus (`claude-opus-4-8`) |

**Decisión de diseño explícita de esta ronda** (distinta a rondas 2/3, donde
critic iba con Sonnet e integrator con el mismo coder): acá **critic** se
agrupa con coder/tester bajo el modelo nombrado, e **integrator** pasa a
Opus junto con el resto de roles de revisión/cierre. Es deliberado — permite
leer limpiamente "¿qué tan bien codea/critica el modelo X?" vs "¿qué tan bien
arregla Opus el código de X?" como dos preguntas separadas.

Carpetas (`~/Projects/personal/`):
`taskflow-r4-oneshot`, `taskflow-r4-gpt-sol`, `taskflow-r4-qwen`,
`hookrelay-r4-oneshot`, `hookrelay-r4-gpt-sol`, `hookrelay-r4-qwen`.

## Qué NO cambia

- **Specs**: `benchmark/features.md` + `benchmark/CONVENTIONS.md` +
  `benchmark/probe/` para Taskflow; `benchmark/round2/features.md` +
  `benchmark/round2/CONVENTIONS.md` + `benchmark/round2/probe/` para
  Hookrelay. Sin copias nuevas — se referencian los existentes.
- **Pack**: `development/4` (= `benchmark/round3/pack-development-4/`), ya
  con el fix de wiring de `adversary.json` aplicado. Se reusa tal cual.
- **Evaluación**: `benchmark/evaluation.md` / `benchmark/round2/evaluation.md`
  verbatim — mismos probes congelados, misma fórmula de composite.

## Qué SÍ cambia (fijado desde el lanzamiento, no parchado después)

- `coder.timeout_seconds = 2400` desde el día 1 en ambos team.json — el
  ticket de WS/SSE voló el default de 1200s en **todas** las corridas
  anteriores de ambas rondas.
- Policy explícita (`policies/development-r4.json`): `maxAttempts=96`,
  `maxDurationSeconds=86400` (24h) — ronda 2 necesitó subir esto a mano tras
  un abort por presupuesto de tiempo agotado.
- Binario `orq-lite` recompilado desde HEAD actual (`dev-c7df47f`) — mismo
  binario, byte-idéntico, copiado a las 6 carpetas.
- Toda corrida se lanza envuelta en `caffeinate -i` — la Mac durmiéndose de
  noche contaminó el wall-clock de al menos 2 corridas anteriores.

## Lanzamiento

Ticketed (desde la raíz de cada carpeta) — **la policy se copia FUERA del
directorio del pack** (`.orquestalite/development-r4.json`, no
`.orquestalite/packs/development/4/policies/`): `orq-lite` valida que todo
archivo dentro de la carpeta del pack esté listado en `pack.json`, y un
archivo de policy propio ahí adentro rompe esa validación
(`error: pack: unlisted file`). Los flags van INMEDIATAMENTE después del
ref, antes de los `key=value` — `flow run` no acepta `--log-format` (ese flag
es solo del comando legacy `run`):

```sh
nohup caffeinate -i .orquestalite/bin/orq-lite flow run development/factory-governed@4 \
  --policy=.orquestalite/development-r4.json \
  features_path=features.md \
  > /tmp/<carpeta>-launch.log 2>&1 < /dev/null &
disown
```

One-shot (Claude Code + superpowers, prompt verbatim de rondas anteriores):

> Implement every feature in features.md, in order, committing after each
> feature. Follow CONVENTIONS.md. Both gates (`uv run ruff check .`,
> `uv run pytest -q`) must pass after every feature.

## Checklist de pre-vuelo (por carpeta, antes de lanzar)

- [ ] `orq-lite doctor` all green — incluye confirmar que `openai/gpt-5.6-sol`
      resuelve vía opencode (si no resuelve, es blocker previo al
      lanzamiento, no algo a resolver a mitad de corrida)
- [ ] `orq-lite flow list` resuelve `development/factory-governed@4`
- [ ] `team.json`: `coder.timeout_seconds == 2400`
- [ ] policy activa: `maxAttempts=96`, `maxDurationSeconds=86400`
- [ ] gates verdes en HEAD (`ruff check .`, `pytest -q`, suite vacía OK)
- [ ] tree limpio — clon fresh, sin restos de corridas anteriores

## Incidentes (2026-07-23, lanzamiento real)

- **Piloto `hookrelay-r4-gpt-sol`**: convergió T1 a la primera (ticket_qa
  approved:true) — confirmó que `openai/gpt-5.6-sol` resuelve vía opencode.
  Se lanzaron las 5 corridas restantes.
- **Las 4 corridas ticketed murieron ~10min después**, todas con la misma
  ventana temporal pero DOS causas distintas — no asumir que un fallo
  simultáneo comparte causa sin verificar cada una por separado:
  - **qwen (ambas: Taskflow y Hookrelay) — cuota real agotada.**
    `opencode.log` confirma `providerID=opencode-go modelID=qwen3.7-plus
    error="Monthly usage limit reached. Resets in 3 days"`. El coder no
    escribió ni un byte de stdout/stderr en 2400s — atascado contra el muro
    de cuota, no un timeout genuino. **Pausado 3 días** (decisión del
    usuario: no recargar balance).
  - **gpt-5.6-sol (ambas) — NO es cuota** (verificado: cero errores de
    quota/rate-limit para `providerID=openai` en el log). T1-T4 se habían
    aprobado a la primera cada uno; falló recién en T5 (el ticket "pesado"
    de concurrencia/background worker — la misma clase de ticket que voló
    timeouts en rondas 2/3). El `stdout.log` del intento final corta en seco
    a mitad de un step, sin error registrado — agotó los 5 reintentos de
    `max_fix_iterations` resumiendo la misma sesión cada vez. Diagnóstico:
    posible timeout de conexión/HTTP de nivel más bajo que nuestro
    `coder.timeout_seconds` (2400s, nunca llegó a tocarlo — la corrida final
    duró 525s) mientras el modelo razona en modo alto.
- **Fix aplicado**: `opencode models` expone 3 variantes —
  `openai/gpt-5.6-sol{,-fast,-pro}` — sin parámetro de reasoning-effort
  separado en team.json/opencode; el effort se elige vía el ID del modelo.
  Cambiado `team-ticketed-gpt-sol.json` a **`openai/gpt-5.6-sol-fast`**
  (decisión explícita del usuario) — **desviación declarada**: esto
  benchmarquea la variante "fast", no "sol" puro como se acordó
  originalmente. Ambas carpetas gpt-sol reseteadas al scaffold limpio
  (`git reset --hard` + `git clean -fd` + borrado de `.orquestalite/run.log`,
  `runs/`, `results/`, `workflows.db*`) y relanzadas fresh (sesión nueva, sin
  resume) sin qwen compitiendo por recursos de opencode en simultáneo.

## Estado

4 corridas activas ahora mismo: `taskflow-r4-gpt-sol` y `hookrelay-r4-gpt-sol`
relanzadas con `sol-fast`; `taskflow-r4-oneshot` convergió ($7.40, 22min, 5
commits, `benchmark/results/` pendiente de evaluación); `hookrelay-r4-oneshot`
sigue corriendo. `taskflow-r4-qwen` y `hookrelay-r4-qwen` pausadas hasta el
reset de cuota de opencode (~2026-07-26).

## Incidentes (2026-07-27/28, qwen)

Tras el reset de cuota, `taskflow-r4-qwen`/`hookrelay-r4-qwen` se relanzaron
y fallaron **tres veces**, cada una por una causa distinta (no asumir que
fallos repetidos comparten causa):

1. **`invalid_contract` en `coder.json`** (dos corridas, dos campos
   distintos faltantes — `complete` y luego `ticket_id`). Intenté arreglarlo
   agregando una retry rule de `invalid_contract` a la policy; **no tuvo
   ningún efecto** — `agent.invoke@1` es `EffectAtMostOnce`, y
   `scheduler.go` bloquea cualquier retry de policy para ese modo de efecto
   sin importar qué reglas tenga configuradas. Hallazgo documentado en
   `tasks/todo.md` (Fase 5, Task 26) con el fix real propuesto (retry acotado
   vía sesión resumida, no vía policy).
2. **Infra transitoria, no relacionada entre sí**: Taskflow murió por
   `timeout` en T8 (`qwen_coder` colgado sin escribir output, provider
   `opencode` — nada que ver con Claude). Hookrelay murió por `ENOTFOUND`
   (DNS) en `ticket_planner` justo después de T6 aprobado — no coincide con
   la firma de rate-limit confirmada más temprano ese día (`"You've hit your
   session limit"`, HTTP 429, ya resuelta a las 17:00 UTC).
3. **Desviación declarada**: por instrucción explícita del usuario, se pasó
   el agente de soporte (`parser`, `reviewer`, `ticket_planner`, `ticket_qa`,
   `qa`, `adversary`, `integrator`, `gov_reviewer`) de **Opus a
   `claude-haiku-4-5-20251001`** (renombrado `claude_opus` → `claude_haiku`
   en ambos `team.json`) solo en las 2 carpetas qwen, para bajar costo/
   latencia tras la tercera falla. **Esto rompe la comparabilidad de estas 2
   corridas contra el resto de ronda 4** (que usan Opus en esos roles) — sus
   resultados quedan como referencia aparte, no como parte del comparativo
   de 6 condiciones original. Relanzadas fresh (`git stash -u` del código
   parcial de los intentos fallidos, sin resume) el 2026-07-28.
4. **4ta falla, cada carpeta por causa distinta de nuevo**: Taskflow llegó
   hasta el final — los 9 tickets implementados y verificados, y falló recién
   en `final_lint` dentro de `integrated_review` (`gate_failed`, exit 1) a las
   23:59 UTC del 2026-07-28. Hookrelay volvió a colgarse en `timeout` en T4
   (mismo patrón que la 2da falla: `qwen_coder` sin escribir output, ~3h
   colgado). Ninguna de las dos es el bug de `invalid_contract`.
5. **Pivote de modelo (2026-07-30)**: por decisión explícita del usuario, se
   reemplazó **qwen por `opencode-go/minimax-m3`** en `coder`/`tester`/
   `critic` de ambas carpetas (agente renombrado `qwen_coder` →
   `minimax_coder`) — la condición **C** de esta ronda pasa a ser
   "ticketed-minimax", no "ticketed-qwen". Motivo operativo, no solo de
   modelo: el workspace de opencode se quedó sin saldo a mitad de la
   verificación (`Insufficient balance`, bloqueaba tanto qwen como minimax
   por igual) — el usuario recargó y confirmó antes de relanzar. Binario
   reconstruido desde HEAD actual (`dev-fa7d768`, incluye un fix real de
   scoping de sesión por iteración de `while`-loop ausente en el binario
   viejo `dev-c7df47f` usado en todos los intentos anteriores). Relanzadas
   fresh (`git stash -u` de nuevo) el 2026-07-30. La desviación de soporte en
   Haiku (punto 3) se mantiene sin cambios.
6. **5ta falla — nueva firma, mismo síntoma de fondo**: ambas corridas con
   MiniMax M3 fallaron en `implement_ticket` (Taskflow en T6, Hookrelay en
   T2) con `error_class: permanent`, `exit=0` — el coder corrió y (según su
   propio output) pasó tests, pero **nunca escribió
   `.orquestalite/results/coder.json`**. Ni `invalid_contract` (schema mal)
   ni `timeout` (colgado): el agente simplemente no cumplió la instrucción
   final de escribir el archivo. Contando qwen + minimax: **5 fallas
   consecutivas del rol `coder`, 3 firmas distintas** (`invalid_contract` ×2,
   `timeout` ×2, "no escribe el archivo" ×2 — solapan porque hubo 6 intentos
   totales). Las tres firmas comparten la misma causa raíz de fondo: el rol
   no cumplió su contrato de salida y no hay ningún mecanismo de retry real
   (ver `tasks/todo.md` Fase 5, Task 26 — `EffectAtMostOnce` bloquea
   cualquier retry de policy para `agent.invoke@1`). Pausado hasta decidir si
   se prioriza ese fix antes de un 6to intento, o se cierra la condición
   "ticketed-small-model" de ronda 4 como no convergida.
7. **6to intento — segundo pivote de modelo (2026-07-31)**: por decisión
   explícita del usuario, se reemplazó `minimax-m3` por **`gpt-5.6-luna` vía
   `codex`** (no `opencode`) en `coder`/`tester`/`critic` de ambas carpetas
   (agente renombrado `minimax_coder` → `luna_coder`), effort `medium` —
   lanzado en paralelo mientras se diseña (no implementa todavía) el fix
   real de Task 26 en `internal/invoke/role.go` (retry acotado del mismo
   agente vía sesión resumida + prompt correctivo, para las 3 firmas de
   fallo de una sola vez). Mismo binario `dev-fa7d768`, mismo soporte en
   Haiku. Relanzadas fresh (`git stash -u` de nuevo).
   **Resultado**: `luna_coder` funcionó correctamente en las dos — Taskflow
   completó 8/9 tickets, Hookrelay 5/6, sin ningún fallo del propio coder.
   Ambas murieron igual, pero por `invalid_contract` de los roles de soporte
   en **Haiku** (`integrator` escribió una propiedad `"changes"` no permitida
   por el schema; `ticket_qa` escribió `findings[0]` como objeto en vez de
   string) — el mismo bug de fondo, ahora del lado de soporte en vez del
   coder. Confirma que el fix de Task 26 es valioso más allá del rol
   `coder`.
8. **Fix de Task 26 implementado y commiteado (2026-07-31,
   `internal/invoke/role.go`)**: retry acotado (`contractRetryBudget = 2`)
   del mismo agente para `invalid_contract`/`result_missing`, resumiendo su
   sesión efímera con un prompt correctivo — nunca para
   `rate_limit`/`timeout`/`auth_failed` ni roles best-effort. 9 tests nuevos
   + 2 tests existentes actualizados, suite completa verde. Ver
   `tasks/todo.md` Task 26 para el detalle.
9. **7mo intento (2026-07-31)**: mismo `gpt-5.6-luna` vía `codex`, mismo
   soporte en Haiku, binario reconstruido con el fix de Task 26 ya adentro.
   Relanzadas fresh (`git stash -u` de nuevo).
   **CONVERGIERON LAS DOS**:
   - **Taskflow** (`r20260731T170407Z-1621`): `succeeded`, 8/8 tickets, ~40min.
     Todos los steps con 1 solo intento — no hizo falta ningún retry
     correctivo en esta corrida.
   - **Hookrelay** (`r20260731T170413Z-aef0`): `succeeded`, 9/9 tickets,
     ~70min. El retry disparó dos veces y se auto-corrigió ambas: `adversary`
     (intento 1 `invalid_contract` → intento 2, misma sesión resumida,
     válido — encontró una race condition real con fix) y `governance`
     (intento 1 `invalid_contract` → intento 2 válido, `approved:false` con 4
     findings de duplicación de constantes; `governance_repair` los arregló,
     gates verdes, aprobado).

   Cierra la condición "ticketed-small-model" de ronda 4 como
   **"ticketed-luna"** (no qwen, no minimax) después de 7 intentos —
   desviación de soporte en Haiku (punto 3) sigue vigente, documentada como
   no comparable 1:1 contra las otras 4 corridas de la ronda. Pendiente:
   evaluación determinística + jueces LLM de estas 2, igual que las otras 4
   corridas ya convergidas.
