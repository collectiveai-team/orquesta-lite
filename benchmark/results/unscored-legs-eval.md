# Puntuando las 3 corridas "sin evaluar": gov-loop (R2), Taskflow r3, Hookrelay r3

Estas 3 corridas convergieron y fueron documentadas cualitativamente en
[`benchmark-round2-hookrelay.md`](../../.claude...) (memoria del proyecto) y
[`round3-r1.md`](./round3-r1.md), pero nunca pasaron por el pipeline de
puntuación completo (checklist por ejecución + gates + probe + bug hunt +
jueces LLM). Este documento corre ese pipeline retroactivamente sobre los
árboles de trabajo intactos (`~/Projects/personal/hookrelay-ticketed-qwen-govloop`,
`~/Projects/personal/taskflow-ticketed-r3`, `~/Projects/personal/hookrelay-ticketed-r3`)
para producir composites reales y comparables.

## Desviaciones de protocolo (declaradas, no ocultas)

1. **Sin juez fuera de familia.** No hay gemini disponible en este entorno.
   Se usó Sonnet 5 y Opus como jueces — mismo caveat que rondas anteriores
   cuando gemini no estaba disponible, agravado acá porque en Taskflow r3 y
   Hookrelay r3 **ambos** modelos (Sonnet=coder, Opus=todo el resto) ya son
   parte del team bajo evaluación.
2. **N de jueces reducido a mitad de evaluación por un límite de gasto
   mensual de la organización** (no rate-limit transitorio — 3 de 9 sesiones
   de juez fallaron con "hit your org's monthly spend limit"). Resultado:
   - Hookrelay r3: **N=3 completo** (protocolo cumplido)
   - Taskflow r3: **N=2** (la sesión Opus #1 falló)
   - govloop: **N=1** (fallaron 2 de 3 sesiones Sonnet) — sin mediana real,
     es el score de una única sesión.
3. **Sin matriz pairwise nueva** contra las demás corridas de cada ronda —
   reconstruir eso exige re-exportar en blind los árboles de rondas
   anteriores, fuera de alcance. Solo se reporta L absoluto.
4. **Bug hunt de los 2 legs de R3 reutiliza hallazgos ya reproducidos**
   documentados en `round3-r1.md`, re-verificados de forma independiente por
   el agente de evaluación (repro propia, no solo lectura del reporte).
   `govloop` sí recibió una sesión de bug hunt estandarizada nueva (nunca
   había tenido una).
5. Gates y probe corrieron sobre copias en scratchpad, preservando los
   árboles de trabajo originales intactos.

## Resultados

### Taskflow r3 (`factory-governed@4`, Sonnet 5 coder + Opus resto + rol adversary)

| Métrica | Valor |
|---|---|
| Checklist | 25/27 (92.6%) |
| Gates | ✅ ruff clean, 47/47 tests |
| Probe independiente | 14/14 (100%) |
| Bugs confirmados | 2 — DELETE de job pendiente → `AttributeError` no manejado en el flow; bus de eventos global de proceso en vez de scoped a `app.state` |
| Correctness subtotal | 53.78/60 |
| Costo | $45.38 (r3 original) / $53.73 combinado con la corrección r3b |
| Wall-clock activo | 4121s (~1.14h, tras remover 3 stalls de infraestructura) |
| eff | 0.5384 |
| **Q** | **0.8247** |
| L (mediana, **N=2**) | Spec 4, Correctness 2, Arch 3, Test 4, Concurrency 3, Code 4, Docs 5 → **0.68** |
| **Composite** | **76.68** |

Referencia Taskflow ronda 1: default 84.22, v4 84.22, v3 72.2, v2 73.5 uncapped/50 capped, one-shot 75.6. Taskflow r3 (76.68) queda entre v3 y one-shot/v4 — el rol `adversary` detectó bugs reales por método pero el defecto de wiring (ver `round3-r1.md`) impidió que se arreglaran, así que el resultado final es comparable a los legs que shippearon 2+ bugs, no una mejora sobre v4.

### Hookrelay r3 (`factory-governed@4`, mismo team, flow **pre-fix** de wiring)

| Métrica | Valor |
|---|---|
| Checklist | 28/28 (100%) |
| Gates | ✅ ruff clean, 68/68 tests |
| Probe independiente v2.2 | 15/15 (100%) |
| Bugs confirmados | 2 — `RuntimeError` de cliente HTTP cerrado escapa el catch de excepciones en `attempt_delivery`; TOCTOU universal en `POST /subscriptions` (5 concurrentes → 5×201) |
| Correctness subtotal | 56/60 |
| Costo | $74.73 |
| Wall-clock activo | 7432s (~2.06h, tras remover 3 stalls de infraestructura) |
| eff | 0.1607 |
| **Q** | **0.7788** |
| L (mediana, **N=3 completo**) | Spec 4, Correctness 3, Arch 4, Test 4, Concurrency 3, Code 4, Docs 5 → **0.75** |
| **Composite** | **76.73** |

Referencia Hookrelay ronda 2: one-shot 94.8, ticketed-sonnet 89.8, ticketed-qwen 50 capped/76.4 uncapped. Hookrelay r3 (76.73) queda muy cerca del uncapped de ticketed-qwen (76.4) y bien por debajo de one-shot/ticketed-sonnet — el defecto de wiring del adversary anula la ventaja esperada del rol nuevo.

### ticketed-qwen-govloop (`factory-governed@3`, qwen3.7-plus + loop de reparación de gobernanza)

| Métrica | Valor |
|---|---|
| Checklist | 34/34 (100%) |
| Gates | ✅ ruff clean, 68/68 tests |
| Probe independiente v2.2 | 15/15 (100%) |
| Bugs confirmados | 1 — TOCTOU universal en `POST /subscriptions` (mismo patrón de ronda 2) |
| Correctness subtotal | 58/60 |
| Costo | $52.44 **floor** (roles Claude; 18 corridas qwen sin costo reportado) |
| Wall-clock activo | 12503s (~3.47h, tras remover un timeout por sueño de la Mac) |
| Ciclos de gobernanza | 2 (gov#1 rechazó por 2 violaciones de CONVENTIONS → integrator corrigió → gov#2 aprobó) |
| eff | 0.1446 (one-sided por ser floor) |
| **Q** | **0.8022** |
| L (**N=1 únicamente**, no mediana) | Spec 4, Correctness 3, Arch 4, Test 4, Concurrency 3, Code 4, Docs 4 → **0.74** |
| **Composite** | **77.74** |

Referencia directa — su comparación más limpia es `ticketed-qwen` sin el loop (mismo coder, mismo spec, ronda 2): 50 capped / 76.4 uncapped. El loop de gobernanza convirtió una corrida que hubiera terminado **capada en 50** en una que converge y puntúa **77.74** — la mejora práctica más grande viene de evitar el cap por no-convergencia, no de una diferencia dramática en calidad de código (76.4 uncapped → 77.74 es una mejora modesta pero real, y las 2 violaciones de CONVENTIONS que gov#1 originalmente rechazó están genuinamente reparadas — verificado por el juez vía el test AST de `test_scaffold.py`).

## Nota metodológica final

Estos 3 números son comparables en escala (misma fórmula, mismos specs/probes)
a los legs ya publicados de sus rondas respectivas, pero **no tienen el mismo
nivel de confianza estadística** — especialmente govloop (N=1 en L) y Taskflow
r3 (N=2). Tratarlos como el punto medio de una distribución más ruidosa que
los legs con N=3 completo, no como una cifra exacta.
