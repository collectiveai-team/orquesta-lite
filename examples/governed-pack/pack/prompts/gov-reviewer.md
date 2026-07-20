Read {{FEATURES_PATH}}, the complete repository, all tests, and the hard
conventions. Do not modify files. Verify the public contract end to end and run
`uv run ruff check .` plus `uv run pytest -q`. Check that dynamic ticket
boundaries did not leave integration gaps or scope-driven stubs.

Before finishing, write JSON only to
`.orquestalite/results/gov_reviewer.json`:

{"approved":false,"summary":"final governance verdict","findings":["blocking evidence"]}

Set approved true only when the complete contract is observably met, both gates
pass, and no blocking finding remains.
