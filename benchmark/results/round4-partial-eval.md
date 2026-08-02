# Ronda 4 — evaluación parcial (8/8 corridas convergidas evaluadas; jueces LLM pendientes)

Ronda 4 aísla el modelo como única variable: mismo `orq-lite` recompilado
desde HEAD (`v0.3.3`, con el fix de session-resume por ticket), mismo pack
`development/4` corregido (fix de `ticket-planner.md`), mismo scaffold
limpio por spec, sobre **2 specs** (Taskflow, Hookrelay) — no 3, "ronda" ≠
"spec" (ver aclaración en la comparativa completa) — y **3 configuraciones
de modelo**: one-shot (Sonnet 5), ticketed gpt-5.6-sol-fast, y ticketed
Sonnet 5 effort=medium (todas coder/tester/critic bajo el modelo nombrado,
resto Opus). Se evaluó la parte **determinística** (checklist por ejecución
+ gates + probe congelado + bug hunt reproducido) de las 6 corridas que ya
convergieron. Los **jueces LLM (L, absoluto+pairwise) quedan diferidos**
hasta que las 2 corridas qwen (pausadas por cuota de opencode, reset
~2026-07-26) estén listas, para juzgar las 8 condiciones de la ronda en una
sola tanda — evita rehacer la matriz pairwise dos veces.

**Incidentes operativos de esta ronda** (documentados en detalle en
`benchmark/round4/README.md`): (1) un bug real de session-resume en orq-lite
(`ForeachKey` vacío en steps anidados de subflow; el fix correcto usa
`ScopePath`) causaba que cada ticket resumiera la sesión del anterior,
inflando tokens cacheados hasta 18× el costo esperado — corregido en
`v0.3.2`→`v0.3.3`, con test de regresión. (2) Un bug determinístico en el
prompt de `ticket-planner.md` (el modelo escribía objetos en vez de strings
en `completed`, violando el schema) — corregido con un ejemplo explícito.
(3) `maxAgentAttempts=48` (heredado sin cambios desde la plantilla de ronda 2,
que ya había documentado el mismo problema) resultó insuficiente para specs
con muchos tickets — subido a 192 en la policy de ronda 4.

**Nota de método**: los dos evaluadores de Taskflow usaron granularidad de
checklist distinta (27 ítems vs 59 ítems) para el mismo `features.md` — no
es una diferencia real de cobertura, ambos llegaron a 100%. El score
normalizado a 30 puntos no se ve afectado, pero los conteos crudos "27/27" y
"59/59" no son comparables entre sí como números absolutos.

## Resultados

| | Checklist | Gates | Probe | Bugs confirmados | Costo | Wall-clock | eff | Q |
|---|---|---|---|---|---|---|---|---|
| **taskflow-r4-oneshot** | 27/27 (100%) | ✅ 35 tests | 14/14 | 2 | $7.40 (completo) | 1328s | **1.00** (nuevo mejor) | **0.9467** |
| **hookrelay-r4-oneshot** | 27/30 (90%) | ✅ 96 tests | 14/15 | 2 | $22.50 (completo) | 7724s | 0.2941 | **0.7566** |
| **taskflow-r4-gpt-sol** (sol-fast) | 59/59 (100%) | ✅ 44 tests | 14/14 | 1 | **$132.05** (real) | 7212s | 0.1201 | **0.7974** |
| **hookrelay-r4-gpt-sol** (sol-fast) | 30/30 (100%) | ✅ 55 tests | 15/15 | 0 | **$146.34** (real) | 7025s | 0.1376 | **0.8275** |
| **taskflow-r4-sonnet5-medium** | 26/27 (96%) | ✅ 40 tests | 14/14 | 1 | $30.49 (real) | 4290s | 0.2761 | **0.8137** |
| **hookrelay-r4-sonnet5-medium** | 30/30 (100%) | ✅ 65 tests | 15/15 | 1 | $62.93 (real) | 8986s activo / 26732s bruto | 0.1542 | **0.8042** |

