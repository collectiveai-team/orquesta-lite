# Governed pack v4: pack canónico, cap derivado y gates agnósticos — design

**Fecha:** 2026-07-30
**Contexto:** un run real de `development/factory-governed@1` sobre un proyecto
externo necesitó 6 relanzamientos para completar 22 de 24 tickets. El
diagnóstico inicial atribuyó el costo al modelo del `ticket_planner` y a que
el planner "pierde el estado en cada relanzamiento". La inspección del runtime
mostró tres causas distintas, ninguna de ellas el modelo del planner.

## Hallazgos que motivan el spec

### 1. La policy del pack no se aplica salvo que la nombres a mano

`loadWorkflowPolicy` (`internal/commands/workflowcmd.go:483-486`) devuelve
`DefaultPolicy()` cuando no se pasa `--policy`, y `DecodePolicy` no mergea con
defaults: decodifica el JSON tal cual. O sea que el `policies/development@2.json`
que el pack shippea **no se usa nunca** a menos que el operador lo nombre
explícitamente. `DefaultPolicy()` (`internal/workflow/policy.go:31-36`) trae
`MaxAttempts: 32` y ningún cap de duración.

32 attempts / 5 por ticket = **~6 tickets por run**. Contra 24 tickets, eso son
4-6 relanzamientos — que es exactamente lo observado.

Y ya había pasado: `benchmark/results/round2-execution-report.md:163` registra
"Launched without `--policy` → default 32-attempt budget killed the run at 61m",
clasificado entonces como **operator error**. La respuesta fue documentarlo
(`examples/governed-pack/README.md:106`: "always pass `--policy`"). La segunda
ocurrencia, ahora contra un proyecto real y con un agente como operador, es la
evidencia de que documentar un footgun no lo arregla.

### 2. El cap que frenaba no era `maxIterations`

Incluso pasando `--policy`, el freno no es el cap de iteraciones.
`maxAttempts` y `maxAgentAttempts` son presupuestos **run-wide** acumulados
contra `RunUsage`, no por paso (`internal/workflow/scheduler.go:704-712`).
`develop-ticket@1` gasta 3 invocaciones de agente por ticket
(`implement_ticket`, `verify_ticket`, `update_ticket_plan`) y 5 attempts
totales.

| Cap | Valor | Tickets que permite |
|---|---|---|
| `DefaultPolicy.maxAttempts` (sin `--policy`) | 32 | **~6** ← el caso real |
| `development@2.maxAgentAttempts` | 48 | ~15 |
| `development@2.maxAttempts` | 96 | ~19 |
| `maxIterations` (flow) | 20 | 20 |
| `development@2.maxDurationSeconds` | 28800 | — |

El literal `maxIterations: 20` es el cap **menos** restrictivo de la lista.
Nunca fue el que cortó. Subirlo solo, sin tocar nada más, no habría cambiado
nada.

### 3. La versión del pack está acoplada a la del flow

`internal/commands/workflowcmd.go:56-68` parsea `pack/flow@V` y usa la **misma
V** para buscar el directorio del pack: `development/factory-governed@1` →
`.orquestalite/packs/development/1`. Un solo número hace dos trabajos. No hay
forma de expresar "pack 4, flow 1"; para publicar un pack v4 hay que renombrar
los flows a `@4`, que es exactamente lo que hizo
`benchmark/round3/pack-development-4/` (`flows/factory-governed@4.json`).

### 4. Los gates hardcodeados a Python son del subflow compartido

No eran del pack de round3. Están en `develop-ticket@1`, que ambos packs
comparten:

- `examples/governed-pack/pack/subflows/develop-ticket@1.json:41` → `["uv","run","ruff","check","."]`
- `examples/governed-pack/pack/subflows/develop-ticket@1.json:47` → `["uv","run","pytest","-q"]`
- `examples/governed-pack/pack/flows/factory-governed@1.json:70` → `["uv","run","python","-c", ...]`

Desinstalar el pack v4 no los saca. Mientras tanto `team.json` ya declara
`lint_command` y `full_test_command`, que el pack ignora.

