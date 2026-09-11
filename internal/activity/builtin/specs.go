package builtin

import "github.com/collectiveai-team/orquesta-lite/internal/activity"

// Specs is the canonical list of activities a compiler must know about.
//
// It exists as one function because it used to exist as two: the CLI and the
// web dashboard each built their own copy, and adding an activity to one left
// the other silently unable to compile any flow that used it — the flow simply
// vanished from the listing.
func Specs() []activity.Spec {
	return []activity.Spec{
		(&AgentExecutor{}).Spec(),
		(&CommandExecutor{}).Spec(),
		(&GateExecutor{}).Spec(),
		GateAssertExecutor{}.Spec(),
		ReviewAggregateExecutor{}.Spec(),
		(&ArtifactExecutor{}).Spec(),
		ApprovalExecutor{}.Spec(),
		(&GitCommitExecutor{}).Spec(),
	}
}
