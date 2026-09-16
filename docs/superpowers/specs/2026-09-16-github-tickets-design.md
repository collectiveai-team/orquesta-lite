# Tickets en GitHub como fuente opcional — design

**Fecha:** 2026-09-16
**Contexto:** hoy el core de la implementación corre sobre tickets que existen
únicamente dentro del run: `ticket_planner` emite un `workflow-state@2` a
`.orquestalite/results/ticket_planner.json` y el loop `develop-ticket@1` lo
consume. Nadie fuera del run los ve, ni antes ni después. El pedido es poder
(a) pasar como spec un issue de GitHub en lugar de un `features.md` local, y
(b) que los tickets vivan en GitHub, creados por el planner o preexistentes,
con la elección local/github declarada **en el pack** y no en el código Go.

## Lo que ya existe (y por qué no alcanza)

`issue-fix@1` ya acepta "un issue" — pero como **archivo local**: su step
`read_issue` hace `cat {{issue_path}}` y el `intake` triagea ese texto. No
habla con GitHub en ningún momento. Un `grep` por `gh` sobre todo el pack
devuelve exactamente un uso: `factory-governed@2` corre
`["gh","pr","create","--fill"]` detrás de `create_pr`, default `false`.

El planner ya modela un grafo: `workflow-state@2` exige `dependencies` por
ticket, y desde el PR #65 el prompt define esas aristas como blocking edges y
la selección de `next_ticket` como frontier. Lo que falta no es el modelo de
tickets — es que el modelo tenga una representación fuera del run.

## Restricciones del runtime (verificadas, no asumidas)

Estas cuatro condicionan el diseño más que cualquier preferencia:

1. **El lenguaje de `if` no tiene predicados de string.** `internal/flow/expr.go`
   parsea `&& || == != >= <= > <` y nada más. Un flow **no puede** inspeccionar
   `spec_ref` para decidir si es un path o una referencia a issue. Esa rama no
   existe como opción de diseño; la decisión tiene que venir de un input
   explícito o del agente.
2. **`command.run` con `argv` corre; con `shell` no.** `internal/workflow/scheduler.go:701`
   deniega solo el campo `shell` cuando la policy no declara `allowShell`, y
   `policies/development@3.json` no lo declara. `gh issue list --json ...` corre
   hoy sin tocar la policy. Un pipeline `sh -c` no.
3. **`foreach` nunca se ejecutó en este pack.** El runtime lo implementa
   (`ForeachSpec`, scheduler, store con `foreach_key`), pero `grep foreach` sobre
   `packs/development/` no devuelve nada. Apoyar el diseño en `foreach` sería
   estrenarlo en producción — el mismo patrón por el que `factory-governed@2`
   non-fast salió roto en v0.7.0. Este diseño no lo usa.
4. **`command.run@1` es `EffectAtMostOnce`.** Para escrituras a GitHub eso corta
   bien los reintentos, pero un crash a mitad deja estado indeterminado que el
   resume no repara. Las escrituras tienen que ser idempotentes por construcción
   o verificables después.

Y una restricción de política, no de runtime: **el pack tiene hoy una postura
explícita anti-escritura saliente.** `pr-review@1` arranca con un `gate.assert`
que exige `publish == false`, con el mensaje _"development@6 produces local
review evidence; publication requires a separate authorized action"_. Crear y
editar issues es escritura saliente y no puede entrar por la puerta de atrás.

## Decisiones de diseño

1. **Un subflow `tickets-source@1` concentra el cambio.** Los cinco flows que
   hoy tienen un step `plan_tickets` (`plan-tickets@1`, `task-list@1`,
   `factory-fast@1`, `factory-governed@2`, `issue-fix@1`) y el `replan` dentro
   de `develop-ticket@1` pasan a usarlo. La semántica local/github se define una
   vez y no se replica en seis lugares.
2. **La elección vive como input del flow con default en el JSON del pack.**
   `ticket_store` con `default: "local"` declarado en cada flow. Cambiar el
   default de un proyecto es editar el flow del pack — "plasmado en el pack",
   sin extender `pack.json` ni su loader en Go. Se overridea por run desde la
   CLI sin tocar archivos.
3. **Las escrituras a GitHub van detrás de un flag explícito, default `false`**,
   por simetría con `create_pr`. `publish_tickets=false` permite **leer** issues
   siempre; crear o editar, nunca. La postura del pack queda intacta.
