package process

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

const ProtocolV1 = "orq.activity/v1"

type Manifest struct {
	Protocol string        `json:"protocol"`
	Command  []string      `json:"command"`
	Spec     activity.Spec `json:"spec"`
}

func DecodeManifest(raw []byte) (*Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("activity manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("activity manifest: trailing JSON")
	}
	if manifest.Protocol != ProtocolV1 || len(manifest.Command) == 0 || manifest.Spec.Name == "" || manifest.Spec.Version == "" {
		return nil, fmt.Errorf("activity manifest: protocol, command, spec name and version are required")
	}
	if manifest.Spec.Timeout < 0 || manifest.Spec.TimeoutMS < 0 || manifest.Spec.Retry.MaxAttempts < 0 || manifest.Spec.Retry.InitialMS < 0 || manifest.Spec.Retry.MaxMS < 0 {
		return nil, fmt.Errorf("activity manifest: timeout and retry values cannot be negative")
	}
	switch manifest.Spec.Effect {
	case activity.EffectPure, activity.EffectIdempotent, activity.EffectReconcilable, activity.EffectAtMostOnce, activity.EffectUnsafe:
	default:
		return nil, fmt.Errorf("activity manifest: invalid effect mode %q", manifest.Spec.Effect)
	}
	for _, arg := range manifest.Command {
		if arg == "" {
			return nil, fmt.Errorf("activity manifest: command arguments cannot be empty")
		}
	}
	return &manifest, nil
}