**Costo corregido (no floor)**: el `run.log` de opencode/orq-lite no factura el
lado gpt-5.6-sol-fast, pero sí registra tokens reales por invocación. Con la
tarifa de OpenAI para GPT-5.6-sol ($5.00/M input, $30.00/M output, sin
descuento de caché aplicado por no tener esa tarifa) + tarifa estándar de
Claude Opus 4 ($15/M input, $75/M output, $1.50/M cache-read — cruzada
independientemente contra la estimación previa del agente evaluador para el
lado Opus de Hookrelay: $51.31 vs $51.31, coincide exacto):

| | gpt-5.6-sol-fast (input+output) | Opus (roles de revisión) | **Total real** |
|---|---|---|---|
| taskflow-r4-gpt-sol | $91.04 (17.9M in / 52k out) | $41.01 | **$132.05** |
| hookrelay-r4-gpt-sol | $95.04 (18.6M in / 64k out) | $51.31 | **$146.34** |

Esto es 18× más caro de lo que sugería el floor inicial para Taskflow. La
corrección **no cambia el ranking de Hookrelay** (gpt-sol sigue ganando,
0 bugs vs 2 del one-shot compensa el costo) pero **sí sharpenea** el de
Taskflow: la ventaja del one-shot (Q 0.9467 vs 0.7974) es mucho más clara de
lo que parecía con el floor — no es solo que el one-shot sea más eficiente,
es que la config ticketed con gpt-5.6-sol-fast es genuinamente cara (10
tickets × ticket_planner+coder+ticket_qa, cada uno reprocesando ~1.5-1.8M
tokens de contexto cacheado por llamada).

`Composite` completo (0.6Q + 0.4L) queda pendiente de L — no calculado
todavía, ver nota de método arriba.

## Detalle por corrida

### taskflow-r4-oneshot (Sonnet 5 + superpowers)

Correctness: 30 (checklist) + 10 (gates) + 10 (probe) + 6 (bugs, 10−2×2) = **56/60**.

**Nuevo mejor conocido de Taskflow en costo y wall-clock** ($7.40/1328s vs
$12.53/3300s histórico de rondas 1-3) — eff=1.0.

Bugs confirmados:
1. **Timestamps naive tras round-trip de aiosqlite** — `DateTime(timezone=True)`
   en el modelo no preserva `tzinfo` al leer de vuelta. Reproducido:
   `assert created_at.tzinfo is not None` falla.
2. **DELETE de job pendiente crashea el flow** — `mark_running` tiene
   `assert job is not None` fuera del `try/except` que envuelve `execute`;
   si el job se borra antes de que corra la task, el `AssertionError` escapa
   sin escribir `status=failed`. Misma familia de bug que Ronda 3
   (crash al borrar job pendiente), reaparece de forma independiente en esta
   build de Sonnet 5.

### hookrelay-r4-oneshot (Sonnet 5 + superpowers)

Correctness: 27 (checklist 90%) + 10 (gates) + 9.33 (probe 14/15) + 6 (bugs) = **52.33/60**.

Costo/tiempo peor que el mejor histórico de Hookrelay ($22.50/7724s vs
$8.84/1509s) — eff=0.29.

Bugs confirmados:
1. **Mismo bug de timestamps naive** que en Taskflow — afecta
   `GET /subscriptions/{id}`, `GET /events/{id}`, `GET /deliveries`; probe
   `test_delivery_happy_path_and_signature` falla explícitamente por esto.
2. **TOCTOU universal de Hookrelay SÍ se coló acá** — 5 requests concurrentes
   a `POST /subscriptions` con el mismo `target_url` devuelven 5×201 en vez
   de 1×201+4×409 (sin constraint de unicidad a nivel DB).

### taskflow-r4-gpt-sol (ticketed, coder/tester/critic = `openai/gpt-5.6-sol-fast`, resto Opus)

Correctness: 30 (checklist 100%) + 10 (gates) + 10 (probe) + 8 (bugs, 10−2×1) = **58/60**.

De las 6 sospechas de la hunt-list histórica, **5 no se reprodujeron**
(timestamps correctos, WS sin leak, shutdown limpio, DELETE de job pendiente
manejado sin crash, filtro WS correcto). La única confirmada:

