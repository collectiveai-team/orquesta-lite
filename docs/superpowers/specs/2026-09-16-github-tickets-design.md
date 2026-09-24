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
   parsea `&& || == != >= <= > <`, referencias y literales, y nada más. Un flow
   **no puede** inspeccionar un string para decidir si parece un path o una
   referencia a issue: no hay `startsWith` ni `matches`. Sí puede comparar contra
   un literal, incluido `== ""`, y de ahí sale el diseño de dos campos
   excluyentes en lugar de un campo polimórfico que alguien tenga que adivinar.
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
4. **La spec entra por dos campos excluyentes, no por uno polimórfico.**
   La restricción 1 impide ramificar sobre la *forma* de un string, pero no
   sobre si está **vacío**: `expr.go` lexea literales y `if: inputs.spec_issue == ""`
   compila y evalúa. Así que la discriminación vive en el flow, deterministica,
   en vez de depender del criterio del agente. Un `spec_path` mal escrito falla
   al leerlo, antes de gastar una invocación. Lo que el agente sí resuelve es lo
   que ningún `if` puede: si los tickets ya existen en GitHub o hay que crearlos.
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
| `spec_path` | `schema:path@1` | `""` | La spec como archivo del repo (`features.md`). |
| `spec_issue` | `schema:text@1` | `""` | La spec como issue (`#123`, `owner/repo#123`, o URL). |
| `ticket_store` | `schema:ticket-store@1` (nuevo, enum `local`\|`github`) | `"local"` | Dónde viven los tickets. |
| `publish_tickets` | `schema:flag@1` | `false` | Autoriza crear/editar issues y commitear la spec. |

`spec_path` y `spec_issue` son **mutuamente excluyentes**: exactamente uno tiene
que venir no vacío. Un `gate.assert@1` al inicio de `tickets-source@1` rechaza
los dos puestos y los dos vacíos, con mensajes distintos — "ambiguo" y "falta la
spec" son errores del operador diferentes y merecen texto diferente.

`features_path` se mantiene como input **deprecado pero funcional**: cuando
`spec_path` y `spec_issue` vienen vacíos, `tickets-source@1` usa `features_path`
como `spec_path`. Esto deja que los alias de CLI existentes (`orq-lite plan`,
`factory`, `issue-fix`) sigan funcionando sin cambios en `aliases.go` hasta que
se migren en un ticket aparte. La exclusión mutua se evalúa después de ese
fallback, así que `features_path` + `spec_issue` también es ambiguo.

### El subflow `tickets-source@1`

Inputs: `spec_path`, `spec_issue`, `features_path`, `ticket_store`,
`publish_tickets`, `mode`, y los pass-through que el planner ya recibe
(`state`, `implementation`, `verification`, `triage`, `append`).

Steps:

1. **`spec_is_unambiguous`** — `gate.assert@1`. Exactamente una de `spec_path` /
   `spec_issue` no vacía, evaluado después del fallback a `features_path`.
2. **`fetch_issue_spec`** — `command.run@1`, `if: inputs.spec_issue != ""`.
   `gh issue view <ref> --json title,body` y el planner lo materializa a
   `.orquestalite/spec.md`. En el camino local este step ni se materializa.
3. **`resolve`** — `agent.invoke@1` sobre `ticket_planner`, con
   `outputSchema: schema:workflow-state@3`. Es el mismo rol de hoy con vars
   nuevas (`SPEC_PATH`, `SPEC_ISSUE`, `TICKET_STORE`, `PUBLISH_TICKETS`).
4. **`verify_tickets_exist`** — `command.run@1`, `if: ticket_store == "github"`.
   Corre un `gh issue view` por cada id declarado y falla si alguno no resuelve.
5. **`tickets_are_real`** — `gate.assert@1` sobre la salida del step anterior.
6. **`commit_spec`** — `git.commit@1`, `if: publish_tickets == true`, con
   `type: "chore"`, `scope: "spec"` y un subject que nombra los issues creados.
   Ver "El ledger se commitea solo", abajo.

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

### El ledger se commitea solo