Y no son solo los gates: **los prompts hardcodean los mismos comandos en las
instrucciones que le dan a los agentes** — 8 ocurrencias en `coder.md` (2+2),
`batch-coder.md`, `integrator.md`, `gov-reviewer.md`, `ticket-qa.md`, más una
referencia a "pytest test" en `adversary.md`. Parchear solo el `argv` de los
flows deja a cada rol *instruido* para correr `uv run ruff check .` contra el
repo. Descubierto al preparar el bootstrap para dogfoodear este spec.

### 5. Los `plan_tickets` repetidos no son un feature faltante

`orq-lite flow resume <run-id>` existe (`workflowcmd.go:384-388`) y el store
durable no re-ejecuta pasos completados. Los 6 relanzamientos fueron runs
**nuevos**: `--source-key` es opcional (`splitRunOptions`) y sin él no hay
deduplicación. Es un problema de descubribilidad, no de runtime.

## Objetivo

Un pack governed canónico en versión 4 que no dependa de que el operador
recuerde nada: su policy se aplica sola, su presupuesto de iteración sale del
plan en vez de un literal, sus gates salen de la config del proyecto en vez del
lenguaje del benchmark, y sus límites dejan de ser un cap encubierto de tickets.

El hilo común de los cinco hallazgos es el mismo: **el pack declara algo y el
runtime no lo levanta por defecto.** La policy hay que nombrarla, la versión del
pack hay que codificarla en la del flow, el presupuesto del loop es un literal
que el planner no puede tocar, y los comandos de gate ignoran la config que el
proyecto ya declaró. Cada uno se arregló antes con documentación; este spec los
arregla en el código.

## Diseño

### A. Un pack canónico `development@4`

`examples/governed-pack/pack/pack.json` pasa a `"version": "4"`. Del pack de
round3 hay exactamente **un** archivo que absorber: `prompts/ticket-planner.md`.
Verificado con `diff` archivo por archivo — los otros 7 prompts son
byte-idénticos y `integrated-review@3` es estructuralmente igual al `@1`.

El `ticket-planner.md` de round3 es un fork divergente: endurece `completed`
(ids como strings desnudos, con ejemplo contrastando contra `history`) pero le
faltan las secciones `APPEND` y `TRIAGE` que el `@1` ganó después para
`issue-fix`. El merge toma las dos mitades:

- Del `@1`: bloques `Append mode: {{APPEND}}`, `Triage: {{TRIAGE}}` y sus dos
  párrafos de instrucción.
- Del round3: la instrucción endurecida de `completed` + el ejemplo JSON.

`benchmark/round3/pack-development-4/` queda como artefacto histórico de
benchmark. No se instala y no se mantiene.

Los digests se regeneran con `examples/governed-pack/regen-digests.py`.

### B. Ref desacoplado: `pack[@V]/flow@V`

`compileWorkflowTarget` deja de derivar la versión del pack de la del flow.

```
development/factory-governed@1      # pack: la más alta instalada; flow: 1
development@4/factory-governed@1    # pin explícito
development@1/factory-governed@1    # el pack viejo, para comparar
```

`versionPattern` (`internal/flow/spec.go:163`) admite `1`, `1.2`, `1.2.3` y
sufijos de prerelease, así que la selección de "la más alta" necesita orden
semver real, no comparación de entero. Se implementa como una función pura en
`internal/flow` con tests de tabla, incluyendo el orden de prereleases.

Cuando no hay match, el error lista las versiones que sí están instaladas en
lugar de solo el directorio buscado.

La auditoría no se afloja: `PinPack` (`internal/flow/pack.go:47`) ya graba
name + version + digest en el IR, y `flow run` ya imprime
`pack=<name>@<version>:<digest>`. Cada run queda con el pack exacto que usó,
independientemente de cómo se escribió el ref.

**Compatibilidad:** un proyecto con una sola versión instalada no cambia de
comportamiento. Un proyecto con dos versiones y un ref sin pin pasa a resolver
la más alta — que es el cambio buscado.

### C. Cap de iteración derivado del plan

#### C.1 `workflow-state@2`

