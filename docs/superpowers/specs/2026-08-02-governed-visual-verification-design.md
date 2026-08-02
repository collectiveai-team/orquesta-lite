# Validación visual/UX gobernada — design

**Fecha:** 2026-08-02
**Contexto:** segundo gap del mismo brainstorm de dirección de producto que
produjo el refactor pass (`docs/superpowers/specs/2026-07-28-governed-refactor-pass-design.md`).
El usuario pidió, en sus propias palabras, roles/flows que — dado todo lo
implementado y el objetivo — testeen y validen "como lo haría un humano", no
vía tests automatizados: probar un frontend, validar UX contra un diseño de
referencia.

## Lo que ya existe (y por qué no alcanza)

`internal/commands/assets/prompts/factory-visual-verify.md` ya hace casi
exactamente esto: maneja un browser real (`agent-browser`, con fallback a
playwright MCP → `npx playwright` → curl+HTML), navega, hace click, saca
screenshots, y exige evidencia observada por cada criterio (nunca aprueba un
check sin haberlo visto). `orq-lite doctor` ya reporta
`binary:agent-browser — browser-driven visual verification available`, así
que la integración a nivel de herramienta ya está.

El problema: ese prompt está cableado **solo al comando `factory` legacy
(v1)** (`internal/commands/factorycmd.go:366-407`) — corre por feature
marcada `Visual`, con su propio loop de fix-tasks encadenado a la cola de
tasks de v1. No existe nada equivalente en el governed pack v2
(`examples/governed-pack/`), que es el camino recomendado hoy. Comparación
contra Figma no existe en ningún lado del repo (confirmado por grep).

## Decisiones de diseño (del brainstorm)

1. **No son dos subproyectos — es uno solo.** Lo que arrancó como "portar
   visual-verify" + "agregar Figma" se resolvió en un único rol: el check de
   Figma es un ítem más dentro de la misma lista de `checks[]` que el rol ya
   produce, no un mecanismo aparte.
2. **Insumo de Figma: imagen exportada a mano**, no MCP ni API de Figma. El
   usuario exporta el frame como imagen y la deja en el repo. Sin
   autenticación, sin nuevo tipo de credencial.
3. **Ubicación del insumo: convención de carpeta (`design/`) + referencia por
   nombre de archivo dentro del criterio de aceptación en `features.md`.**
   No es un input nuevo del flow (no `design_path` a nivel `factory-governed`)
   — el rol la descubre leyendo el ticket, igual que ya lee `FEATURES_PATH`.
4. **Encaje en el pipeline: adentro de `integrated_review`, junto a
   `qa`/`adversary`/`critic`** — no después de `governance` como el refactor
   pass. Un defecto visual/UX real es un defecto del feature, no una mejora
   opcional: debe poder bloquear la aprobación final igual que un finding de
   QA o adversary.
5. **Auto-detección de ausencia de UI**: el propio rol confirma primero si
   existe superficie de UI real (frontend, dev server, `index.html`) antes de
   intentar nada con el browser. Si no hay nada que abrir, reporta
   `approved:true` con el summary explicando que no aplica — mismo patrón que
   usa `refactorer` para "nada que refactorizar". Sin flag nuevo a nivel
   flow (`has_ui=...`); Taskflow/Hookrelay (sin frontend) nunca disparan el
   camino de browser.

## Arquitectura

```
integrated_review (modificado):
   lint → tests → qa → adversary → critic → visual_verifier
   → integration_repair   [reconcilia QA + adversary + critic + visual, SIN loop nuevo]
   → gates → governance   [también evalúa el finding visual, mismo peso]
   → governance_repair
   → governance_gate
```

`governance-cycle@1` (usado por `governance_repair`) también recibe el
review visual — mismo patrón que ya recibe `QA_REVIEW`/`CRITIC_REVIEW`/
`ADVERSARY_REVIEW`.

