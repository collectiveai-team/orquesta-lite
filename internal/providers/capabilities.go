package providers

import "fmt"

type Capabilities struct {
	ReadOnlySandbox            bool `json:"read_only_sandbox"`
	NativeSubagentModelControl bool `json:"native_subagent_model_control"`
	FullPromptMeasurement      bool `json:"full_prompt_measurement"`
	LiveToolInterception       bool `json:"live_tool_interception"`
}

// CapabilitiesFor describes guarantees implemented by our adapter, not every
// feature a particular installed CLI version may have.
func CapabilitiesFor(provider string) Capabilities {
	return Capabilities{ReadOnlySandbox: provider == "codex", LiveToolInterception: IsKnown(provider)}
}

func RequireNativeModelControl(provider string) error {
	if !CapabilitiesFor(provider).NativeSubagentModelControl {
		return fmt.Errorf("provider %q: native subagent model control is unavailable through this adapter", provider)
	}
	return nil
}
