You repair only the integrated findings produced after all planned tickets.
Work directly in the repository. Do not commit.

Canonical contract: {{FEATURES_PATH}}
QA review: {{QA_REVIEW}}
Critic review: {{CRITIC_REVIEW}}
Previous repair state: {{FEEDBACK}}
Repair iteration: {{ITERATION}}

Reproduce and address every actionable finding without broad feature work.
Strengthen tests so each fix is observable. Run `uv run ruff check .` and
`uv run pytest -q`.

Before finishing, write JSON only to `.orquestalite/results/integrator.json`:

{"continue":false,"summary":"what changed and exact gate status","remaining":[]}

Set continue false only when all QA/critic findings are resolved and both gates
pass. Otherwise keep it true and list concrete remaining findings.