Nuevo schema (no mutación del `@1`, para que los runs ya pineados conserven su
contrato):

```json
"iteration_budget": { "type": "number", "minimum": 1, "maximum": 200 }
```

Requerido. El `ticket_planner` es el rol que sabe cuántos tickets hay y cuánto
tienden a abrirse — el presupuesto lo declara él, no un factor inventado en el
flow. El `maximum: 200` es el techo duro: se valida en cada emisión del agente
por la validación de output que ya existe (`validateSchema`), así que un
planner que quiera crecer sin límite falla el contrato.

Blast radius: 7 documentos del pack (8 refs a `schema:workflow-state@1`) +
`prompts/ticket-planner.md` + `prompts/batch-coder.md`. Los prompts de planner
deben emitir el campo nuevo y explicar la regla: presupuesto = pendientes +
margen para aperturas, y se **re-emite** en cada advance, subiéndolo si el
replan descubrió trabajo nuevo.

#### C.2 `WhileSpec.MaxIterations` pasa de `int` a `Value`

Simétrico con `While.Initial`, que ya es `Value` y ya se resuelve en
`executeWhile` (`internal/workflow/scheduler.go:356`). `Value` deserializa un
número literal igual que hoy (`internal/flow/spec.go:82`), así que
`"maxIterations": 20` sigue compilando — los packs existentes no se rompen.

Cambios:

- `internal/flow/spec.go:49` — el campo pasa a `Value`.
- `internal/flow/decode.go:100` — la validación `MaxIterations < 1` aplica solo
  cuando el valor es un literal numérico; un `$ref` se valida en runtime.
- `internal/flow/compiler.go:155` — agregar
  `validateValue(path+".while.maxIterations", step.While.MaxIterations, true)`.
  `allowItem: true` (a diferencia de `while.initial`, que es `false`).

#### C.3 Re-resuelto en cada pasada

El bound se resuelve **dentro** del loop, con el resolver que ya tiene `item`
seteado, no una sola vez antes de entrar:

```json
"while": {
  "condition": "item.state.status == \"active\"",
  "maxIterations": {"$ref": "item.state.iteration_budget"},
  "initial": {"state": {"$ref": "steps.plan_tickets.output"}}
}
```

En la iteración 0, `item` es el `initial` ya resuelto — o sea el output de
`plan_tickets` — así que la referencia funciona uniformemente desde la primera
pasada sin necesidad de una expresión aparte para el valor inicial.

Esto es lo que hace que el cap sea de verdad adaptativo: si un ticket se abre
en cuatro a mitad de vuelo (el caso T12: 12 consumidores descubiertos contra un
criterio de aceptación que asumía uno), el replan sube su presupuesto y el loop
no muere. La `condition` ya se re-evalúa por pasada; re-evaluar el bound es
consistente con eso.

**Backstop de runaway.** Como el loop puede extenderse solo, se agrega una
constante en el scheduler:

```go
const whileIterationCeiling = 1000
```

Un bound resuelto que exceda el techo, no sea numérico, no sea entero o sea
< 1 aborta el paso con un error explícito citando el valor. Esto no es
redundante con el `maximum: 200` del schema: el schema protege *este* pack, la
constante protege el runtime de cualquier flow.

El hint de capacidad `make([]any, 0, step.While.MaxIterations)`
(`scheduler.go:360`) pasa a una constante chica.

### D. La policy: se aplica sola, y deja de contar attempts

#### D.1 El flow declara su policy

Es el fix de mayor impacto del spec. Sin esto, arreglar `development@3` no
cambia nada: el archivo nunca se carga.

`Metadata` (`internal/flow/spec.go:21`) gana un campo opcional:

```json
"metadata": {
  "name": "factory-governed",
  "version": "1",
  "policy": "policy:development@3"
}
```

Precedencia en `loadWorkflowPolicy`:

1. `--policy=<ref|path>` explícito — sigue ganando, para experimentos y benchmarks.
2. `metadata.policy` del flow.
3. `DefaultPolicy()`.

