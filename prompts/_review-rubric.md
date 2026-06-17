# Thermo-Nuclear Code Quality Review Rubric

Upstream: https://github.com/cursor/plugins/blob/main/cursor-team-kit/skills/thermo-nuclear-code-quality-review/SKILL.md

This rubric is derived from the upstream skill; re-sync if it changes.

Use this as a strict maintainability bar. Do not approve merely because the code works. Prefer actionable findings that preserve behavior while making the implementation simpler, smaller, more direct, and easier to reason about.

## Core Standards

- Be ambitious about structural simplification. Look for code-judo moves that make whole branches, helpers, modes, conditionals, or layers disappear.
- Treat a file crossing roughly 1000 lines as a strong smell. Ask for decomposition unless there is a clear structural reason to keep it together.
- Do not allow spaghetti growth: ad-hoc conditionals, scattered special cases, one-off flags, or edge-case branches inserted into unrelated flows.
- Keep logic in the canonical layer. Reuse existing helpers and move behavior to the package, module, or service that owns the concept.
- Prefer direct, boring, maintainable code over hacky, brittle, magical, or overly generic mechanisms.
- Push on type-contract clarity. Question unnecessary optionality, casts, `any`, `unknown`, or loosely shaped data when an explicit boundary would simplify the flow.
- Treat unnecessary sequential orchestration and non-atomic updates as design smells when a cleaner structure is obvious.

## Primary Questions

- Is there a code-judo move that would make this dramatically simpler?
- Can the change be reframed so fewer concepts, branches, or helper layers are needed?
- Does this improve or worsen the local architecture?
- Did the diff add branching complexity where a better abstraction should exist?
- Did a previously cohesive module become more coupled, more stateful, or harder to scan?
- Is this logic living in the right file and layer?
- Did this change enlarge a file or component past a healthy size boundary?
- Are repeated conditionals signaling a missing model or helper?
- Is the implementation direct and legible, or does it rely on special cases and incidental control flow?
- Is each abstraction earning its keep, or is it just a wrapper?
- Did the diff introduce casts, optionality, or ad-hoc object shapes that obscure the real invariant?
- Is independent work serialized for no reason, or can orchestration be simpler and more atomic?

## Findings To Escalate

- A complicated implementation where a cleaner reframing could delete whole categories of complexity.
- Refactors that move code around without reducing the concepts a reader must hold in mind.
- Files pushed past roughly 1000 lines by the change.
- New conditionals bolted onto unrelated or already busy code paths.
- One-off booleans, nullable modes, fallback branches, or feature checks that complicate existing control flow.
- Feature-specific logic leaking into general-purpose modules.
- Generic magic that hides simple structure and makes the code harder to reason about.
- Thin wrappers or identity abstractions that add indirection without clarity.
- Unnecessary casts, optional params, or loose object shapes that muddy the real contract.
- Copy-pasted logic instead of a canonical helper.
- Bespoke helpers where the codebase already has a clear utility for the job.
- Logic added in the wrong layer or package when there is a clear canonical home.
- Unfinished behavior shipped as a stub (`raise NotImplementedError`, `panic("not implemented")`, `TODO`-bodied functions, hard-coded fake results) that a prior task should have implemented — especially a stub reachable from a shipped code path, where calling it raises at runtime.

## Preferred Remedies

- Delete a layer of indirection rather than polishing it.
- Reframe the state model so conditionals disappear instead of merely centralizing them.
- Change ownership boundaries so the feature becomes a natural extension of an existing abstraction.
- Turn special-case logic into a simpler default flow with fewer exceptions.
- Extract a focused helper or pure function.
- Split large files into smaller modules with clear ownership.
- Move feature-specific behavior behind a dedicated abstraction.
- Replace condition chains with a typed model or explicit dispatcher.
- Separate orchestration from business logic.
- Collapse duplicate branches into one clearer flow.
- Delete wrappers that do not clarify the API.
- Reuse canonical helpers instead of introducing near-duplicates.
- Make type boundaries explicit so control flow gets simpler.
- Parallelize independent work when it simplifies orchestration.
- Make related updates atomic when partial state would be harder to reason about.

## Approval Blockers

Block approval unless the author can clearly justify the design when:

- The change preserves incidental complexity even though a plausible code-judo move would delete it.
- The change pushes a file from under roughly 1000 lines to over roughly 1000 lines.
- The change adds ad-hoc branching that makes an existing flow more tangled.
- The change solves a local problem by scattering feature checks across shared code.
- The change adds unnecessary abstraction, wrapper, cast-heavy code, or optionality that makes the design more indirect.
- The change duplicates an existing helper or puts logic in the wrong layer when there is a clear canonical home.
