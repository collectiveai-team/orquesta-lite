# Governed refactor pass — design

**Fecha:** 2026-07-28
**Contexto:** brainstorm de dirección de producto para orq-lite (diferenciales del
loop autónomo). De los 5 diferenciales propuestos por el usuario (flujos
dinámicos, AFK, garantías de calidad, testing+mejora vía loops, refactors),
4 ya están sólidamente implementados en el governed pack (`examples/governed-pack/`).
El quinto — una pasada de refactor tras la implementación — no existe en
ningún pack/flow del repo (confirmado por grep). Este spec cubre solo ese
gap. La validación "como lo haría un humano" (browser-driven UX/Figma) es un
segundo gap identificado en el mismo brainstorm y se especifica por separado.

## Motivación

"Siempre que un agente (o varios) implementan algo hay espacio para un buen
refactor." Hoy el governed pack (`factory-governed@1`) termina en
`governance_gate` aprobado → `publish_pr` opcional. No hay ningún paso que
mire el código ya implementado y gobernado buscando específicamente
oportunidades de limpieza estructural (duplicación, nombres, complejidad
incidental) — solo bugs/spec-violations (`adversary`, `critic`,
`gov_reviewer`).

## Decisiones de diseño (del brainstorm)

1. **Cuándo:** al final, sobre todo el diff ya integrado — no por ticket.
   Evita pisar trabajo de tickets futuros y ve el codebase completo.
2. **Autoridad:** el rol nuevo (`refactorer`) encuentra oportunidades
   (findings), no edita directo. Un integrator las aplica — mismo patrón que
   `adversary`/`critic` → `integrator`.
3. **Aislamiento (por qué C y no A):** el refactor pass corre en un subflow
   separado, **después** de que `governance_gate` ya aprobó — no comparte el
   presupuesto de reintentos de `integration_repair`/`governance_repair`. Un
   finding de refactor nunca compite por iteraciones con un finding de bug
   real.
4. **Checkpoint commit:** justo antes de que el refactor pass toque nada, se
   commitea explícitamente el estado ya gobernado y aprobado. Esto convierte
   el aislamiento de (3) en un límite durable e inspeccionable en git, no
   solo en la posición del subflow.
5. **Peso de la falla:** si el refactor pass no converge en su propio
   presupuesto de reintentos, el run entero falla cerrado (fail-closed) —
   mismo peso que cualquier otro finding de gobernanza. El checkpoint commit
   de (4) es lo que hace esta falla segura de recuperar: el código gobernado
   ya está en git antes de que exista cualquier riesgo de regresión del
   refactor.
6. **Siempre activo**, sin flag `refactor=true`. Coherente con "siempre hay
   espacio para un refactor"; el costo extra (un rol Opus-tier más + su
   propio loop de repair) se acepta como parte estándar de
   `factory-governed`.

## Arquitectura

```
plan_tickets → develop_tickets → integrated_review (sin cambios: qa → adversary → critic →
                                  integration_repair → gates → governance → governance_repair →
                                  governance_gate)
             → checkpoint_commit   [NUEVO]
             → refactor_review     [NUEVO subflow]
             → publish_pr          (sin cambios, if create_pr == true)
```

`integrated-review@1` **no se modifica**. Todo lo nuevo vive en pasos y
subflows adicionales del flow top-level `factory-governed`.

### `checkpoint_commit` (nuevo step, top-level flow)

```json
{
  "id": "checkpoint_commit",
  "uses": "activity:command.run@1",
  "with": {
    "argv": ["sh", "-c", "git add -A && (git diff --cached --quiet || git commit -m 'checkpoint: governance approved, pre-refactor')"]
  }
}
```

`EffectAtMostOnce` (heredado de `command.run@1`) es aceptable acá: si el
proceso muere después de que el commit ya se hizo pero antes de que el step
se marque completo, un re-run del mismo comando es un no-op seguro gracias
al `git diff --cached --quiet` guard — no hace falta una activity nueva
`git.commit@1` idempotente para el primer corte. Se documenta como mejora
futura si el uso real muestra que hace falta (ver Alternativas).