El compilador resuelve el ref igual que ya resuelve los de `retry`, metiéndolo
en `ir.Policies` (`internal/flow/compiler.go:132,170,244`), así que la policy
declarada queda pineada dentro del IR y entra en el digest de definición: un
run graba con qué presupuesto corrió sin depender de lo que tipeó el operador.
`IR.Metadata` ya se copia desde el documento (`compiler.go:34`), así que la
propagación es automática.

`flow run` pasa a imprimir el origen de la policy además del hash
(`policy=policy:development@3 policy_source=flow-metadata`), para que un
operador — humano o agente — vea qué presupuesto se aplicó sin inferirlo.

`DefaultPolicy()` con sus 32 attempts se queda como fallback para flows
ad-hoc sueltos, pero ningún flow del pack governed va a caer ahí.

**Los 7 flows del pack declaran su policy.** No solo `factory-governed`: un
`issue-fix` o un `review-existing` lanzados sin `--policy` tienen el mismo
footgun.

#### D.2 Los budgets dejan de contar attempts

Contar attempts es el proxy equivocado cuando el largo del loop es
data-dependent: cualquier número elegido es un cap encubierto de tickets.

Nuevo `policies/development@3.json` (versión nueva, no mutación — el `@2` queda
para los runs que ya lo pinearon):

**Primera versión, descartada.** El diseño original ponía `maxAttempts: 0` y
`maxAgentAttempts: 0` (`0` = sin límite, `scheduler.go:704-709` gatea con `> 0`)
y delegaba el freno a `maxCostUSD: 250`. La revisión adversarial lo rechazó con
razón: `internal/cost/prices.go` no tiene entradas para `claude-opus-5`,
`claude-sonnet-5` ni `gpt-5.5`, así que `EstimateUSD` devuelve `!ok`,
`runSpendUSD` devuelve 0 en silencio y el costo acumulado queda en `$0` para
siempre. Verificado sobre el store del propio repo: 14 attempts, `SUM(cost_usd)
= 0.0`. Neto: cambié un freno mal dimensionado por uno que no existe, dejando el
reloj de 8h como único límite real.

**Como quedó construido.** El backstop de attempts vuelve, pero **derivado del
techo del propio loop en vez de adivinado** — que es la distinción que faltaba
la primera vez:

```json
{
  "maxDurationSeconds": 28800,
  "maxAttempts": 2400,
  "maxAgentAttempts": 1200,
  "maxCostUSD": 250,
  "maxParallelism": 1,
  "retries": { ... sin cambios ... }
}
```

`iteration_budget` tope 200 pasadas × 3 agentes por pasada en `develop-ticket@1`
= 600 invocaciones legítimas, más el plan y la revisión integrada: 612 agentes y
~1024 attempts en el peor caso legítimo. Los caps quedan ~2× por encima, así que
**no pueden ligar antes que el bound declarado del loop**. Eso es lo que hace la
diferencia entre un backstop y un cap de tickets encubierto: el `48` histórico
paraba un run gobernado a los ~15 tickets.

`TestGovernedPackAttemptBackstopExceedsItsOwnLoopCeiling` fija la derivación, no
los números: lee el `maximum` del schema y cuenta los pasos de agente del
subflow, así que agregar un rol mueve el piso solo. Con el `48` histórico falla
nombrando el 600.

`maxCostUSD: 250` se queda —funciona para modelos que sí están tarifados— pero el
README ya no lo vende como freno activo. Dos cosas pendientes para que lo sea:
las entradas de la familia 5 en `prices.go`, y que `EstimateUSD` deje de cobrar
input cacheado a precio full (en el run de revisión, 10.612.146 de 10.612.286
tokens de input fueron cacheados).

Para presupuestar de verdad, `--policy` con un archivo propio sigue disponible y
gana por precedencia explícita.

`maxParallelism: 1` no se toca. Los tickets declaran dependencias y todos
escriben sobre el mismo working tree; sin worktrees aislados dos coders se
pisan. Subirlo es otro spec.

### E. Gates desde la config del proyecto

#### E.1 Namespace `config.` en el resolver

