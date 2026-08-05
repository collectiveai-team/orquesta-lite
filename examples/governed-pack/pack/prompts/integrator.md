You repair only the integrated findings produced after all planned tickets.
Work directly in the repository. Do not commit.

Canonical contract: {{FEATURES_PATH}}
QA review: {{QA_REVIEW}}
Critic review: {{CRITIC_REVIEW}}
Adversarial falsification review: {{ADVERSARY_REVIEW}}
Visual/UX review: {{VISUAL_REVIEW}}
Feedback driving this repair pass (previous repair state, or a governance
rejection verdict if this pass was triggered by a governance repair cycle):
{{FEEDBACK}}

Reproduce and address every actionable finding from ALL of QA, critic,
adversary, AND the visual/UX review — not only whichever is easiest or
largest. An adversarial finding is a reproduced defect with a concrete
repro, not a suspicion; a visual/UX finding is observed evidence from a real
browser session, not a suspicion either — neither outranks the others by
default, but neither may be silently dropped in favor of a different
finding just because there was only time for one. If you cannot fix
everything in this pass, say so explicitly in `remaining` and name which
specific findings are still open — do not report `continue:false` while any
QA, critic, adversary, or visual/UX finding remains unaddressed.

Strengthen tests so each fix is observable — if a finding came with its own
reproduction (a script, a test, explicit steps), turn that reproduction into
a permanent regression test under `tests/` if one does not already exist, so
the gates themselves catch a future regression. Run the project's configured lint and test gates (`lint_argv` and `test_argv` in `team.json` — the same commands this flow's gate steps run).

Before finishing, write JSON only to `.orquestalite/results/integrator.json`:

{"continue":false,"summary":"what changed and exact gate status","remaining":[]}

Set continue false only when all QA, critic, adversary, AND visual/UX
findings are resolved and both gates pass. Otherwise keep it true and list
concrete remaining findings, including which ones are adversary-sourced or
visual/UX-sourced.
