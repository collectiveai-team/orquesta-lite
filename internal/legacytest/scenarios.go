// Package legacytest names the behavioral scenarios that the durable workflow
// runtime must preserve before the specialized scheduler can be deleted.
package legacytest

// Scenario identifies one externally observable runtime invariant.
type Scenario struct {
	Name           string
	Description    string
	LegacyExpected string
	V2Expected     string
}

// Scenarios is the canonical parity inventory shared by legacy and v2 tests.
var Scenarios = []Scenario{
	{"invalid_contract_fallback", "invalid agent output falls through to the next configured agent", "next agent is tried", "next agent is tried; exhaustion is invalid_contract"},
	{"bounded_retry", "a repair loop cannot exceed its configured attempt budget", "loop stops at configured limit", "workflow global and step retry budgets stop scheduling"},
	{"quality_gate_failure", "lint, test, and review failures prevent success", "task remains failed", "gate_failed prevents step and run success"},
	{"full_suite_gate", "the full suite passes before an increment is accepted", "commit is rejected", "development pack gate rejects the increment"},
	{"rollback_on_failure", "failed work is not committed as accepted work", "worktree is restored", "declared compensation restores confirmed effects"},
	{"decomposition_limit", "recursive task decomposition has a hard depth limit", "handoff after depth limit", "bounded while/policy hands off after limit"},
	{"needs_human", "unrecoverable work becomes explicit human attention", "handoff file is written", "run and approval become needs_human"},
	{"review_stop", "review completion stops the outer loop", "approved review ends loop", "while condition ends the versioned subflow"},
	{"resume_reuses_plan", "resume continues the persisted decomposition", "factory.json plan is reused", "stored IR and completed StepRuns are reused"},
	{"repeated_failure", "the same failure cannot loop forever", "factory stops", "retry/attempt budget stops"},
	{"residue_checkpoint", "interrupted changes are preserved before switching branches", "checkpoint commit exists", "development pack activity records checkpoint artifact"},
	{"merge_conflict", "a merge conflict pauses instead of claiming completion", "feature needs human", "conflict error policy requests approval/human"},
	{"budget_exhausted", "cost or attempt exhaustion stops scheduling new work", "queue stops", "duration/attempt/agent/cost budget stops"},
	{"interrupt_resume", "completed work is not repeated after restart", "completed tasks are skipped", "pinned successful StepRuns are skipped"},
	{"publish_uncertain", "an uncertain external publish is reconciled before retry", "operator resolves publish", "reconciler runs before any execute retry"},
}

// ByName returns a scenario by its stable identifier.
func ByName(name string) (Scenario, bool) {
	for _, scenario := range Scenarios {
		if scenario.Name == name {
			return scenario, true
		}
	}
	return Scenario{}, false
}