Cuando el planner crea los issues y escribe la sección `## Tickets` en una spec
local, deja `features.md` modificado y sin commitear. `git.commit@1` hace
`git add -A` (`internal/activity/builtin/gitcommit.go:156`), así que sin un step
propio esa sección viaja **dentro del primer commit de ticket**: el commit cuyo
mensaje dice "ticket #101" llevaría además el registro de los seis issues, y su
mensaje dejaría de describir su propio diff.

Peor que eso es la ventana: si el run muere después de crear los issues y antes
del primer commit, los issues existen en GitHub y la spec queda sucia en el
working tree con una sección que el usuario no escribió. Recuperable —el
re-run lee la sección y encuentra los issues en vez de duplicarlos— pero deja un
cambio local que nadie pidió.

Por eso `commit_spec` corre dentro de `tickets-source@1`, apenas publicados los
issues:

```
chore(spec): link tickets #101-#106
```

El commit del ticket queda conteniendo solo el ticket, y la ventana sucia se
cierra en el mismo pase que la abre. `git add -A` sigue barriendo cualquier otro
cambio pendiente del working tree, pero eso ya es cierto para todo commit que el
pack hace hoy: no es algo que este diseño introduzca.

Cuando la spec es un issue, el ancla se escribe con `gh issue edit` y no hay
nada que commitear: `commit_spec` reporta "nothing to commit" y sigue.

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

- Qué spec leer: el flow ya garantizó que llega exactamente una de `SPEC_PATH`
  o `SPEC_ISSUE`, así que el prompt no discrimina formas de string — lee la que
  venga no vacía. Con `SPEC_ISSUE`, materializa el cuerpo a `.orquestalite/spec.md`
  y devuelve **ese** path en `spec_path`.
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
| `flows/{plan-tickets,task-list,factory-fast,factory-governed,issue-fix}` | inputs nuevos (`spec_path`, `spec_issue`, `ticket_store`, `publish_tickets`); `plan_tickets` → `subflow:tickets-source@1` |
| `packs/development/pack/subflows/develop-ticket@1.json` | el `replan` usa el subflow; `features_path` → `spec_path` |
| `packs/development/pack/subflows/integrated-review@1.json` | `features_path` → `spec_path` |
| `internal/doctor/doctor.go` | check de `gh auth status` |
| `internal/commands/aliases.go` | `--spec` / `--spec-issue` / `--ticket-store` / `--publish-tickets` |
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
- **Exclusión mutua.** Tres tests sobre `spec_is_unambiguous`: las dos puestas,
  las dos vacías, y `features_path` + `spec_issue`. Cada uno con su mensaje.
- **El commit del ticket queda limpio.** Con `publish_tickets=true` y spec
  local, el commit del primer ticket **no** contiene la sección `## Tickets`:
  ya la commiteó `commit_spec`. Es la verificación de que el step sirve para
  algo, y falla si alguien lo borra.
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

## Resuelto en revisión (2026-09-23)

1. **La spec se commitea sola.** Se evaluó dejar que la sección `## Tickets`
   viajara dentro del primer commit de ticket. Se descartó: el mensaje dejaría
   de describir su diff, y un run caído entre la creación de issues y el primer
   commit dejaría la spec sucia sin que nadie la haya tocado. `commit_spec`
   cierra las dos cosas por un step.
2. **`id` = número de issue.** Se aceptó que `history` diga "123 verified" en
   vez de "T3 verified". La alternativa —ids `T1..Tn` más una tabla de mapeo a
   números de issue— pone en el agente la obligación de sostener esa tabla entre
   pases, y una tabla que el agente sostiene es una tabla que el agente pierde.
3. **Dos campos excluyentes en vez de un `spec_ref` polimórfico.** El borrador
   original tenía un solo input que el agente discriminaba, justificado en que
   la restricción 1 impide ramificar sobre strings. Es cierto para la *forma*
   del string y falso para si está **vacío**: `expr.go` lexea literales y
   `inputs.spec_issue == ""` evalúa sin problema. Con dos campos, un path mal
   escrito falla al leerlo en vez de convertirse en una búsqueda en GitHub
   adentro de una invocación de agente.

Queda como deuda, no como pregunta: `internal/flow/schema.go` no implementa
`pattern`, así que `spec_issue` acepta cualquier string y su forma solo se
valida al usarla. Si el subset de schemas gana `pattern`, estrechar ese input.
