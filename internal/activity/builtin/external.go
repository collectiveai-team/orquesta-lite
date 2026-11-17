package builtin

import (
	"context"
	"fmt"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

// ExternalExecutor advertises the asynchronous contract. The durable scheduler
// owns its execution so an adapter never holds an activity invocation open.
type ExternalExecutor struct{}

func (ExternalExecutor) Spec() activity.Spec {
	return activity.Spec{Name: "external.dispatch", Version: "1", Effect: activity.EffectAtMostOnce}
}

func (ExternalExecutor) Execute(context.Context, activity.Request) (activity.Result, error) {
	return activity.Result{}, fmt.Errorf("external.dispatch requires the durable workflow runtime")
}
