# Usage guard: fuentes unificadas y cadencia

Contexto: el guard consulta el uso antes de lanzar cada agente. Las fuentes
elegidas son correctas (coinciden con CodexBar), pero la cadencia provoca un
429 autoinfligido y el cache local de Claude puede tener un dia de atraso.

Hallazgos medidos:
- `GET /api/oauth/usage` devuelve 429 tras ~6 llamadas rapidas; se recupera en ~4 min.
- `Invalidate` tras cada corrida => 8 corridas = 8 llamadas (cache inutil en el camino exitoso).
- Un 429 cae al PTY de `claude /usage`, que pega contra el mismo limite: 25s perdidos y switch espurio.
- `~/.claude.json:cachedUsageUtilization` tenia 25.9 h de atraso y decia 7d=100% con la realidad en 0%.
- `~/.codex/sessions/**/rollout-*.jsonl` trae `rate_limits.*.used_percent` escrito en cada turno.

## Plan

- [x] 1. `Percent` canonico (0-100 usado) con constructores explicitos por convencion
- [x] 2. `Window.ObservedAt` + rechazo de lecturas viejas (`max_reading_age_seconds`)
- [x] 3. Claude: detectar 429 y no gastar el PTY contra el mismo limite
- [x] 4. Guard: reusar la ultima lectura buena ante fallo de fetch; `Invalidate` deja de borrarla
- [x] 5. Codex: lector local de sesiones como fallback del RPC
- [x] 6. Config + validacion + README
- [x] 7. Verificacion: unit, mutacion, suite completa y probes en vivo

## Review

Hecho:
- `Percent` canonico (0-100 usado) con `Used()` / `Remaining()`; el compilador ahora
  obliga a declarar la convencion en cada frontera de parseo.
- `Window.ObservedAt` + `max_reading_age_seconds` (default 900): una lectura vieja se
  reporta como `stale` y no se aplica, en vez de tratarse como un porcentaje bajo.
- Claude: el 429 se reconoce (`ErrProviderRateLimited`) y ya no escala al PTY de
  `/usage`, que pega contra el mismo limite. Se ahorran los 25s y el switch espurio.
- Guard: retiene la ultima lectura buena y la reusa ante un fallo de fetch; `Invalidate`
  solo vence el TTL en vez de borrarla.
- Codex: `CodexLocalReader` lee `~/.codex/sessions/**/rollout-*.jsonl` (snake_case, a
  diferencia del RPC) como fallback encadenado del app-server.
- README documenta la escala unica, la frescura y la tabla de diferencias por proveedor.

Verificado por mutacion (cada una rompe un test distinto):
- maxAge enorme            -> TestCheckRejectsAReadingObservedTooLongAgo
- descartar la ultima lectura -> TestCheckFallsBackToLastGoodReadingWhenFetchFails
- 429 como error comun     -> TestClaudeReaderSurfacesRateLimitWithoutTryingTheCLI
- aceptar cualquier escala -> TestPercentRejectsValuesOffTheDeclaredScale

Pendiente / no hecho:
- El bloque `extra_usage`/`spend` de Claude (gasto en dolares) sigue sin mirarse.
- Los probes en vivo hay que correrlos de a uno: la trilogia completa gasta 6 llamadas
  y dispara el 429 por si sola.
