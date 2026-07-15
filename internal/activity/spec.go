// Package activity defines bounded, typed effects executed by the workflow
// runtime. Activities never schedule workflow steps or own orchestration loops.
package activity

import "time"

// EffectMode describes what the runtime may safely do after an interrupted
// attempt whose external outcome is not yet known.
type EffectMode string

const (
	EffectPure         EffectMode = "pure"
	EffectIdempotent   EffectMode = "idempotent"
	EffectReconcilable EffectMode = "reconcilable"
	EffectAtMostOnce   EffectMode = "at_most_once"
	EffectUnsafe       EffectMode = "unsafe"
)

// RetryPolicy is the activity's default. A workflow policy may make it more
// restrictive, but never expand it without explicit authorization.
type RetryPolicy struct {
	MaxAttempts int           `json:"maxAttempts,omitempty"`
	Initial     time.Duration `json:"-"`
	Max         time.Duration `json:"-"`
	InitialMS   int           `json:"initialMs,omitempty"`
	MaxMS       int           `json:"maxMs,omitempty"`
	Classes     []ErrorClass  `json:"classes,omitempty"`
}

// Spec is the stable contract advertised by one activity version.
type Spec struct {
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	InputSchema  string        `json:"inputSchema"`
	OutputSchema string        `json:"outputSchema"`
	Effect       EffectMode    `json:"effect"`
	Timeout      time.Duration `json:"-"`
	TimeoutMS    int           `json:"timeoutMs,omitempty"`
	Retry        RetryPolicy   `json:"retry,omitempty"`
}

// Ref returns the canonical registry key.
func (s Spec) Ref() string { return s.Name + "@" + s.Version }

func (s Spec) EffectiveTimeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return time.Duration(s.TimeoutMS) * time.Millisecond
}
