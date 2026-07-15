package activity

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds exact activity versions. Version selection happens while a
// flow is compiled; execution always uses the pinned ref.
type Registry struct {
	mu    sync.RWMutex
	items map[string]Executor
}

func NewRegistry() *Registry { return &Registry{items: map[string]Executor{}} }

func (r *Registry) Register(executor Executor) error {
	if executor == nil {
		return fmt.Errorf("activity: nil executor")
	}
	spec := executor.Spec()
	if spec.Name == "" || spec.Version == "" {
		return fmt.Errorf("activity: name and version are required")
	}
	switch spec.Effect {
	case EffectPure, EffectIdempotent, EffectReconcilable, EffectAtMostOnce, EffectUnsafe:
	default:
		return fmt.Errorf("activity %s: invalid effect %q", spec.Ref(), spec.Effect)
	}
	if spec.Effect == EffectReconcilable {
		if _, ok := executor.(Reconciler); !ok {
			return fmt.Errorf("activity %s: reconcilable executor has no Reconcile", spec.Ref())
		}
	}
	if spec.Timeout < 0 || spec.TimeoutMS < 0 || spec.Retry.MaxAttempts < 0 || spec.Retry.InitialMS < 0 || spec.Retry.MaxMS < 0 {
		return fmt.Errorf("activity %s: timeout and retry values cannot be negative", spec.Ref())
	}
	for _, class := range spec.Retry.Classes {
		switch class {
		case ErrorTransient, ErrorRateLimited, ErrorTimeout, ErrorInvalidContract, ErrorGateFailed, ErrorConflict, ErrorPermissionDenied, ErrorCancelled, ErrorPermanent, ErrorOutcomeUnknown, ErrorNeedsHuman:
		default:
			return fmt.Errorf("activity %s: invalid retry class %q", spec.Ref(), class)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[spec.Ref()]; exists {
		return fmt.Errorf("activity: duplicate %s", spec.Ref())
	}
	r.items[spec.Ref()] = executor
	return nil
}

func (r *Registry) Resolve(ref string) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.items[ref]
	return executor, ok
}

func (r *Registry) Specs() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.items))
	for _, executor := range r.items {
		out = append(out, executor.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out
}
