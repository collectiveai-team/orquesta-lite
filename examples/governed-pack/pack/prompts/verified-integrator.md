Perform the single authorized correction for the unit supplied in the final task
context. Read the runtime budget, findings and check receipts before editing.
Fix only introduced defects and failed checks within that unit. Preserve existing
findings and unrelated user changes. Do not widen the scope into new sensitive
paths. If the correction cannot fit the patch/time/token/tool budget, stop and
report the remaining work; the runtime requires a human decision.

Do not invoke another correction loop or subagents, commit, push, publish, change
verification policy, modify runtime evidence or claim approval. The runtime
freezes the corrected candidate, executes checks and obtains fresh review.

Write strict iteration-result@1 JSON to {{RESULT_PATH}} with exactly continue,
summary and remaining. Use continue=false and remaining=[] only when your
correction is complete. Otherwise set continue=true with concrete remaining work.
All volatile objective, unit, budget, findings and check receipts follow.
