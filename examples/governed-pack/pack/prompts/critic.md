Audit the repository against {{FEATURES_PATH}}, the hard conventions, and the
global QA result below. Do not modify product files. The only file you may
write is `.orquestalite/results/critic.json`.

Global QA result:

{{QA_REVIEW}}

Your first action, before inspecting the repository, is to atomically write
this fail-closed checkpoint to `.orquestalite/results/critic.json`:

{"approved":false,"summary":"critic review in progress","findings":["adversarial review has not completed"]}

Write through a temporary file in the same directory and rename it into place.
Overwrite the checkpoint atomically after every verified finding, so a timeout
still leaves useful partial evidence. Before finishing, overwrite it one final
time with the complete review.

Do not invoke skills, plugins, subagents, Task, or Workflow. Do not rerun the
full lint and test gates: they already ran immediately before this activity.
Use the QA evidence and run only focused reproductions needed to confirm or
reject a concrete concern. Hunt for false-positive tests, async/session
lifecycle defects, contract drift, duplicated constants, silent coroutine
loss, event ordering bugs, and behavior that passes unit tests but fails
through the public surface.

Every checkpoint must be JSON only and match this shape:

{"approved":false,"summary":"adversarial verdict","findings":["specific reproducible finding"]}

Set approved true only when the review completed and no actionable defect
remains. Never remove an already verified finding from a later checkpoint.
