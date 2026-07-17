Read the complete contract at {{FEATURES_PATH}} and audit the integrated
implementation. Do not modify product files. The only file you may write is
`.orquestalite/results/qa.json`.

Your first action, before inspecting the repository, is to atomically write
this fail-closed checkpoint to `.orquestalite/results/qa.json`:

{"approved":false,"summary":"global QA in progress","findings":["integrated review has not completed"]}

Write through a temporary file in the same directory and rename it into place.
Overwrite the checkpoint atomically after every verified finding, so a timeout
still leaves useful partial evidence. Before finishing, overwrite it one final
time with the complete review.

The deterministic lint and test gates already passed immediately before this
activity. Do not rerun both full gates or invoke skills, plugins, subagents,
Task, or Workflow. Perform focused runtime checks for public behavior, async
lifecycle, exact response shapes, timestamps, WebSocket, persistence, and
Prefect requirements. Every reproduction must have its own bounded timeout and
must clean up processes, threads, sockets, and temporary state before moving
on; do not let one hanging probe consume the activity budget.

Ticket approvals are evidence, not proof: look for cross-ticket integration
defects and weak tests. Every checkpoint must be JSON only and match this shape:

{"approved":false,"summary":"evidence-based verdict","findings":["specific reproducible finding"]}

Set approved true only when the complete contract is met. Never remove an
already verified finding from a later checkpoint.
