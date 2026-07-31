# Lessons

Patrones que ya me costaron tiempo. Leer al empezar sesión.

## Nunca pasar `timeout` a un run largo en background

**2026-07-30.** Lancé `flow run development/factory-governed@1` en background con
`timeout: 600000` pensando que 10 minutos era generoso. El timeout **mata** el
proceso: el run murió durante `batch_qa`, después de que el coder ya había
implementado 40 archivos. No fue un fallo del run, fue el límite que puse yo.

**Regla:** un run gobernado dura horas. En background, no poner `timeout` — o
ponerlo por encima del peor caso real (`maxDurationSeconds` de la policy). El
límite del harness es 600000 ms, o sea que **ningún** valor de `timeout` alcanza
para un run gobernado: la única opción correcta es omitirlo.

Mitigación que sí funcionó: el store durable. `plan_tickets`, `implement_batch`
y los dos gates quedaron `succeeded` y no había que re-pagarlos. Antes de
relanzar algo, mirar `sqlite3 .orquestalite/workflows.db "select step_id,status
from step_runs;"` — casi siempre hay más hecho de lo que parece.

## El cwd de Bash persiste entre llamadas y re-ancla todo lo relativo

**2026-07-30.** Trabajando entre `main` y un worktree, hice
`cd /path/to/main && sed -n ... internal/foo.go` para leer código. Eso dejó el
cwd en `main`, y las **cuatro** verificaciones siguientes —greps de `uv `,
conteo de `metadata.policy`, `go build`, `flow validate`— midieron el árbol sin
tocar. Reporté "tres contradicciones con el tracker" que no existían.

**Regla:** cuando hay más de un árbol en juego, cada comando arranca con
`W=<worktree>; cd "$W" &&`, o usa rutas absolutas. Nunca asumir el cwd. Si un
resultado contradice lo esperado, el primer chequeo es `pwd`, antes de escribir
una sola conclusión.

## Recompilar antes de medir, y mirar si el árbol se movió bajo el binario

**2026-07-30.** Compilé `.tmp/orq-lite-new`, lancé el run de revisión, y el
integrator modificó 26 archivos. Después probé un hallazgo con **ese mismo
binario** y concluí que el agujero seguía abierto. Estaba cerrado: el binario era
anterior al fix. Recompilé y el probe se dio vuelta.

Peor: en la misma tanda leí `agent.go` con `CostUSD` asignado y "corregí" al
critic diciendo que se había equivocado. El plumbing era trabajo del integrator
sin commitear; en el commit auditado había 0 ocurrencias. El critic tenía razón y
yo le di al usuario el sitio de fix equivocado.

**Regla:** antes de verificar cualquier cosa con un binario, recompilar. Y para
auditar lo que dice un revisor, comparar contra el **commit que revisó**
(`git show <sha>:<path>`), no contra el working tree — un run de gobierno cambia
el árbol mientras corre, así que el árbol y el commit son dos cosas distintas.

## Un tracker que el agente se escribió a sí mismo no es evidencia

**2026-07-30.** El `batch_coder` dejó `tasks/todo-governed-pack-v4.md` con
T1–T14 en `[x]`. Todo resultó cierto, pero eso se supo **después** de verificarlo
a mano: binario viejo rechazando el pack nuevo, `policy_source=flow-metadata` vs
`=flag`, fail-fast con `lint_argv` faltante y vacío, refs `@1`/`@4`/sin pin/`@9`,
y el test de budget que sube a mitad de loop corriendo 4 pasadas en vez de 2.

**Regla:** para cada ítem del spec, encontrar la observación que lo distingue de
su ausencia. "El suite está verde" no distingue nada: los tests los escribió el
mismo agente que escribió el código.
