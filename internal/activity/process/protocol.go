package process

import (
	"encoding/json"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

type Operation string

const (
	OperationDescribe   Operation = "describe"
	OperationExecute    Operation = "execute"
	OperationReconcile  Operation = "reconcile"
	OperationCompensate Operation = "compensate"
)

type requestEnvelope struct {
	Protocol   string                      `json:"protocol"`
	Operation  Operation                   `json:"operation"`
	Request    *activity.Request           `json:"request,omitempty"`
	Reconcile  *activity.ReconcileRequest  `json:"reconcile,omitempty"`
	Compensate *activity.CompensateRequest `json:"compensate,omitempty"`
}

type responseEnvelope struct {
	Protocol  string                    `json:"protocol"`
	OK        bool                      `json:"ok"`
	Result    *activity.Result          `json:"result,omitempty"`
	Reconcile *activity.ReconcileResult `json:"reconcile,omitempty"`
	Error     *responseError            `json:"error,omitempty"`
	Spec      *activity.Spec            `json:"spec,omitempty"`
}

type responseError struct {
	Class   activity.ErrorClass `json:"class"`
	Message string              `json:"message"`
}

func encodeRequest(envelope requestEnvelope) ([]byte, error) { return json.Marshal(envelope) }
