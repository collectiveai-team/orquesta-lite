```bash
export PATH="/usr/local/go/bin:$PATH"
```

# orq-lite Product Readiness — Execution Tracker

Source plan: `docs/superpowers/plans/2026-07-01-product-readiness.md`

## Fase 0 — Desbloqueos P0
- [x] Task 1: Shippear `prompts/intake.md` embebido
- [x] Task 2: Destrackear config de dogfooding
- [x] Task 3: `init` scaffoldea `flows.json` + default flows
- [x] Task 4: `watch` surface errors + `run` empty task list
- [x] Task 5: Archivar el todo histórico

## Fase 1 — Trazabilidad
- [x] Task 6: Run ID + directorio por corrida
- [x] Task 7: Emitir eventos fantasma del dashboard
- [x] Task 8: Persistir artefactos por invocación
- [x] Task 9: Diff por agente/intento
- [x] Task 10: Costo first-party
- [ ] Task 11: task_id en todos los agent_run + log grouped
- [x] Task 11b: Proyección SQLite + API consulta serve

## Fases 2–3 — Runtime durable y flows dinámicos

Las Tasks 12–21 originales fueron reemplazadas por el plan ejecutable
[`docs/superpowers/plans/2026-07-14-durable-dynamic-workflow-runtime.md`](../docs/superpowers/plans/2026-07-14-durable-dynamic-workflow-runtime.md).

- [ ] Milestone 0: baseline y matriz de paridad.
- [ ] Milestone 1: flow spec v2, compiler e IR.
- [ ] Milestone 2: activity model y protocolo externo.
- [ ] Milestone 3: store durable, scheduler y recovery.
- [ ] Milestone 4: CLI, config y observabilidad v2.
- [ ] Milestone 5: control dinámico y pack contract.
- [ ] Milestone 6: aliases, cutover y eliminación del runtime especializado.

## Fase 4 — Recovery + medic
- [ ] Task 22: internal/recovery + rol medic

## Fase 5 — Bugs + pulido
- [ ] Task 23: Batch correctness bugs
- [ ] Task 24: Refresh documentación
- [ ] Task 25: Verificación de release