### Rol nuevo: `visual_verifier`

Prompt nuevo (`prompts/visual-verifier.md`), adaptado del `factory-visual-
verify.md` de v1, con dos agregados:

1. **Paso 0 — auto-detección**: antes de tocar un browser, confirmar que
   existe una superficie de UI real (buscar `package.json` con script de
   dev, `index.html`, un frontend en el repo). Si no hay nada, escribir
   `{"approved": true, "summary": "no UI surface found; nothing to verify visually", "findings": []}`
   y terminar ahí — sin abrir `agent-browser`.
2. **Check de Figma opcional**: si el ticket actual referencia una imagen
   bajo `design/<archivo>` en su criterio de aceptación, el rol compara el
   screenshot real contra esa imagen como un check más de la misma lista de
   `checks[]` — mismo estándar de evidencia que cualquier otro check (nunca
   aprobar sin haber comparado).

Todo lo demás del v1 se preserva: `agent-browser` primero, fallback a
playwright MCP → `npx playwright` → curl+HTML; cada check requiere
evidencia observada (screenshot, texto renderizado, estado de consola);
cualquier error de consola o excepción no capturada es un check fallido;
limpieza del dev server y la sesión de browser al final.

**Schema de salida**: no el `{status, checks: [...]}` de v1 — para encajar
en `integration_repair`/`governance` sin mecanismo nuevo, el rol escribe
`schema:review-result@1` (`{approved, summary, findings}`), igual que
`qa`/`adversary`/`critic`/`refactorer`. Cada check fallido de la lista
interna se resume a un string de `findings` con la evidencia concreta adentro
(ej.: `"agent-browser open /jobs; snapshot -i — esperaba banner 'Over-
allocated', no está presente (screenshot tmp/jobs.png)"`) — no se pierde
detalle, cambia de forma.

**`fallbackOutput`**: `{"approved": false, ...}`, igual que `qa`/`adversary`/
`critic` — **no** como `refactorer` (`approved: true`). La asimetría de
`refactorer` se justificaba porque la ausencia de un refactor es segura por
definición; acá no: un defecto visual real que no se pudo chequear por una
falla de infraestructura del rol (agente no disponible, no escribió
checkpoint válido) es exactamente el mismo riesgo que una falla de `qa` o
`adversary` — debe bloquear fail-closed, no aprobarse por default. La
distinción importante es entre **"el rol corrió y decidió que no aplica"**
(caso 5 arriba, legítimo `approved:true` del propio agente) y **"el rol no
pudo correr"** (fallback, debe ser `approved:false`).

### Wiring genuino, no solo placeholder-safety

A diferencia del `REFACTOR_REVIEW` del refactor pass (donde alcanzaba con
sembrar un literal `"n/a"` en los call-sites existentes de `integrator`,
porque el refactor corre en una fase separada), acá `visual_verifier` es un
finder de **primera clase**, al mismo nivel que `qa`/`adversary`/`critic` —
sus findings deben ser evaluados genuinamente, no solo evitar que un
placeholder quede sin resolver:

- `integration_repair`: agrega `VISUAL_REVIEW` al `context`, con el valor
  real (`{"$ref": "steps.visual_verifier.output"}`), igual que ya hace con
  `QA_REVIEW`/`CRITIC_REVIEW`/`ADVERSARY_REVIEW`.
- `governance` y `governance-cycle@1`: mismo agregado — `VISUAL_REVIEW` real
  en su `context`.
- `integrator.md`: agrega una línea `Visual/UX review: {{VISUAL_REVIEW}}` al
  bloque de reviews existente, y extiende la instrucción de "reproducí y
  arreglá todos los findings de QA, critic y adversary" para incluir
  explícitamente los del review visual.
- `gov-reviewer.md`: agrega `Visual/UX result: {{VISUAL_REVIEW}}` a su
  bloque de reviews, y extiende "para todo finding `approved:false` de
  arriba, confirmá independientemente si ya está arreglado" para cubrir las
  cuatro reviews, no tres.