Nuevo root read-only en `executionState.resolve` (`scheduler.go:790`), al lado
de `inputs` / `steps` / `item` / `attempt` / `run`. Namespace **plano**: solo
`config.<clave>`, sin anidamiento, lo que hace que la validación del whitelist
sea una comparación de dos segmentos.

- `internal/flow/compiler.go:275` (`validateReference`) — nuevo
  `case "config":` que exige `len(parts) == 2`.
- `internal/workflow/scheduler.go:24` (`Runtime`) — nuevo campo
  `Config map[string]any`.
- `internal/commands/workflowcmd.go` (`newWorkflowDeps`) — lo puebla desde
  `team.json` con un whitelist explícito.

Whitelist inicial, dos claves:

```json
// team.json
"lint_argv": ["go", "vet", "./..."],
"test_argv": ["go", "test", "./..."]
```

Son arrays, no strings: `allowShell` es `false` en la policy y la forma shell se
rechaza en `scheduler.go:513`. Los `lint_command` / `full_test_command` string
se quedan sin tocar para el engine v1.

**No se persiste un snapshot de la config en el `Run`.** El `RoleInvoker` ya se
reconstruye desde `team.json` en cada `resume` — prompts, modelos y timeouts ya
salen live de la config, no pineados. Pinear solo el argv de los gates sería
inconsistente, y agregar una columna al store para eso no se paga. Los valores
resueltos quedan igual auditables: `StepRun.Inputs` graba el argv efectivo de
cada attempt.

#### E.2 Validación fail-fast al arrancar

Un `config.` roto que explota a los 20 minutos de run es inaceptable. Después
de compilar y antes de `runtime.Start`, `compileWorkflowTarget` (donde la config
sí está disponible) recorre el IR buscando refs `config.*` y verifica que cada
clave exista en el whitelist **y** que su valor sea un array de strings no
vacío. Cualquier fallo aborta antes de que arranque el run, en la línea del
commit `a30e1b2` (watch falla rápido si un flow ref no compila).

El array no vacío es deliberado: un gate que se saltea en silencio es peor que
uno que falla. Es la lección de round 3 — sin gate bloqueante, el bug shippea
con la aprobación de governance encima.

#### E.3 `gate.assert@1`, y muere el último `uv`

`factory-governed@1:70` usa `uv run python -c "import json,sys; ..."` para una
aserción sobre JSON. No se puede reemplazar con lo que ya existe: el resolver no
indexa arrays (`scheduler.go:832` castea a `map[string]any` y nada más), así que
la salida del `while` no es referenciable, y el gate actual lee del archivo de
resultados del rol.

Nuevo builtin en `internal/activity/builtin/`, `EffectPure`:

```json
{
  "id": "ticket_plan_complete",
  "uses": "activity:gate.assert@1",
  "with": {
    "value": {"$ref": "..."},
    "equals": "complete",
    "message": "el plan de tickets no llegó a complete"
  }
}
```

Pasa si `value == equals`; si no, falla con `ErrorGateFailed` y el `message` en
el error. Sin shell, sin Python, agnóstico de lenguaje. Se registra en
`builtinSpecs()` (`workflowcmd.go:41`).

#### E.4 Los prompts también dejan de nombrar comandos

Los 6 prompts que hoy dicen `uv run ruff check .` / `uv run pytest -q` pasan a
referirse a los comandos por su rol, no por su texto.

**Como quedó construido** (deviación deliberada del diseño original): los
prompts nombran la fuente — "los gates configurados del proyecto (`lint_argv` y
`test_argv` en `team.json`, los mismos comandos que corren los gate steps de
este flow)" — en vez de recibir los comandos resueltos como variables
(`{{LINT_CMD}}`, `{{TEST_CMD}}`) inyectadas desde `config.lint_argv`. Es más
simple y no toca los flows; el costo es que el agente tiene que leer `team.json`
por su cuenta en lugar de recibir el comando literal. Si aparece un rol que
reporta comandos equivocados, la variable inyectada es el siguiente paso.

Sin esto, E.1 arregla el gate pero deja al `coder` y al `integrator` corriendo
`uv run pytest` por su cuenta y reportando gates que nunca pasaron. Es el mismo
patrón de round 3: el rol hace su trabajo contra un contrato que nadie conectó.