1. **Bus de eventos global de proceso** (`_event_bus` module-level) en vez
   de scoped a `app.state` — mismo patrón que Ronda 3, pero acá **sin fallas
   de test observables** (el propio evaluador lo marca como desviación de
   diseño, no defecto funcional con impacto probado).

10 tickets (T1-T10), 1 ronda de gobernanza sin rechazos, 36 steps totales.

### hookrelay-r4-gpt-sol (ticketed, coder/tester/critic = `openai/gpt-5.6-sol-fast`, resto Opus)

Correctness: 30 (checklist 100%) + 10 (gates) + 10 (probe) + 10 (bugs, 0 confirmados) = **60/60 perfecto**.

Los 11 targets de la hunt-list estándar **más** el TOCTOU universal de
Hookrelay fueron chequeados uno por uno con reproducción activa — **ninguno
se confirmó**. El TOCTOU específicamente: partial unique index sobre
`target_url` activo + manejo de `IntegrityError`, verificado con 5 requests
concurrentes reales → 1×201+4×409 correcto. Gobernanza aprobó citando que
todos los findings previos de critic/adversary quedaron reparados con test
de regresión propio.

### taskflow-r4-sonnet5-medium (ticketed, coder/tester/critic = `claude-sonnet-5` effort=medium, resto Opus)

Correctness: 28.89 (checklist 96%) + 10 (gates) + 10 (probe) + 8 (bugs, 10−2×1) = **56.89/60**.

De las 5 sospechas de la hunt-list histórica de Taskflow, **4 no se
reprodujeron** (timestamps correctos, WS sin leak, shutdown limpio, DELETE
de job pendiente manejado sin crash — el flow captura la excepción y
retorna `Completed()`). La única confirmada, con repro propia:

1. **Bus de eventos global de proceso** — igual que en `taskflow-r4-gpt-sol`
   y en Ronda 3: `event_bus = EventBus()` a nivel de módulo, compartido entre
   instancias de `create_app()`. Reproducido con leak cross-app real: un
   suscriptor WS en `app1` recibió el evento `job.created` de un job creado
   en `app2` en el mismo proceso.

8 tickets (T1-T8), 1 ronda de gobernanza sin rechazos, 30 steps totales.
Costo real $30.49 ($4.30 Sonnet + $26.19 Opus, 6.1× más caro el lado de
orquestación que el de código) — el más barato de las 3 configuraciones
ticketed evaluadas hasta ahora.

### hookrelay-r4-sonnet5-medium (ticketed, coder/tester/critic = `claude-sonnet-5` effort=medium, resto Opus)

Correctness: 30 (checklist 100%) + 10 (gates) + 10 (probe) + 8 (bugs, 10−2×1) = **58/60**.

De los 11 targets de la hunt-list estándar, **10 no se reprodujeron**. La
única confirmada, con repro propia (5 requests concurrentes → 5×201):

1. **TOCTOU universal de Hookrelay** — el rol `adversary` sí lo había
   encontrado y marcado `approved: false`, pero el `integrator` priorizó
   arreglar el finding del `critic` (constantes de status duplicadas) en su
   lugar y nunca tocó el TOCTOU. Gobernanza aprobó igual. **Es el mismo
   patrón de la lección de Ronda 3**: un finding reproducido por un rol
   adversarial no llega al fix si el integrator tiene más de un finding
   para elegir y no hay gate bloqueante que fuerce arreglar todos.

Incidente operativo: el workflow murió por presupuesto de intentos agotado
(`maxAgentAttempts=48`) justo después de la aprobación de gobernanza — se
subió el límite a 192 y se resumió con `flow resume`, cerrando en
`succeeded`. El wall-clock bruto (26732s) incluye ese gap de espera manual
más un incidente de rate-limit de cuenta de 5hs no relacionado con esta
corrida específica; el activo estimado (8986s) lo excluye. Costo real
$62.93 ($9.23 Sonnet + $53.70 Opus) — el más caro de las 3 configuraciones
ticketed, con 48 steps totales.

## Patrón cruzado (6 corridas evaluadas)

