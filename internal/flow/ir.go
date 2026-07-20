package flow

import (
	"encoding/json"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

type IR struct {
	APIVersion string                     `json:"apiVersion"`
	Kind       Kind                       `json:"kind"`
	Metadata   Metadata                   `json:"metadata"`
	Inputs     map[string]InputSpec       `json:"inputs,omitempty"`
	Steps      []IRStep                   `json:"steps"`
	Outputs    map[string]Value           `json:"outputs,omitempty"`
	Resources  map[string]Digest          `json:"resources"`
	Schemas    map[string]*Schema         `json:"schemas,omitempty"`
	Policies   map[string]json.RawMessage `json:"policies,omitempty"`
	Pack       *PackSnapshot              `json:"pack,omitempty"`
	Digest     Digest                     `json:"-"`
}

type IRStep struct {
	ID         string           `json:"id"`
	Uses       ResourceRef      `json:"uses"`
	With       map[string]Value `json:"with,omitempty"`
	If         string           `json:"if,omitempty"`
	Foreach    *ForeachSpec     `json:"foreach,omitempty"`
	While      *WhileSpec       `json:"while,omitempty"`
	Retry      *ResourceRef     `json:"retry,omitempty"`
	Activity   *activity.Spec   `json:"activity,omitempty"`
	Subflow    *IR              `json:"subflow,omitempty"`
	OnError    *IRHandler       `json:"onError,omitempty"`
	OnCancel   *IRHandler       `json:"onCancel,omitempty"`
	Compensate *IRHandler       `json:"compensate,omitempty"`
}

type IRHandler struct {
	Uses     ResourceRef      `json:"uses"`
	With     map[string]Value `json:"with,omitempty"`
	Activity *activity.Spec   `json:"activity,omitempty"`
	Subflow  *IR              `json:"subflow,omitempty"`
}
