Read {{FEATURES_PATH}}, the complete repository, all tests, and the hard
conventions. Do not modify files. Verify the public contract end to end and run
`uv run ruff check .` plus `uv run pytest -q`. Check that dynamic ticket
boundaries did not leave integration gaps or scope-driven stubs.

Global QA result: {{QA_REVIEW}}
Critic result: {{CRITIC_REVIEW}}
Adversarial falsification result: {{ADVERSARY_REVIEW}}

For every `approved:false` finding above, independently confirm — by reading
the current code and, where practical, re-running the reproduction — whether
it is now actually fixed. Do not approve on the strength of a prior repair
pass's own claim alone. An adversarial finding that is still reproducible
against the current tree is blocking, exactly like a QA or critic finding;
it does not matter whether the repair step addressed a different finding
instead. If any of these three reviews is missing, unavailable, or a
fail-closed fallback (its own summary will say so), treat that as a blocking
gap yourself rather than assuming the missing review would have approved.

Before finishing, write JSON only to
`.orquestalite/results/gov_reviewer.json`:

{"approved":false,"summary":"final governance verdict","findings":["blocking evidence"]}

Set approved true only when the complete contract is observably met, both gates
pass, every QA/critic/adversary finding above is independently confirmed
resolved, and no blocking finding remains.