### `refactor_review` (nuevo subflow, `subflows/refactor-review@1.json`)

Mismo patrón exacto que `integrated-review@1` para su fase de gobernanza:

```json
{
  "steps": [
    {
      "id": "refactorer",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "refactorer",
        "outputSchema": "schema:review-result@1",
        "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}},
        "fallbackOutput": {
          "approved": true,
          "summary": "refactorer unavailable; no refactor findings to apply",
          "findings": []
        }
      }
    },
    {
      "id": "refactor_repair",
      "uses": "subflow:refactor-cycle@1",
      "while": {
        "condition": "item.approved != true",
        "maxIterations": 2,
        "initial": {
          "approved": {"$ref": "steps.refactorer.output.approved"},
          "verdict": {"$ref": "steps.refactorer.output"}
        }
      },
      "with": {
        "features_path": {"$ref": "inputs.features_path"},
        "verdict": {"$ref": "item.verdict"}
      }
    },
    {
      "id": "refactor_gate",
      "uses": "activity:gate.run@1",
      "with": {
        "argv": ["uv", "run", "python", "-c",
          "import json,sys; data=json.load(open(sys.argv[1])); raise SystemExit(0 if data.get('approved') is True else 1)",
          ".orquestalite/results/refactorer.json"]
      }
    }
  ]
}
```

Nota sobre `fallbackOutput.approved: true`: a diferencia de `qa`/`adversary`/
`critic` (donde la ausencia del rol debe bloquear fail-closed, porque no
sabemos si hay bugs), la ausencia del `refactorer` es segura de tratar como
"sin oportunidades encontradas" — el peor caso es que no se refactorizó
nada, no que un bug pasó desapercibido. Esto es una asimetría deliberada
respecto a los roles de calidad existentes.

### `refactor-cycle@1` (nuevo subflow, clon de `governance-cycle@1`)

Idéntico en forma a `subflows/governance-cycle@1.json`, con `GOV_REVIEW` /
`gov_reviewer` reemplazados por `REFACTOR_REVIEW` / `refactorer`:

```json
{
  "steps": [
    {
      "id": "repair",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "integrator",
        "outputSchema": "schema:iteration-result@1",
        "context": {
          "FEATURES_PATH": {"$ref": "inputs.features_path"},
          "FEEDBACK": {"$ref": "inputs.verdict"},
          "REFACTOR_REVIEW": {"$ref": "inputs.verdict"}
        }
      }
    },
    {"id": "lint", "uses": "activity:gate.run@1", "with": {"argv": ["uv", "run", "ruff", "check", "."]}},
    {"id": "tests", "uses": "activity:gate.run@1", "with": {"argv": ["uv", "run", "pytest", "-q"]}},
    {
      "id": "refactorer",
      "uses": "activity:agent.invoke@1",
      "with": {
        "role": "refactorer",
        "outputSchema": "schema:review-result@1",
        "vars": {"FEATURES_PATH": {"$ref": "inputs.features_path"}}
      }
    }
  ],
  "outputs": {
    "approved": {"$ref": "steps.refactorer.output.approved"},
    "verdict": {"$ref": "steps.refactorer.output"}
  }
}
```

Reusa el rol `integrator` existente (no hace falta un rol de aplicación
nuevo) — su prompt debe extenderse para reconocer `REFACTOR_REVIEW` además
de `GOV_REVIEW`/`CRITIC_REVIEW`/etc., aplicando el mismo criterio: cambios
estructurales que preservan comportamiento, nunca funcionalidad nueva.

### Rol nuevo: `refactorer`

Agregado a `team.json` (Opus-tier, mismo nivel que `adversary`/`critic`/
`gov_reviewer` — es un rol de juicio, no de implementación):

