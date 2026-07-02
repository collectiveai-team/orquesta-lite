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
- [ ] Task 11b: Proyección SQLite + API consulta serve

## Fase 2 — Endurecimiento del engine
- [ ] Task 12: Validación de flows al cargar
- [ ] Task 13: invalid_contract dentro del fallback loop
- [ ] Task 14: when/break_if/timeout/retry_delay
- [ ] Task 15: Persistencia de estado del flow + flow resume
- [ ] Task 16: Acciones nativas alta fidelidad

## Fase 3 — Los 3 flows por defecto
- [ ] Task 17: factory_fast_governed
- [ ] Task 18: pr_review + watch ruteado a flows
- [ ] Task 19: GitHub Action template + deploy docs
- [ ] Task 20: issue_fix
- [ ] Task 21: flow list + docs de flows

## Fase 4 — Recovery + medic
- [ ] Task 22: internal/recovery + rol medic

## Fase 5 — Bugs + pulido
- [ ] Task 23: Batch correctness bugs
- [ ] Task 24: Refresh documentación
- [ ] Task 25: Verificación de release