4. **El agente resuelve, el flow no ramifica.** Consecuencia directa de la
   restricción 1: `ticket_planner` recibe `SPEC_REF` y `TICKET_STORE` y decide
   si eso es un archivo o un issue, si los tickets ya existen o hay que crearlos.
   Tiene `gh` disponible y es el único componente que puede mirar el string.
5. **La rama de vuelta se resuelve con datos, no con `if`:** el planner
   **devuelve** dónde quedó la spec, y todo lo de abajo consume ese valor.
6. **La spec es el ledger de tickets.** La referencia a los issues que
   implementan una spec vive **en la spec** — en `features.md` o en el cuerpo
   del issue-spec. No en labels, no en un archivo aparte.
7. **En modo github el frontier se relee en cada `advance`.** Si un humano
   cierra, edita o agrega un issue entre pases, el run lo respeta. Ese es el
   valor de tener los tickets afuera; si no se relee, GitHub es decoración.

## Diseño

### Superficie de inputs

Tres inputs nuevos, declarados en cada flow que hoy declara `features_path`:

| Input | Schema | Default | Qué es |
|---|---|---|---|
| `spec_ref` | `schema:text@1` | — | Path local (`features.md`) o referencia a issue (`#123`, `owner/repo#123`, o URL). Un solo campo: el agente discrimina. |
| `ticket_store` | `schema:ticket-store@1` (nuevo, enum `local`\|`github`) | `"local"` | Dónde viven los tickets. |
| `publish_tickets` | `schema:flag@1` | `false` | Autoriza crear/editar issues y editar la spec. |

`features_path` se mantiene como input **deprecado pero funcional**: cuando
`spec_ref` viene vacío, `tickets-source@1` usa `features_path`. Esto deja que
los alias de CLI existentes (`orq-lite plan`, `factory`, `issue-fix`) sigan
funcionando sin cambios en `aliases.go` hasta que se migren en un ticket aparte.

### El subflow `tickets-source@1`

Inputs: `spec_ref`, `features_path`, `ticket_store`, `publish_tickets`, `mode`,
y los pass-through que el planner ya recibe (`state`, `implementation`,
`verification`, `triage`, `append`).

Steps:

1. **`resolve`** — `agent.invoke@1` sobre `ticket_planner`, con
   `outputSchema: schema:workflow-state@3`. Es el mismo rol de hoy con vars
   nuevas (`SPEC_REF`, `TICKET_STORE`, `PUBLISH_TICKETS`).
2. **`verify_tickets_exist`** — `command.run@1`, `if: ticket_store == "github"`.
   Corre un `gh issue view` por cada id declarado y falla si alguno no resuelve.
3. **`tickets_are_real`** — `gate.assert@1` sobre la salida del step anterior.

Outputs: `state` (un `workflow-state@3`) y `spec_path`, proyectado desde el
mismo estado para que los consumidores no tengan que navegarlo.

### `schema:workflow-state@3` (no un wrapper)

El primer borrador de este diseño envolvía el estado en un
`schema:ticket-source@1` con un campo `state`. **No se puede.** El validador de
schemas del runtime (`internal/flow/schema.go`) es un subset escrito a mano que
decodifica con `DisallowUnknownFields` y conoce exactamente:
`type`, `properties`, `required`, `additionalProperties`, `items`, `enum`,
`minItems`, `minLength`, `minimum`, `maximum`, `title`, `$schema` y
`x-orq-review-contract`. **No existe `$ref`**, así que un wrapper obligaría a
duplicar las ~50 líneas de `workflow-state@2` inline, y a mantener las dos
copias sincronizadas a mano para siempre.

La salida correcta es una versión nueva del estado: `workflow-state@3` es
`@2` más tres campos requeridos.

```json
{
  "spec_path":   { "type": "string", "minLength": 1 },
  "store":       { "type": "string", "enum": ["local", "github"] },
  "ticket_refs": { "type": "array", "items": { "type": "string" } }
}
```

`workflow-state@2` no se toca: `pack-v5/` lo sigue usando y los refs pinneados
tienen que seguir resolviendo. Dentro de `pack/`, las referencias a `@2` en
`develop-ticket@1` (condición del `while`, `maxIterations`, los gates) y en los
cinco flows pasan a `@3`.

