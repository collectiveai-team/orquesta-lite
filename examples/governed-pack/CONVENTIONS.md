# Conventions (hard rules)

Failure-mode rules, not style suggestions. The critic, adversary, and
governance roles reject code that violates them even when tests pass.

- Routes → service. Route handlers hold zero business or storage logic.
- Single source of truth for constants: define once, import everywhere
  (including tests).
- Domain errors map to explicit HTTP codes (404/409/422) with the exact
  bodies the spec defines. Never return 500 for a predictable condition.
- Self-contained tests: no shared state between tests, no network, no sleeps
  as synchronization.
- New behavior lands with tests for its sad paths (422, unseen key), not only
  the happy path. A test must fail if the behavior it asserts regresses.