- **Timestamps naive por round-trip de aiosqlite**: aparece en ambos
  one-shot (Taskflow y Hookrelay), pero en NINGUNA de las 4 ticketed
  (gpt-sol-fast ×2, sonnet5-medium ×2). El ticket_qa por-ticket parece estar
  atrapando esto de forma sistemáticamente más consistente que el
  auto-review de una sesión one-shot — patrón que se sostiene con 4/4.
- **Bus de eventos global (Taskflow)**: aparece en **las 3 configuraciones
  ticketed** (gpt-sol-fast, sonnet5-medium) pero en NINGUNA de las 2 formas
  independientes en que el one-shot construyó la app — sugiere que es un
  patrón que el propio pack/prompt de `coder.md` induce (probablemente un
  ejemplo o costumbre compartida entre corridas ticketed), no azar del
  modelo. Ninguna corrida lo detecta con sus propios tests porque ningún
  test instancia dos apps en el mismo proceso.
- **TOCTOU universal de Hookrelay**: presente en hookrelay-r4-oneshot y
  **también en hookrelay-r4-sonnet5-medium** (a pesar de que el adversary lo
  encontró y marcó explícitamente `approved: false` — el integrator priorizó
  otro finding y nunca lo tocó). Ausente únicamente en hookrelay-r4-gpt-sol
  — la única de 3 corridas Hookrelay evaluadas donde se confirma un fix
  genuino. El patrón se sostiene: encontrar el bug (adversary) y arreglarlo
  (integrator) siguen siendo dos eventos independientes, tal como la
  lección de Ronda 3 predijo.
- **Costo real de las 4 corridas ticketed** (ordenado): sonnet5-medium
  Taskflow $30.49 < gpt-sol-fast Taskflow $132.05 < sonnet5-medium
  Hookrelay $62.93 < gpt-sol-fast Hookrelay $146.34. Sonnet 5 con
  effort=medium fue consistentemente más barato que gpt-5.6-sol-fast en
  ambos specs, a pesar de tener el mismo rol Opus para el resto del team en
  ambas configuraciones — la diferencia está enteramente en el consumo de
  tokens del coder/tester/critic.

## taskflow-r4-luna / hookrelay-r4-luna (condición C final: ticketed-luna, no qwen)

Tras 6 intentos fallidos con qwen y MiniMax M3 (ver `benchmark/round4/README.md`,
incidentes 1-6 — 3 firmas de error, causa raíz común: `agent.invoke@1` sin
retry real para `invalid_contract`/`result_missing`), la condición C de esta
ronda pasó a **`opencode-go/qwen3.7-plus`/`minimax-m3`/`gpt-5.6-luna`**
(coder/tester/critic vía **codex**, no opencode) — converge recién en el 7mo
intento, ya con el fix implementado en `internal/invoke/role.go`
(`contractRetryBudget`). **Desviación no corregida**: soporte
(`parser`/`reviewer`/`ticket_planner`/`ticket_qa`/`qa`/`adversary`/
`integrator`/`gov_reviewer`) corre en **Claude Haiku**, no Opus como el
resto de la ronda — rompe comparabilidad estricta contra las 6 corridas de
arriba, se reportan como referencia aparte.

Evaluación determinística por codex (`gpt-5.6-sol` vía codex, effort medium)
+ verificación manual mía de cada finding antes de aceptarlo (2 falsos
positivos de checklist descartados en Taskflow tras releer el código; 2 de
5 bugs de Hookrelay reverificados leyendo el código directamente).

| | Checklist | Gates | Probe | Bugs confirmados | Tokens (in/out) | Wall-clock | Costo |
|---|---|---|---|---|---|---|---|
| **taskflow-r4-luna** | 27/27 (100%, corregido de 25/27 — 2 falsos positivos del evaluador) | ✅ 25 tests | 13/14 (92.9%) | 5 | 20.5M / 196k | 2433s (~40min) | **$3.48** |
| **hookrelay-r4-luna** | 25/30 (83.3%, sin re-verificar cada ítem) | ✅ 27 tests | 15/15 (100%) | 5 | 32.1M / 351k | 4809s (~80min) | **$5.46** |