## Presupuestos / números

Sin loop de repair nuevo — `visual_verifier` alimenta el `integration_repair`
existente (`maxIterations: 3`, sin cambios) y el `governance_repair`
existente (`maxIterations: 2`, sin cambios). A diferencia del refactor pass,
acá no hace falta aislar presupuesto: un finding visual compite por atención
del integrator exactamente igual que un finding de QA o adversary, porque
tiene el mismo peso — son la misma clase de problema (el feature no cumple
lo prometido), no una mejora opcional aparte.

Costo: un rol Opus-tier más por corrida (`visual_verifier`, invocado una vez
por `integrated_review`, más las veces que corra dentro de
`integration_repair`/`governance_repair` si hay findings que reconciliar) —
mismo orden de magnitud que agregar `adversary` ya agregó en su momento. Para
specs sin UI (Taskflow, Hookrelay), el costo real es mínimo: el rol se
auto-detecta y termina en el primer paso sin abrir un browser.

## Alternativas consideradas y descartadas

- **Figma vía MCP/API** (autenticación, lectura directa del archivo). Más
  cómodo para el usuario a largo plazo, pero agrega superficie de
  credenciales y complejidad de setup para un caso de uso que la exportación
  manual ya resuelve. Descartado para este primer corte; se puede reconsiderar
  si el uso real muestra que la fricción de exportar a mano importa.
- **Rol separado solo para Figma**, aparte del visual-verify portado. Más
  aislado, pero duplica la infraestructura de browser+screenshot para un
  chequeo que es, en esencia, "una comparación más" del mismo tipo de
  evidencia visual que el rol ya produce. Descartado a favor de un check
  adicional en el mismo rol.
- **Correr después de `governance`, como el refactor pass** (aislado, propio
  presupuesto). Descartado explícitamente: un defecto visual es un defecto
  real del feature, no una mejora posterior — debe poder bloquear la
  aprobación final, no llegar después de que ya se aprobó.
- **Flag `has_ui=true` a nivel flow**, en vez de auto-detección. Más
  explícito, pero reintroduce la granularidad manual por-feature que v1 tenía
  (`f.Visual`) como config a mano en vez de detección automática — y ya
  tenemos el patrón de auto-evaluación legítima (`refactorer`) probado en el
  otro spec de esta misma sesión de brainstorm.

## Verificación

- `orq-lite flow validate development/factory-governed@N` con los archivos
  modificados/nuevos instalados.
- Confirmar que en un proyecto sin frontend (ej. Taskflow, Hookrelay) el rol
  termina en el paso de auto-detección sin invocar `agent-browser` ni
  agregar tiempo/costo significativo.
- Un run real sobre un proyecto con frontend, con un defecto de UI plantado
  a propósito (ej. un botón que no dispara la acción prometida), confirmando
  que `visual_verifier` lo encuentra, `integration_repair` lo arregla sin
  romper gates, y `gov-reviewer` lo re-verifica antes de aprobar.
- Un run con una imagen de referencia en `design/` deliberadamente distinta
  del render real, confirmando que el check de Figma lo detecta y se
  resuelve por el mismo camino de repair que cualquier otro finding.
- Actualizar dígests de `pack.json` con el mismo protocolo ya usado (SHA-256
  por archivo, verificación byte a byte).

## Fuera de alcance de este spec

- El refactor pass (`docs/superpowers/specs/2026-07-28-governed-refactor-pass-design.md`)
  — spec y plan independientes, ya escritos, todavía no implementados.
- Integración de Figma vía MCP/API (ver Alternativas) — posible incremento
  futuro, no bloqueante.
- El fix de `invalid_contract`/`EffectAtMostOnce` de `tasks/todo.md` Task 26
  — ya implementado en una rama de trabajo separada de esta sesión, no
  relacionado.