```json
"refactorer": {"agents": ["claude_opus"], "prompt": ".../prompts/refactorer.md", "result_path": ".../refactorer.json", "timeout_seconds": 1500}
```

Reusa `schema:review-result@1` sin cambios (`{approved, summary, findings}`)
— no hace falta un schema nuevo. El prompt debe dejar explícito:
- Ámbito: todo el diff integrado y ya gobernado, no un ticket aislado.
- Prohibido: cambiar comportamiento observable, agregar funcionalidad,
  tocar la superficie pública de la API salvo que sea estrictamente
  cosmético.
- Cada finding debe poder verificarse como "seguro" corriendo lint+tests sin
  cambios de resultado — si un finding requiere reescribir tests para
  validarse, no es un finding de refactor puro.

## Presupuestos / números

- `refactor_repair` (`maxIterations`): **2**, igual que `governance_repair`.
  Presupuesto propio — no toca `integration_repair` (sigue en 3) ni
  `governance_repair` (sigue en 2), que es justamente el punto de elegir C
  sobre A: el refactor nunca infla el loop de bugs reales.
- Costo: un rol Opus más por run (`refactorer`, invocado 1 + hasta 2 veces
  en `refactor-cycle@1`) más el `integrator` reinvocado hasta 2 veces. Del
  mismo orden de magnitud que agregar un ciclo de `governance_repair` extra.
  No se gatea con flag (decisión explícita): siempre activo.

## Alternativas consideradas y descartadas

- **(A) Cuarto finder dentro de `integrated-review`**, findings al mismo
  `integration_repair`. Más barato en steps nuevos (reusa el loop
  existente), pero mezcla presupuesto de reintentos con bugs reales y pierde
  el límite durable que da el checkpoint commit. Descartado a favor de C.
- **(B) Meter el refactor dentro del prompt del `critic`.** Más barato aún
  (cero agentes nuevos), pero mezcla dos tipos de juicio distintos en un
  solo rol y diluye la señal de cada uno. Descartado.
- **`git.commit@1` como activity nueva (`EffectIdempotent`)** en vez del
  shell-guard sobre `command.run@1`. Más "correcto" respecto al modelo de
  efectos (una activity dedicada podría exponer semántica idempotente
  explícita en vez de depender de un guard de shell), pero es máquina
  adicional sin evidencia todavía de que el guard de shell no alcance.
  Queda como mejora futura, no bloqueante de este spec.
- **Segundo commit al cerrar `refactor_gate` aprobado** (separado del
  checkpoint), para que `publish_pr` muestre dos commits lógicos y un
  `git revert` del refactor sea trivial sin tocar el feature gobernado. Buena
  idea de bajo riesgo, pero no bloqueante — se deja como follow-up de
  implementación, no como parte obligatoria de este spec.

## Verificación

- `orq-lite flow validate development/factory-governed@N` con los nuevos
  archivos instalados.
- Un run real sobre un spec chico (ej. el mismo usado en benchmark rounds)
  con un diff que tenga oportunidades de refactor obvias plantadas a
  propósito, confirmando: (a) `refactorer` las encuentra, (b) `integrator`
  las aplica sin romper gates, (c) `checkpoint_commit` deja un commit previo
  al refactor, (d) forzar que el refactor rompa un gate y no converja en 2
  iteraciones causa fail-closed del run completo, con el checkpoint commit
  intacto en git log.
- Actualizar dígests de `pack.json` siguiendo el mismo protocolo ya usado en
  esta sesión para los packs de benchmark (SHA-256 por archivo,
  verificación byte a byte antes de tocar `pack.json`).

## Fuera de alcance de este spec

- La validación "como lo haría un humano" (browser-driven UX, comparación
  contra Figma) — gap identificado en el mismo brainstorm, spec separado.
- El fix de `invalid_contract`/`EffectAtMostOnce` documentado en
  `tasks/todo.md` (Fase 5, Task 26) — no relacionado, no se toca acá.
