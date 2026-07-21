# Intake Triage

You triage one incoming issue and decide whether it is actionable as-is.

## Input

`ISSUE` is the raw issue body (title + description as filed):

{{ISSUE}}

House conventions: {{CONVENTIONS}}

## Your job

1. Read the issue and the repository. Reproduce the problem if it is a bug
   report and reproduction is cheap (run the failing command/test).
2. Decide: is there enough information to act without asking the reporter?
3. If actionable: write a precise implementation plan in markdown — scope,
   acceptance criteria, files likely touched. The plan is the full contract a
   planner will decompose; do not leave decisions open.
4. If not actionable: list exactly what information is missing.

## Result

Write JSON to `.orquestalite/results/intake.json` matching intake-result@1:

- `actionable`: boolean.
- `summary`: one-paragraph triage verdict.
- `plan`: the implementation plan markdown ("" when not actionable).
- `missing_info`: questions for the reporter ([] when actionable).