`ticket_refs` es lo que el gate verifica contra `gh`. En modo `local` es `[]`.

### `spec_path`: por qué resuelve el problema más grande

`features_path` **no lo consume solo el planner**. `integrated-review@1` se lo
pasa a `qa`, `adversary`, `critic` y `visual_verifier`, y los cuatro reciben un
*path* que leen del disco. Si la spec pasa a ser un issue, o materializamos el
issue a un archivo o tocamos cinco roles más.

El planner materializa y devuelve:

- **spec local** → `spec_path` es el path original. No se copia nada. Cero
  cambio de comportamiento respecto de hoy.
- **spec en issue** → el planner escribe el cuerpo del issue a
  `.orquestalite/spec.md` y devuelve ese path.

Los consumidores pasan de `{"$ref": "inputs.features_path"}` a
`{"$ref": "steps.tickets.output.spec_path"}`. Sin condicionales, sin schema
nuevo aguas abajo, y con el beneficio lateral de que queda un artefacto
auditable de exactamente qué spec usó el run.

### Contrato de anclaje: la spec declara sus tickets

Para que "el agente busca los tickets en GitHub" sea reproducible y no una
búsqueda por parecido, la spec lleva una sección con formato fijo:

```markdown
## Tickets

- [ ] #123 — Persistir el objetivo del run
- [x] #124 — Exponer el objetivo en el dashboard
- [ ] #125 — Bloqueado por #123: propagar el objetivo al adversary
```

Reglas:

- **Descubrir** = leer la spec (archivo o cuerpo del issue), extraer los `#N` de
  esa sección, y `gh issue view` cada uno. No se consulta ninguna otra fuente.
- **Crear** = después de publicar los issues, el planner escribe esa sección en
  la spec. Si la spec es local, edita el archivo; si es un issue, corre
  `gh issue edit`. Ambas cosas son escritura y requieren `publish_tickets=true`.
- El orden de la lista no implica dependencias. Las aristas siguen viviendo en
  `dependencies` dentro del estado y, para el lector humano, en el texto del
  ticket.
- Una spec sin sección `## Tickets` y con `ticket_store=github` significa
  "todavía no existen", no "no hay".

Esto hace que la spec sea legible por un humano y verificable por una máquina
con el mismo texto, y que el ancla sobreviva a que alguien renombre labels o
mueva el issue de milestone.

### Reglas de fallo cerrado

| Situación | Qué pasa |
|---|---|
| `ticket_store=github`, `publish_tickets=false`, la spec no declara tickets | El run **falla** con un mensaje que dice exactamente qué falta. No planea local en silencio. |
| `ticket_store=github` y `gh` no está en PATH o no está autenticado | El run falla en el primer step, no a mitad del loop. `orq-lite doctor` gana un check de `gh auth status` cuando algún flow del proyecto declara `ticket_store=github`. |
| El planner declara `ticket_refs` que no resuelven | `tickets_are_real` corta el run. Un plan que dice haber creado issues que no existe es indistinguible de uno que no creó nada, y lo segundo tiene que doler. |
| `ticket_store=github` y un issue referenciado fue cerrado a mano entre pases | Se trata como ticket completado, y el `advance` lo registra en `history`. No se reabre. |
| `ticket_store=github` y GitHub no responde a mitad del loop | El pase falla como error transitorio y entra en el retry de la policy. Agotados los reintentos, el run queda `needs_human` con el estado local intacto. |

### Cambios en `prompts/ticket-planner.md`

Una sección nueva, `## Ticket store`, que cubre:

- Cómo discriminar `SPEC_REF`: si resuelve como archivo existente es una spec
  local; si matchea `#N`, `owner/repo#N` o una URL de issue, es un issue.
  Ambiguo (existe el archivo **y** parece ref) → gana el archivo, y se anota en
  `risks`.
- En `TICKET_STORE=github`: leer la sección `## Tickets` de la spec; si existe,
  construir el estado desde esos issues (`gh issue view --json number,title,body,state`)
  en lugar de descomponer de cero; si no existe y `PUBLISH_TICKETS=true`,
  descomponer como siempre, crear un issue por ticket con
  `gh issue create --title ... --body ...`, y escribir la sección de vuelta.
- El `id` del ticket en modo github **es** el número de issue (`"123"`), no
  `T1`. `completed` sigue siendo un array plano de strings, así que no cambia
  de forma.