### F. Ergonomía para un agente que orquesta

- **`orq-lite pack list`** — hoy solo existe `pack install` (`packcmd.go:23`), o
  sea que un agente no tiene forma programática de preguntar qué packs y qué
  versiones hay instalados. Lista name, version, digest y cantidad de archivos,
  ordenado por versión con la resolución por defecto marcada.
- **Aviso de resume en `flow run`** — si ya existe un run no terminado del mismo
  `flowRef`, imprime su run-id y el comando `orq-lite flow resume <id>` antes de
  arrancar. No cambia semántica: sigue arrancando el run nuevo. Solo hace
  visible lo que ya existe.
- **Docs que dejan de pedir disciplina** — `examples/governed-pack/README.md:106`
  ("always pass `--policy`"), `README.md:393` y `benchmark/README.md:136`
  documentan workarounds que D.1 y B vuelven innecesarios. Se actualizan en el
  mismo cambio: una instrucción que ya no hace falta es una trampa para el
  próximo operador.

## Lo que este spec no hace

- No sube `maxParallelism`. Requiere worktrees aislados por ticket.
- No cambia el modelo del `ticket_planner`. Es una decisión de costo por run,
  no de estructura, y `team.json` ya la expone.
- No toca `integrated-review`. El pack de round3 no aportaba nada ahí.
- No arregla el crecimiento del agregado del `while`: los outputs de todas las
  iteraciones se acumulan en memoria y se serializan juntos
  (`scheduler.go:390`), y `workflow-state` incluye un `history` que crece. Con
  el techo de 200 esto empeora. Queda anotado como riesgo conocido, medible
  contra `maxWorkflowValueBytes` (8 MiB).

## Testing

**Unit**
- Precedencia de policy: `--policy` > `metadata.policy` > `DefaultPolicy()`; y
  que un `metadata.policy` que no resuelve falla en compilación, no en runtime.
- Orden semver de versiones de pack, incluyendo prereleases y `1` vs `1.0` vs `1.0.0`.
- `Value` en `maxIterations`: literal numérico (compatibilidad), `$ref` válido,
  no numérico, no entero, `< 1`, y por encima de `whileIterationCeiling`.
- `validateReference` con `config.x` (ok), `config` pelado, `config.a.b` (rechazado).
- `gate.assert@1`: match, no-match, tipos distintos, `message` en el error.

**Integración**
- Un `while` cuyo `iteration_budget` sube a mitad de loop corre más pasadas que
  el presupuesto inicial. Es el test que representa el caso T12 y la razón de
  ser de C.3.
- Un `while` cuyo budget se mantiene corta exactamente donde debe.
- Refs `pack/flow@V`, `pack@V/flow@V` y ausencia de versión con dos packs
  instalados, verificando el `pack=` impreso.
- Startup falla cuando `lint_argv` falta, no es array, o está vacío.
- El gate de un pack v4 corre los comandos de `team.json` y no `uv`.

**Integración (cont.)**
- Un run de `factory-governed` **sin** `--policy` corre con `development@3` y no
  con los 32 attempts del default. Es el test que representa la regresión de
  round 2 y del run real; sin él, el footgun vuelve.

**Gate de paridad**
`internal/commands/governedpack_test.go` y el gate de development-pack tienen
que seguir verdes contra el pack v4.

Dos greps que tienen que dar cero, y el alcance del primero importa:
`grep -rn "uv " examples/governed-pack/pack/` — **solo bajo `pack/`** — y
cualquier flow del pack sin `metadata.policy`.

El gate original decía `examples/governed-pack/` entero, y estaba mal: después
de E.1 los comandos del lenguaje viven precisamente en el `team.json` del
proyecto de ejemplo, que es Python. Tres ocurrencias legítimas quedan fuera de
`pack/` (`team.json` y `features.md` del ejemplo) y un gate que no distingue "el
pack hardcodea Python" de "el ejemplo declara su toolchain" es un gate que
obliga a romper el ejemplo para pasar.