**Nota de proceso — el evaluador (codex) se equivocó dos veces en el checklist
de Taskflow**: marcó FAIL la columna `id` por aparecer `CHAR(32)` en
`.schema jobs` (es el comportamiento normal de `Uuid(as_uuid=True)` de
SQLAlchemy sobre SQLite, no un defecto) y marcó FAIL `tests/test_worker.py`
por una supuesta falla que no reproduce (corre 8/8 verde aislado). Ambos
corregidos a PASS tras verificación directa — **no aceptar checklist/bug-hunt
de un evaluador sin re-chequear al menos los ítems que cambian el score**.

**El bug hunt de Hookrelay quedó bloqueado en el primer intento**: el filtro
de seguridad de OpenAI marcó como "riesgo de ciberseguridad" el test que
codex estaba escribiendo para reproducir una condición de carrera en el
dispatcher (lenguaje del prompt original: "race condition", "duplicate
attempt"). Reintentado con el prompt reformulado como "revisión de
correctitud/idempotencia" — convergió limpio en el segundo intento.

**El TOCTOU del dispatcher de Hookrelay vuelve a aparecer** — 3ra corrida de
4 evaluadas donde aparece (`hookrelay-r4-oneshot`, `hookrelay-r4-sonnet5-medium`,
ahora `hookrelay-r4-luna`; ausente únicamente en `hookrelay-r4-gpt-sol`). Esta
vez el mismo patrón `claim_due` (SELECT sin lock + UPDATE en loop, commit al
final) también aparece en un segundo lugar del mismo codebase: creación de
suscripciones (`check-then-insert` sin constraint único en DB). Verificado
por lectura directa de `app/repositories/deliveries.py:89` y
`app/services/subscriptions.py:20` — no es solo el hallazgo del evaluador,
el patrón es real y estructural.

Taskflow además confirma su propio TOCTOU: `DELETE /jobs/{id}` no protege
un job `running` cuando el cambio de estado ocurre fuera de la sesión
SQLAlchemy activa (`session.get()` devuelve el objeto cacheado del identity
map) — encontrado independientemente por el probe congelado Y por el bug
hunt (mismo defecto, dos métodos).

**Costo real** (tarifa gpt-5.6-luna confirmada por el usuario: $0.20/M input,
$1.20/M output vía codex; Haiku $1.00/M input, $5.00/M output, $0.10/M
cache-read):

| | Haiku (soporte) | gpt-5.6-luna (coder/tester/critic) | **Total** |
|---|---|---|---|
| taskflow-r4-luna | $1.88 | $1.60 | **$3.48** |
| hookrelay-r4-luna | $3.61 | $1.85 | **$5.46** |

**Por lejos las corridas ticketed más baratas de toda la ronda** (vs $132/$146
gpt-sol-fast, $30/$63 sonnet5-medium) — la nueva `luna` pasa a ser el mejor
costo absoluto de las 8 condiciones, lo que cambia el baseline `best_cost`
que usa la normalización de `eff` para TODAS las filas, no solo estas 2.
**No recalculo `eff`/`Q` de las 6 filas ya publicadas ahora** — se recalculan
las 8 juntas en la pasada final de síntesis con los jueces LLM, para no
rehacerlo dos veces.

## Jueces LLM y composite final (0.6Q + 0.4L)

**Protocolo aplicado** (acotado, por decisión del usuario — no el protocolo
completo de `evaluation.md` §3.1 de 3 sesiones absolutas): juez Opus (único
disponible en esta sesión — `gemini-3-pro` no se pudo autenticar), 1 sesión
absoluta por implementación (7 dimensiones) + pairwise completo con
position-swap (2 specs × 6 pares × 2 posiciones = 24 comparaciones), blinding
real (export de cada implementación sin `.git`/`.orquestalite`/`team.json`,
verificado sin strings identificatorias de modelo antes de correr). Corrido
vía `Workflow` — 33 invocaciones (8 absolutas + 24 pairwise + 1 síntesis),
32/33 en el primer intento (síntesis falló por el límite de gasto mensual de
la organización, resuelto y reintentado reusando los 32 resultados en
caché).

**Nota de integridad**: el resultado crudo de síntesis incluyó un link
`claude.ai/code/artifact/...` que ningún agente de este workflow tiene
capacidad de generar (sin acceso a la herramienta Artifact) — es una
alucinación del agente de síntesis. Descartado, no visitado, no presentado
como real. Los números de L de abajo vienen de los 32 veredictos crudos
(absoluto + pairwise), no de esa síntesis textual.

**L (índice cualitativo, 0-1) y win-rate pairwise:**

| | Taskflow L | Taskflow win-rate | Hookrelay L | Hookrelay win-rate |
|---|---|---|---|---|
| gpt-sol | **0.930** | **18/18 (100%)** | **0.970** | **17/18 (94%)** |
| sonnet5-medium | 0.890 | 5/14 (36%) | 0.870 | 5/13 (38%) |
| oneshot | 0.730 | 7/14 (50%) | 0.800 | 11/18 (61%) |
| luna | 0.720 | 0/14 (0%) | 0.610 | 0/17 (0%) |

**Desacuerdo absoluto-vs-pairwise detectado** (evaluation.md §3.5: reportar
si el ganador general difiere entre ambos protocolos): sonnet5-medium
puntuó más alto que oneshot en absoluto (Spec fidelity, Code quality) en
ambos specs, pero en comparación directa cabeza a cabeza oneshot ganó esas
mismas dimensiones consistentemente en las dos posiciones. **El ganador
general no cambia** (gpt-sol gana ambos protocolos, ambos specs, por margen
amplio) — el desacuerdo queda confinado al orden entre 2do y 3er lugar, no
afecta la conclusión principal.

**Recalculo de `Q` con `best_cost`/`best_time` correcto** (luna es ahora el
piso de costo en las dos specs, como se anticipó arriba — esto obliga a
recalcular las 8 filas, no solo agregar 2):

| Taskflow | Q (nuevo) | Q (viejo, pre-luna) | L | **Composite** |
|---|---|---|---|---|
| oneshot | 0.8937 | ~~0.9467~~ | 0.730 | 0.8282 |
| gpt-sol | 0.7944 | ~~0.7974~~ | 0.930 | **0.8486** |
| sonnet5-medium | 0.8009 | ~~0.8137~~ | 0.890 | 0.8365 |
| luna | 0.8118 | (nuevo) | 0.720 | 0.7751 |

| Hookrelay | Q (nuevo) | Q (viejo, pre-luna) | L | **Composite** |
|---|---|---|---|---|
| oneshot | 0.7843 | ~~0.7566~~ | 0.800 | 0.7906 |
| gpt-sol | 0.8722 | ~~0.8275~~ | 0.970 | **0.9113** |
| sonnet5-medium | 0.8355 | ~~0.8042~~ | 0.870 | 0.8493 |
| luna | 0.8000 | (nuevo) | 0.610 | 0.7240 |

## Conclusión de Ronda 4

**`gpt-sol-fast` gana en ambos specs**, por margen amplio, en las tres
señales independientes (Q determinístico, L cualitativo, win-rate
pairwise) — no hay desacuerdo entre métodos sobre el ganador general, solo
en el orden de los puestos 2°-3°. Es también, con diferencia, la condición
más cara de la ronda ($132-146 vs $3.48-62.93 del resto) — el resultado no
dice "gpt-sol-fast es la opción recomendada", dice que bajo este protocolo
de jueces y este composite (0.6Q+0.4L, sin penalizar costo salvo vía el 20%
de peso de `eff`) el gasto extra se traduce en calidad medible.

`luna` (la condición que costó 7 intentos hacer converger, ver
`benchmark/round4/README.md`) termina último en L y en win-rate pairwise en
ambos specs — coherente con que fue la única condición con soporte en Haiku
en vez de Opus (desviación documentada, no comparable 1:1 contra el resto).
No se puede separar cuánto del resultado es el modelo del coder
(`gpt-5.6-luna`) vs. el downgrade de soporte a Haiku sin una corrida de
control adicional (`luna` con soporte en Opus) — queda fuera de alcance de
esta ronda.

## Pendiente

- Corrida de control opcional: `luna` con soporte en Opus (no Haiku), para
  aislar el efecto del downgrade de soporte del efecto del modelo del coder.
- Comparar contra las corridas ya publicadas de rondas 1-3 si se decide
  publicar un reporte comparativo cross-ronda.