- En `advance` con store github: releer los issues antes de elegir el frontier.
- Emitir siempre `spec_path`, `store` y `ticket_refs`.

Las reglas de vertical slice, blocking edges y `iteration_budget` no cambian:
son propiedades del plan, no del lugar donde se guarda.

### Qué toca cada archivo

| Archivo | Cambio |
|---|---|
| `packs/development/pack/subflows/tickets-source@1.json` | nuevo |
| `packs/development/pack/schemas/workflow-state@3.json` | nuevo (`@2` intacto para `pack-v5/`) |
| `packs/development/pack/schemas/ticket-store@1.json` | nuevo (`enum`, que el validador sí soporta) |
| `packs/development/pack/prompts/ticket-planner.md` | sección `## Ticket store` |
| `flows/{plan-tickets,task-list,factory-fast,factory-governed,issue-fix}` | inputs nuevos; `plan_tickets` → `subflow:tickets-source@1` |
| `packs/development/pack/subflows/develop-ticket@1.json` | el `replan` usa el subflow; `features_path` → `spec_path` |
| `packs/development/pack/subflows/integrated-review@1.json` | `features_path` → `spec_path` |
| `internal/doctor/doctor.go` | check de `gh auth status` |
| `internal/commands/aliases.go` | `--spec` / `--ticket-store` / `--publish-tickets` |
| `packs/development/pack/pack.json` | digests |

### Testing

El riesgo real no es que el JSON compile — es que el modo nuevo nunca se
ejecute y salga roto, como ya pasó con `factory-governed@2` non-fast. Entonces:

- **Ejecución, no compilación.** Un pack de prueba con `command.run` en lugar de
  agentes reales, que ejercite `tickets-source@1` en sus cuatro combinaciones
  (`local`/`github` × spec-archivo/spec-issue) contra un `gh` falso en PATH que
  devuelve JSON fijo. Sin esto el diseño no se puede declarar terminado.
- **El gate se prueba fallando.** Un test donde el planner declara
  `ticket_refs` inexistentes y se verifica que el run corta. Un gate que nunca
  se vio fallar no es un gate.
- **Fallo cerrado.** Test de `ticket_store=github` + `publish_tickets=false` +
  spec sin sección: el run falla y no escribe tickets locales.
- **No-regresión del camino local.** Los tests existentes de `ticketcommit` y
  `subflowinputs` tienen que pasar sin cambios de expectativa: en modo local
  esto no altera nada.

## Fuera de alcance (fase 2)

- **Write-back de estado:** cerrar el issue cuando el ticket se completa, y
  comentar el resultado de la verificación. Requiere decidir qué pasa cuando el
  run falla después de cerrar.
- **Sub-issues nativos de GitHub** como representación de `dependencies`.
- **Otros trackers** (Linear, Jira). El diseño no los bloquea: `ticket_store` es
  un enum y el ancla vive en la spec, no en una API.
- **Concurrencia:** varios runs tomando del mismo frontier. Hoy
  `maxParallelism: 1` en `development@3` lo hace imposible de todos modos.

## Preguntas abiertas para revisión

1. **La edición de la spec local ensucia el working tree a mitad de run.** El
   loop `develop-ticket@1` commitea por ticket, así que la sección `## Tickets`
   nueva se colaría dentro del primer commit de ticket. Mi lectura es que está
   bien y hasta es deseable — el commit que arranca el trabajo registra qué
   issues lo componen. Pero es un efecto lateral y merece un sí explícito.
2. **`id` = número de issue rompe la legibilidad de `history`.** Las entradas
   pasan de "T3 verified" a "123 verified". Alternativa: mantener `T1..Tn` como
   id y guardar el número en un campo aparte, a costa de una tabla de mapeo que
   el agente tiene que sostener entre pases. Me inclino por el número de issue
   justamente porque no hay mapeo que perder.
3. **`schema:text@1` para `spec_ref` acepta cualquier string.** El validador no
   implementa `pattern`, así que no hay forma de restringirlo en el schema a un
   path o una ref. La validación real ocurre en el agente, que es lo que la
   restricción 1 impone de todos modos. Queda anotado como deuda: si el subset
   de `internal/flow/schema.go` gana `pattern` algún día, este input debería
   estrecharse.
