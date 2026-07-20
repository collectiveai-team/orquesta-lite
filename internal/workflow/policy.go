package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

type RetryRule struct {
	MaxAttempts int `json:"maxAttempts"`
	DelayMS     int `json:"delayMs,omitempty"`
}

type Policy struct {
	Retries            map[activity.ErrorClass]RetryRule `json:"retries,omitempty"`
	MaxDurationSeconds int                               `json:"maxDurationSeconds,omitempty"`
	MaxAttempts        int                               `json:"maxAttempts,omitempty"`
	MaxAgentAttempts   int                               `json:"maxAgentAttempts,omitempty"`
	MaxCostUSD         float64                           `json:"maxCostUSD,omitempty"`
	AllowShell         bool                              `json:"allowShell,omitempty"`
	ApproveUnsafe      bool                              `json:"approveUnsafe,omitempty"`
	ApprovalEffects    []activity.EffectMode             `json:"approvalEffects,omitempty"`
	ApprovalActivities []string                          `json:"approvalActivities,omitempty"`
	MaxParallelism     int                               `json:"maxParallelism,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{Retries: map[activity.ErrorClass]RetryRule{
		activity.ErrorTransient:   {MaxAttempts: 3, DelayMS: 250},
		activity.ErrorRateLimited: {MaxAttempts: 3, DelayMS: 1000},
		activity.ErrorTimeout:     {MaxAttempts: 2, DelayMS: 250},
	}, MaxAttempts: 32}
}

func DecodePolicy(raw []byte) (Policy, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return DefaultPolicy(), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("policy: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Policy{}, fmt.Errorf("policy: trailing JSON")
	}
	if policy.MaxAttempts < 0 || policy.MaxAgentAttempts < 0 || policy.MaxCostUSD < 0 || policy.MaxDurationSeconds < 0 || policy.MaxParallelism < 0 {
		return Policy{}, fmt.Errorf("policy: budgets cannot be negative")
	}
	for class, rule := range policy.Retries {
		if !validErrorClass(class) {
			return Policy{}, fmt.Errorf("policy: invalid retry error class %q", class)
		}
		if rule.MaxAttempts < 1 || rule.DelayMS < 0 {
			return Policy{}, fmt.Errorf("policy: invalid retry rule for %s", class)
		}
	}
	for _, effect := range policy.ApprovalEffects {
		if !validEffectMode(effect) {
			return Policy{}, fmt.Errorf("policy: invalid approval effect %q", effect)
		}
	}
	return policy, nil
}

func validErrorClass(class activity.ErrorClass) bool {
	switch class {
	case activity.ErrorTransient, activity.ErrorRateLimited, activity.ErrorTimeout, activity.ErrorInvalidContract, activity.ErrorGateFailed, activity.ErrorConflict, activity.ErrorPermissionDenied, activity.ErrorCancelled, activity.ErrorPermanent, activity.ErrorOutcomeUnknown, activity.ErrorNeedsHuman:
		return true
	default:
		return false
	}
}

func validEffectMode(effect activity.EffectMode) bool {
	switch effect {
	case activity.EffectPure, activity.EffectIdempotent, activity.EffectReconcilable, activity.EffectAtMostOnce, activity.EffectUnsafe:
		return true
	default:
		return false
	}
}

func (p Policy) requiresApproval(spec activity.Spec) bool {
	if spec.Effect == activity.EffectUnsafe && !p.ApproveUnsafe {
		return true
	}
	for _, effect := range p.ApprovalEffects {
		if effect == spec.Effect {
			return true
		}
	}
	for _, ref := range p.ApprovalActivities {
		if ref == spec.Ref() || ref == spec.Name {
			return true
		}
	}
	return false
}

// restrictWith lets a step policy reduce retry counts and delays. It cannot
// authorize a class that the run policy disallowed or expand a run budget.
func (p Policy) restrictWith(step Policy) Policy {
	if len(step.Retries) == 0 {
		return p
	}
	restricted := p
	restricted.Retries = make(map[activity.ErrorClass]RetryRule)
	for class, requested := range step.Retries {
		allowed, ok := p.Retries[class]
		if !ok {
			continue
		}
		if requested.MaxAttempts < allowed.MaxAttempts {
			allowed.MaxAttempts = requested.MaxAttempts
		}
		if requested.DelayMS > allowed.DelayMS {
			allowed.DelayMS = requested.DelayMS
		}
		restricted.Retries[class] = allowed
	}
	return restricted
}

func (p Policy) restrictWithActivity(spec activity.Spec) Policy {
	if spec.Retry.MaxAttempts <= 0 && len(spec.Retry.Classes) == 0 && spec.Retry.InitialMS <= 0 {
		return p
	}
	restricted := p
	restricted.Retries = make(map[activity.ErrorClass]RetryRule)
	allowedClasses := make(map[activity.ErrorClass]bool, len(spec.Retry.Classes))
	for _, class := range spec.Retry.Classes {
		allowedClasses[class] = true
	}
	for class, rule := range p.Retries {
		if len(allowedClasses) > 0 && !allowedClasses[class] {
			continue
		}
		if spec.Retry.MaxAttempts > 0 && spec.Retry.MaxAttempts < rule.MaxAttempts {
			rule.MaxAttempts = spec.Retry.MaxAttempts
		}
		if spec.Retry.InitialMS > rule.DelayMS {
			rule.DelayMS = spec.Retry.InitialMS
		}
		restricted.Retries[class] = rule
	}
	return restricted
}

func (p Policy) retryRule(class activity.ErrorClass) (RetryRule, bool) {
	rule, ok := p.Retries[class]
	return rule, ok
}
func (p Policy) deadline(start time.Time) (time.Time, bool) {
	if p.MaxDurationSeconds <= 0 {
		return time.Time{}, false
	}
	return start.Add(time.Duration(p.MaxDurationSeconds) * time.Second), true
}
