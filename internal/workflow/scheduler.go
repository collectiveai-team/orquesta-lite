package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/runid"
)

var ErrNeedsHuman = errors.New("workflow needs human input")

const maxWorkflowValueBytes = 8 << 20

// whileIterationCeiling is the runtime's hard backstop for a durable loop. A
// `while` bound may now be data-derived and can extend itself between passes,
// so a flow whose data says "keep going" forever must still stop. This is not
// redundant with a pack's own schema bound: a schema protects one pack, this
// protects the runtime from every flow.
const whileIterationCeiling = 1000

// whileOutputCapacityHint pre-sizes the aggregate slice. The bound is no longer
// known as a plain int before the loop starts, and over-allocating from an
// attacker- or agent-supplied number would be worse than growing the slice.
const whileOutputCapacityHint = 8

type Runtime struct {
	Store      *Store
	Activities *activity.Registry
	Catalog    flow.Catalog
	// Config is the read-only project configuration exposed to flows through
	// the `config.<key>` reference namespace. The host populates it from an
	// explicit whitelist; the runtime never writes to it.
	Config map[string]any
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
	Jitter func(time.Duration, int, string) time.Duration
}

type StartOptions struct {
	RunID, SourceKey, FlowRef string
	Inputs                    map[string]any
	Policy                    Policy
}

func (r *Runtime) Start(ctx context.Context, ir *flow.IR, options StartOptions) (*Run, error) {
	if r.Store == nil || r.Activities == nil || r.Catalog == nil {
		return nil, fmt.Errorf("workflow runtime is not configured")
	}
	if options.RunID == "" {
		options.RunID = runid.New()
	}
	if options.FlowRef == "" {
		options.FlowRef = "flow:" + ir.Metadata.Name + "@" + ir.Metadata.Version
	}
	inputsRaw, err := json.Marshal(options.Inputs)
	if err != nil {
		return nil, err
	}
	if err = validateInputs(ir.Inputs, options.Inputs, ir.Schemas); err != nil {
		return nil, err
	}
	irRaw, err := json.Marshal(ir)
	if err != nil {
		return nil, err
	}
	policyRaw, err := json.Marshal(options.Policy)
	if err != nil {
		return nil, err
	}
	run, created, err := r.Store.CreateRunOnce(ctx, CreateRunParams{ID: options.RunID, SourceKey: options.SourceKey, FlowRef: options.FlowRef, DefinitionHash: string(ir.Digest), IR: irRaw, Inputs: inputsRaw, Policy: policyRaw})
	if err != nil {
		return nil, err
	}
	if !created {
		return run, nil
	}
	if err = r.executeRun(ctx, run, ir, options.Inputs, options.Policy); err != nil {
		latest, loadErr := r.Store.GetRun(ctx, run.ID)
		if loadErr != nil {
			return nil, errors.Join(err, loadErr)
		}
		return latest, err
	}
	return r.Store.GetRun(ctx, run.ID)
}

func (r *Runtime) Resume(ctx context.Context, runID string) (*Run, error) {
	run, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status == RunSucceeded || run.Status == RunCancelled {
		return run, nil
	}
	var ir flow.IR
	if err = json.Unmarshal(run.IR, &ir); err != nil {
		return nil, fmt.Errorf("workflow: stored IR: %w", err)
	}
	inputs, err := decodeObject(run.Inputs)
	if err != nil {
		return nil, err
	}
	policy, err := DecodePolicy(run.Policy)
	if err != nil {
		return nil, err
	}
	if err = r.Store.SetRunStatus(ctx, runID, RunRunning, ""); err != nil {
		return nil, err
	}
	if err = r.executeRun(ctx, run, &ir, inputs, policy); err != nil {
		latest, loadErr := r.Store.GetRun(ctx, runID)
		if loadErr != nil {
			return nil, errors.Join(err, loadErr)
		}
		return latest, err
	}
	return r.Store.GetRun(ctx, runID)
}

func (r *Runtime) executeRun(ctx context.Context, run *Run, ir *flow.IR, inputs map[string]any, policy Policy) error {
	state := newExecutionState(r, run, policy, inputs, ir.Schemas, ir.Policies)
	_, err := state.executeIR(ctx, ir)
	if errors.Is(err, ErrNeedsHuman) {
		return err
	}
	if err != nil {
		status := RunFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = RunCancelled
		}
		if latest, loadErr := r.Store.GetRun(context.Background(), run.ID); loadErr == nil && latest.Status == RunCancelled {
			return err
		}
		_ = r.Store.SetRunStatus(context.Background(), run.ID, status, err.Error())
		return err
	}
	return r.Store.SetRunStatus(ctx, run.ID, RunSucceeded, "")
}

type executionState struct {
	runtime  *Runtime
	run      *Run
	policy   Policy
	inputs   map[string]any
	steps    map[string]any
	scope    string
	item     any
	index    int
	hasItem  bool
	schemas  map[string]*flow.Schema
	policies map[string]json.RawMessage
	root     bool
	journal  *compensationJournal
}

type completedEffect struct {
	state      executionState
	step       flow.IRStep
	stored     StepRun
	foreachKey string
}

type compensationJournal struct {
	mu      sync.Mutex
	seen    map[int64]bool
	effects []completedEffect
}

type handlerHandledError struct{ err error }

func (e *handlerHandledError) Error() string { return e.err.Error() }
func (e *handlerHandledError) Unwrap() error { return e.err }

func newExecutionState(runtime *Runtime, run *Run, policy Policy, inputs map[string]any, schemas map[string]*flow.Schema, policies map[string]json.RawMessage) *executionState {
	return &executionState{runtime: runtime, run: run, policy: policy, inputs: inputs, steps: map[string]any{}, scope: "root", schemas: schemas, policies: policies, root: true, journal: &compensationJournal{seen: map[int64]bool{}}}
}

func (s *executionState) executeIR(ctx context.Context, ir *flow.IR) (map[string]any, error) {
	for _, step := range ir.Steps {
		if err := s.checkBudget(ctx); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if step.If != "" {
			passed, err := flow.EvalBool(step.If, s.resolve)
			if err != nil {
				return nil, fmt.Errorf("step %s if: %w", step.ID, err)
			}
			if !passed {
				stored, err := s.runtime.Store.EnsureStep(ctx, s.run.ID, s.scope, step.ID, "", step.Uses.String(), nil)
				if err != nil {
					return nil, err
				}
				if stored.Status != StepSkipped && stored.Status != StepSucceeded {
					if err = s.runtime.Store.SkipStep(ctx, stored); err != nil {
						return nil, err
					}
				}
				s.steps[step.ID] = nil
				continue
			}
		}
		var output any
		var err error
		if step.Foreach != nil {
			output, err = s.executeForeach(ctx, step)
		} else if step.While != nil {
			output, err = s.executeWhile(ctx, step)
		} else {
			output, err = s.executeInstance(ctx, step, "")
		}
		if err != nil {
			original := err
			var handled *handlerHandledError
			if errors.As(err, &handled) {
				err = nil
			} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				err = s.runHandler(context.WithoutCancel(ctx), step, "on_cancel", step.OnCancel, "")
			} else if !errors.Is(err, ErrNeedsHuman) {
				err = s.runHandler(context.WithoutCancel(ctx), step, "on_error", step.OnError, "")
			} else {
				err = nil
			}
			if err != nil {
				original = errors.Join(original, fmt.Errorf("step %s handler: %w", step.ID, err))
			}
			if s.root && !errors.Is(original, ErrNeedsHuman) {
				if compensationErr := s.compensate(context.WithoutCancel(ctx)); compensationErr != nil {
					original = errors.Join(original, compensationErr)
				}
			}
			return nil, original
		}
		s.steps[step.ID] = output
	}
	outputs := map[string]any{}
	for name, value := range ir.Outputs {
		resolved, err := flow.ResolveValue(value, s.resolve)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", name, err)
		}
		outputs[name] = resolved
	}
	return outputs, nil
}

func (s *executionState) executeForeach(ctx context.Context, step flow.IRStep) (any, error) {
	itemsValue, err := flow.ResolveValue(step.Foreach.Items, s.resolve)
	if err != nil {
		return nil, err
	}
	items, ok := itemsValue.([]any)
	if !ok {
		return nil, fmt.Errorf("step %s foreach expected array, got %T", step.ID, itemsValue)
	}
	outputs := make([]any, len(items))
	concurrency := step.Foreach.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if s.policy.MaxParallelism > 0 && concurrency > s.policy.MaxParallelism {
		concurrency = s.policy.MaxParallelism
	}
	if concurrency > len(items) {
		concurrency = len(items)
	}
	if concurrency < 1 {
		concurrency = 1
	}
	isolationKeys := map[string]bool{}
	for index, item := range items {
		if step.Foreach.IsolationKey == nil {
			continue
		}
		child := *s
		child.item = item
		child.index = index
		child.hasItem = true
		resolved, resolveErr := flow.ResolveValue(*step.Foreach.IsolationKey, child.resolve)
		if resolveErr != nil {
			return nil, resolveErr
		}
		key := fmt.Sprint(resolved)
		if key == "" || isolationKeys[key] {
			return nil, fmt.Errorf("step %s has empty or duplicate isolation key %q", step.ID, key)
		}
		isolationKeys[key] = true
	}
	type itemResult struct {
		index  int
		output any
		err    error
	}
	jobs := make(chan int)
	results := make(chan itemResult, len(items))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				item := items[index]
				key := foreachKey(index, item)
				child := *s
				child.item = item
				child.index = index
				child.hasItem = true
				output, executeErr := child.executeInstance(workerCtx, step, key)
				if executeErr != nil && !errors.Is(executeErr, ErrNeedsHuman) {
					handler := step.OnError
					kind := "on_error"
					if errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded) {
						handler, kind = step.OnCancel, "on_cancel"
					}
					if handlerErr := child.runHandler(context.WithoutCancel(workerCtx), step, kind, handler, key); handlerErr != nil {
						executeErr = errors.Join(executeErr, handlerErr)
					}
					executeErr = &handlerHandledError{err: executeErr}
				}
				results <- itemResult{index: index, output: output, err: executeErr}
				if executeErr != nil {
					cancel()
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range items {
			select {
			case jobs <- index:
			case <-workerCtx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	for result := range results {
		if result.err != nil {
			return nil, result.err
		}
		outputs[result.index] = result.output
	}
	raw, err := marshalAggregate(step.ID, outputs)
	if err != nil {
		return nil, err
	}
	aggregate, err := s.runtime.Store.EnsureStep(ctx, s.run.ID, s.scope, step.ID, "", step.Uses.String(), raw)
	if err != nil {
		return nil, err
	}
	if aggregate.Status != StepSucceeded {
		if err = s.runtime.Store.CompleteVirtualStep(ctx, aggregate, raw); err != nil {
			return nil, err
		}
	}
	return outputs, nil
}

func (s *executionState) executeWhile(ctx context.Context, step flow.IRStep) (any, error) {
	current, err := flow.ResolveValue(step.While.Initial, s.resolve)
	if err != nil {
		return nil, err
	}
	outputs := make([]any, 0, whileOutputCapacityHint)
	// granted is the high-water mark of every bound this loop has resolved.
	// The bound is re-read each pass so a replan can *raise* it; letting it
	// fall is what turns a plan-derived budget into a backlog truncator.
	//
	// A planner naturally computes "work still to do", which shrinks with every
	// ticket it finishes, while `index` counts passes and rises. Those two
	// curves cross well before the backlog is done — 24 tickets at one per pass
	// with a 25% margin stop at pass 14 with 10 tickets still pending — and the
	// loop exits normally, so the run reports success over half-built work.
	// That is the exact ~6-tickets-per-run truncation this feature was written
	// to eliminate. The prompt now specifies a cumulative total, but a prompt
	// is guidance and this is arithmetic: a bound that never falls cannot
	// truncate work the loop was already promised, no matter what the planner
	// emits. Runaway is still capped by whileIterationCeiling and by the
	// condition itself.
	granted := 0
	for index := 0; ; index++ {
		child := *s
		child.item = current
		child.index = index
		child.hasItem = true
		condition, evalErr := flow.EvalBool(step.While.Condition, child.resolve)
		if evalErr != nil {
			return nil, fmt.Errorf("step %s while: %w", step.ID, evalErr)
		}
		if !condition {
			break
		}
		// The bound is resolved inside the loop, against the resolver that
		// already carries `item` for this pass. On iteration 0 `item` is the
		// resolved `initial` value, so a data-derived bound works uniformly
		// from the first pass — and a mid-run replan that raises the budget
		// actually extends the loop instead of dying at a stale number.
		//
		// It is resolved *after* the condition on purpose: a loop that is
		// already finishing must not be failed by a bound that its final
		// carried value no longer carries.
		bound, boundErr := child.whileBound(step)
		if boundErr != nil {
			return nil, boundErr
		}
		if bound > granted {
			granted = bound
		}
		if index >= granted {
			break
		}
		current, err = child.executeInstance(ctx, step, fmt.Sprintf("while-%06d", index))
		if err != nil {
			if !errors.Is(err, ErrNeedsHuman) {
				handler := step.OnError
				kind := "on_error"
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					handler, kind = step.OnCancel, "on_cancel"
				}
				if handlerErr := child.runHandler(context.WithoutCancel(ctx), step, kind, handler, fmt.Sprintf("while-%06d", index)); handlerErr != nil {
					err = errors.Join(err, handlerErr)
				}
				return nil, &handlerHandledError{err: err}
			}
			return nil, err
		}
		outputs = append(outputs, current)
	}
	raw, err := marshalAggregate(step.ID, outputs)
	if err != nil {
		return nil, err
	}
	aggregate, err := s.runtime.Store.EnsureStep(ctx, s.run.ID, s.scope, step.ID, "", step.Uses.String(), raw)
	if err != nil {
		return nil, err
	}
	if aggregate.Status != StepSucceeded {
		if err = s.runtime.Store.CompleteVirtualStep(ctx, aggregate, raw); err != nil {
			return nil, err
		}
	}
	return outputs, nil
}

// marshalAggregate encodes a loop's or subflow's collected outputs for durable
// storage, surfacing both ways the encoding can go wrong.
//
// The marshal error used to be discarded, and the size was checked only for
// per-step inputs and per-activity outputs — never for the aggregate that
// accumulates one entry per pass. Both matter more now that a plan-derived
// bound admits up to 200 passes rather than 20: a truncated or unwritten
// aggregate is not a cosmetic loss, because `steps.<loop>.output.last...` is
// exactly what the plan-completion gate asserts on. A gate reading a mangled
// aggregate is the silently-wrong verdict the gate exists to prevent.
func marshalAggregate(stepID string, outputs any) ([]byte, error) {
	raw, err := json.Marshal(outputs)
	if err != nil {
		return nil, fmt.Errorf("step %s: encode aggregate: %w", stepID, err)
	}
	if len(raw) > maxWorkflowValueBytes {
		return nil, fmt.Errorf("step %s aggregate is %d bytes, over the %d limit", stepID, len(raw), maxWorkflowValueBytes)
	}
	return raw, nil
}

// whileBound resolves this pass's iteration bound and rejects anything the
// loop cannot safely run: a non-numeric or non-integer value, a value below 1,
// or one above whileIterationCeiling.
func (s *executionState) whileBound(step flow.IRStep) (int, error) {
	resolved, err := flow.ResolveValue(step.While.MaxIterations, s.resolve)
	if err != nil {
		return 0, fmt.Errorf("step %s while maxIterations: %w", step.ID, err)
	}
	bound, ok := integerValue(resolved)
	if !ok {
		return 0, fmt.Errorf("step %s while maxIterations must resolve to an integer, got %v", step.ID, resolved)
	}
	if bound < 1 || bound > whileIterationCeiling {
		return 0, fmt.Errorf("step %s while maxIterations resolved to %d, which is outside 1..%d", step.ID, bound, whileIterationCeiling)
	}
	return bound, nil
}

// integerValue accepts the numeric forms a resolved reference can carry —
// json.Number from durable state, or a plain Go number from a literal — and
// reports false for anything that is not a whole number.
//
// It delegates to flow.WholeNumber so the loop bound and `"type":"integer"`
// schema validation agree on what an integer is. They used to disagree: both
// routed json.Number through Int64(), so a planner emitting the schema-valid
// `"iteration_budget": 8.0` failed the bound check and killed the run. Fixing
// one side alone would only have moved the crash to the other.
func integerValue(value any) (int, bool) {
	number, ok := flow.WholeNumber(value)
	if !ok {
		return 0, false
	}
	return int(number), true
}

func (s *executionState) executeInstance(ctx context.Context, step flow.IRStep, foreachKey string) (any, error) {
	inputs := map[string]any{}
	for name, value := range step.With {
		resolved, err := flow.ResolveValue(value, s.resolve)
		if err != nil {
			return nil, fmt.Errorf("step %s input %s: %w", step.ID, name, err)
		}
		inputs[name] = resolved
	}
	rawInputs, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("step %s inputs: %w", step.ID, err)
	}
	if len(rawInputs) > maxWorkflowValueBytes {
		return nil, fmt.Errorf("step %s resolved inputs exceed %d bytes", step.ID, maxWorkflowValueBytes)
	}
	stored, err := s.runtime.Store.EnsureStep(ctx, s.run.ID, s.scope, step.ID, foreachKey, step.Uses.String(), rawInputs)
	if err != nil {
		return nil, err
	}
	if stored.Status == StepSucceeded {
		output, decodeErr := decodeAny(stored.Output)
		if decodeErr == nil {
			s.recordCompensation(step, stored, foreachKey)
		}
		return output, decodeErr
	}
	if stored.Status == StepSkipped {
		return nil, nil
	}
	if stored.Status == StepFailed {
		return nil, &activity.Error{Class: stored.ErrorClass, Op: step.Uses.String(), Err: fmt.Errorf("persisted terminal failure: %s", stored.Error)}
	}
	if step.Subflow != nil {
		if err = validateInputs(step.Subflow.Inputs, inputs, step.Subflow.Schemas); err != nil {
			return nil, err
		}
		child := &executionState{runtime: s.runtime, run: s.run, policy: s.policy, inputs: inputs, steps: map[string]any{}, scope: s.scope + "/" + step.ID + keySuffix(foreachKey), schemas: step.Subflow.Schemas, policies: step.Subflow.Policies, journal: s.journal}
		outputs, err := child.executeIR(ctx, step.Subflow)
		if err != nil {
			return nil, err
		}
		raw, marshalErr := marshalAggregate(step.ID, outputs)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if err = s.runtime.Store.CompleteVirtualStep(ctx, stored, raw); err != nil {
			return nil, err
		}
		return outputs, nil
	}
	if step.Activity == nil {
		return nil, fmt.Errorf("step %s has no activity or subflow", step.ID)
	}
	output, err := s.executeActivity(ctx, step, stored, rawInputs)
	if err == nil {
		s.recordCompensation(step, stored, foreachKey)
	}
	return output, err
}

func (s *executionState) recordCompensation(step flow.IRStep, stored *StepRun, foreachKey string) {
	if step.Compensate == nil || step.Activity == nil || stored == nil {
		return
	}
	s.journal.mu.Lock()
	defer s.journal.mu.Unlock()
	if s.journal.seen[stored.ID] {
		return
	}
	s.journal.seen[stored.ID] = true
	snapshot := *s
	s.journal.effects = append(s.journal.effects, completedEffect{state: snapshot, step: step, stored: *stored, foreachKey: foreachKey})
}

func (s *executionState) runHandler(ctx context.Context, parent flow.IRStep, kind string, handler *flow.IRHandler, foreachKey string) error {
	if handler == nil {
		return nil
	}
	child := *s
	child.root = false
	child.scope = s.scope + "/handlers/" + parent.ID + keySuffix(foreachKey)
	step := flow.IRStep{ID: kind, Uses: handler.Uses, With: handler.With, Activity: handler.Activity, Subflow: handler.Subflow}
	_, err := child.executeInstance(ctx, step, "")
	return err
}

func (s *executionState) compensate(ctx context.Context) error {
	s.journal.mu.Lock()
	effects := append([]completedEffect(nil), s.journal.effects...)
	s.journal.mu.Unlock()
	var combined error
	for index := len(effects) - 1; index >= 0; index-- {
		effect := effects[index]
		if err := effect.state.runHandler(ctx, effect.step, "compensate", effect.step.Compensate, effect.foreachKey); err != nil {
			combined = errors.Join(combined, fmt.Errorf("compensate %s: %w", effect.step.ID, err))
		}
	}
	return combined
}

func (s *executionState) executeActivity(ctx context.Context, step flow.IRStep, stored *StepRun, rawInputs json.RawMessage) (any, error) {
	executor, ok := s.runtime.Activities.Resolve(step.Activity.Ref())
	if !ok {
		return nil, fmt.Errorf("activity %s is not registered", step.Activity.Ref())
	}
	registered := executor.Spec()
	if registered.Ref() != step.Activity.Ref() || registered.Effect != step.Activity.Effect || registered.InputSchema != step.Activity.InputSchema || registered.OutputSchema != step.Activity.OutputSchema {
		return nil, fmt.Errorf("activity %s runtime contract differs from pinned IR", step.Activity.Ref())
	}
	if err := validateSchema(step.Activity.InputSchema, rawInputs, s.schemas); err != nil {
		return nil, &activity.Error{Class: activity.ErrorInvalidContract, Op: step.Activity.Ref() + " input", Err: err}
	}
	if (step.Activity.Name == "command.run" || step.Activity.Name == "gate.run") && !s.policy.AllowShell {
		var input struct {
			Shell string `json:"shell"`
		}
		if err := json.Unmarshal(rawInputs, &input); err == nil && input.Shell != "" {
			return nil, &activity.Error{Class: activity.ErrorPermissionDenied, Op: step.Activity.Ref(), Err: fmt.Errorf("shell capability is disabled by workflow policy")}
		}
	}
	if s.policy.requiresApproval(*step.Activity) {
		approval, approvalErr := s.runtime.Store.GetApprovalForStep(ctx, stored.ID)
		if approvalErr != nil {
			if !errors.Is(approvalErr, sql.ErrNoRows) {
				return nil, approvalErr
			}
			_, approvalErr = s.runtime.Store.CreateApproval(ctx, stored, "policy requires approval for "+step.Activity.Ref())
			if approvalErr != nil {
				return nil, approvalErr
			}
			return nil, ErrNeedsHuman
		}
		if approval.Status == "pending" {
			return nil, ErrNeedsHuman
		}
		if approval.Status == "rejected" {
			return nil, fmt.Errorf("approval %s rejected", approval.ID)
		}
	}
	if stored.Status == StepRunning || stored.Status == StepOutcomeUnknown {
		recovered, output, err := s.recoverAttempt(ctx, executor, stored)
		if err != nil {
			return nil, err
		}
		if recovered {
			return output, nil
		}
	}
	if step.Activity.Name == "human.wait_for_approval" {
		if approval, err := s.runtime.Store.GetApprovalForStep(ctx, stored.ID); err == nil {
			if approval.Status == "approved" {
				raw, _ := json.Marshal(map[string]string{"decision": "approved"})
				if err = s.runtime.Store.CompleteVirtualStep(ctx, stored, raw); err != nil {
					return nil, err
				}
				return decodeAny(raw)
			}
			if approval.Status == "rejected" {
				return nil, fmt.Errorf("approval %s rejected", approval.ID)
			}
			return nil, ErrNeedsHuman
		}
	}
	for {
		if err := s.checkBudget(ctx); err != nil {
			return nil, err
		}
		stepPolicy, err := s.stepPolicy(step)
		if err != nil {
			return nil, err
		}
		attempt, err := s.runtime.Store.StartAttempt(ctx, stored, step.Activity.Ref(), idempotencyKey(s.run.ID, s.scope, stored.StepID, stored.ForeachKey))
		if err != nil {
			return nil, err
		}
		request := activity.Request{RunID: s.run.ID, StepID: stored.StepID, ScopePath: s.scope, ForeachKey: stored.ForeachKey, Attempt: attempt.Number, IdempotencyKey: attempt.IdempotencyKey, Inputs: rawInputs}
		execCtx := ctx
		cancel := func() {}
		if timeout := step.Activity.EffectiveTimeout(); timeout > 0 {
			execCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		result, execErr := executor.Execute(execCtx, request)
		cancel()
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			execErr = &activity.Error{Class: activity.ErrorTimeout, Op: step.Activity.Ref(), Err: context.DeadlineExceeded}
		}
		if execErr == nil {
			if len(result.Output) > maxWorkflowValueBytes {
				execErr = &activity.Error{Class: activity.ErrorInvalidContract, Op: step.Activity.Ref() + " output", Err: fmt.Errorf("output exceeds %d bytes", maxWorkflowValueBytes)}
			}
		}
		if execErr == nil {
			if err = validateSchema(step.Activity.OutputSchema, result.Output, s.schemas); err != nil {
				execErr = &activity.Error{Class: activity.ErrorInvalidContract, Op: step.Activity.Ref() + " output", Err: err}
			}
		}
		if execErr == nil {
			if err = s.runtime.Store.CompleteAttempt(ctx, stored, attempt, result); err != nil {
				return nil, err
			}
			return decodeAny(result.Output)
		}
		var approvalErr *activity.ApprovalRequiredError
		if errors.As(execErr, &approvalErr) {
			_ = s.runtime.Store.FailAttempt(ctx, stored, attempt, result, activity.ErrorNeedsHuman, execErr, false)
			_, err = s.runtime.Store.CreateApproval(ctx, stored, approvalErr.Reason)
			if err != nil {
				return nil, err
			}
			return nil, ErrNeedsHuman
		}
		class := activity.Classify(execErr)
		mayRetryEffect := step.Activity.Effect == activity.EffectPure || step.Activity.Effect == activity.EffectIdempotent
		if step.Activity.Effect == activity.EffectReconcilable {
			reconciler, ok := executor.(activity.Reconciler)
			if !ok {
				return nil, fmt.Errorf("activity %s has no reconciler", step.Activity.Ref())
			}
			reconciled, reconcileErr := reconciler.Reconcile(ctx, activity.ReconcileRequest{Request: request, Receipt: result.Receipt})
			if reconcileErr != nil {
				if markErr := s.runtime.Store.MarkAttemptUnknown(ctx, stored, attempt); markErr != nil {
					return nil, errors.Join(reconcileErr, markErr)
				}
				if _, createErr := s.runtime.Store.CreateApproval(ctx, stored, "reconcile failed after an uncertain effect: "+step.Activity.Ref()); createErr != nil {
					return nil, errors.Join(reconcileErr, createErr)
				}
				return nil, ErrNeedsHuman
			}
			switch reconciled.Status {
			case activity.ReconcileApplied:
				if reconciled.Result == nil {
					return nil, fmt.Errorf("reconcile applied without result")
				}
				if err = validateSchema(step.Activity.OutputSchema, reconciled.Result.Output, s.schemas); err != nil {
					contractErr := &activity.Error{Class: activity.ErrorInvalidContract, Op: step.Activity.Ref() + " reconciled output", Err: err}
					if persistErr := s.runtime.Store.FailAttempt(ctx, stored, attempt, *reconciled.Result, activity.ErrorInvalidContract, contractErr, true); persistErr != nil {
						return nil, errors.Join(contractErr, persistErr)
					}
					return nil, contractErr
				}
				if err = s.runtime.Store.CompleteAttempt(ctx, stored, attempt, *reconciled.Result); err != nil {
					return nil, err
				}
				return decodeAny(reconciled.Result.Output)
			case activity.ReconcileNotApplied:
				mayRetryEffect = true
			default:
				if err = s.runtime.Store.MarkAttemptUnknown(ctx, stored, attempt); err != nil {
					return nil, err
				}
				if _, err = s.runtime.Store.CreateApproval(ctx, stored, "activity outcome is unknown after reconcile: "+step.Activity.Ref()); err != nil {
					return nil, err
				}
				return nil, ErrNeedsHuman
			}
		}
		rule, retry := stepPolicy.retryRule(class)
		retry = retry && mayRetryEffect
		terminal := !retry || attempt.Number >= rule.MaxAttempts
		if err = s.runtime.Store.FailAttempt(ctx, stored, attempt, result, class, execErr, terminal); err != nil {
			return nil, err
		}
		if terminal {
			return nil, execErr
		}
		delay := s.runtime.retryDelay(time.Duration(rule.DelayMS)*time.Millisecond, attempt.Number, attempt.IdempotencyKey)
		if err = s.runtime.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (s *executionState) stepPolicy(step flow.IRStep) (Policy, error) {
	if step.Retry == nil {
		return s.policy.restrictWithActivity(*step.Activity), nil
	}
	raw, ok := s.policies[step.Retry.String()]
	if !ok {
		return Policy{}, fmt.Errorf("policy %s is not present in pinned IR", step.Retry.String())
	}
	restriction, err := DecodePolicy(raw)
	if err != nil {
		return Policy{}, err
	}
	return s.policy.restrictWith(restriction).restrictWithActivity(*step.Activity), nil
}

func (s *executionState) checkBudget(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if current, err := s.runtime.Store.GetRun(ctx, s.run.ID); err != nil {
		return err
	} else if current.Status == RunCancelled {
		return context.Canceled
	}
	if deadline, ok := s.policy.deadline(s.run.StartedAt); ok && !s.runtime.now().Before(deadline) {
		return fmt.Errorf("workflow duration budget exhausted")
	}
	usage, err := s.runtime.Store.RunUsage(ctx, s.run.ID)
	if err != nil {
		return err
	}
	if s.policy.MaxAttempts > 0 && usage.Attempts >= s.policy.MaxAttempts {
		return fmt.Errorf("workflow attempt budget exhausted")
	}
	if s.policy.MaxAgentAttempts > 0 && usage.AgentAttempts >= s.policy.MaxAgentAttempts {
		return fmt.Errorf("workflow agent attempt budget exhausted")
	}
	if s.policy.MaxCostUSD > 0 && usage.CostUSD >= s.policy.MaxCostUSD {
		return fmt.Errorf("workflow cost budget exhausted: %.6f >= %.6f", usage.CostUSD, s.policy.MaxCostUSD)
	}
	return nil
}

func (s *executionState) recoverAttempt(ctx context.Context, executor activity.Executor, step *StepRun) (bool, any, error) {
	attempt, err := s.runtime.Store.LatestAttempt(ctx, step.ID)
	if err != nil {
		return false, nil, err
	}
	if attempt.Status != AttemptRunning && attempt.Status != AttemptOutcomeUnknown {
		return false, nil, nil
	}
	spec := executor.Spec()
	switch spec.Effect {
	case activity.EffectPure, activity.EffectIdempotent:
		if err = s.runtime.Store.MarkAttemptUnknown(ctx, step, attempt); err != nil {
			return false, nil, err
		}
		if err = s.runtime.Store.ResetStepReady(ctx, step.ID); err != nil {
			return false, nil, err
		}
		return false, nil, nil
	case activity.EffectReconcilable:
		reconciler, ok := executor.(activity.Reconciler)
		if !ok {
			return false, nil, fmt.Errorf("activity %s has no reconciler", spec.Ref())
		}
		request := activity.Request{RunID: s.run.ID, StepID: step.StepID, ScopePath: step.ScopePath, ForeachKey: step.ForeachKey, Attempt: attempt.Number, IdempotencyKey: attempt.IdempotencyKey, Inputs: step.Inputs}
		result, err := reconciler.Reconcile(ctx, activity.ReconcileRequest{Request: request, Receipt: attempt.Receipt})
		if err != nil {
			return false, nil, err
		}
		switch result.Status {
		case activity.ReconcileApplied:
			if result.Result == nil {
				return false, nil, fmt.Errorf("reconcile applied without result")
			}
			if err = s.runtime.Store.CompleteAttempt(ctx, step, attempt, *result.Result); err != nil {
				return false, nil, err
			}
			output, err := decodeAny(result.Result.Output)
			return true, output, err
		case activity.ReconcileNotApplied:
			if err = s.runtime.Store.MarkAttemptUnknown(ctx, step, attempt); err != nil {
				return false, nil, err
			}
			if err = s.runtime.Store.ResetStepReady(ctx, step.ID); err != nil {
				return false, nil, err
			}
			return false, nil, nil
		default:
			if err = s.runtime.Store.MarkAttemptUnknown(ctx, step, attempt); err != nil {
				return false, nil, err
			}
			if _, err = s.runtime.Store.CreateApproval(ctx, step, "activity outcome is unknown: "+spec.Ref()); err != nil {
				return false, nil, err
			}
			return false, nil, ErrNeedsHuman
		}
	case activity.EffectAtMostOnce, activity.EffectUnsafe:
		if err = s.runtime.Store.MarkAttemptUnknown(ctx, step, attempt); err != nil {
			return false, nil, err
		}
		if _, err = s.runtime.Store.CreateApproval(ctx, step, "activity outcome is unknown and cannot be retried safely: "+spec.Ref()); err != nil {
			return false, nil, err
		}
		return false, nil, ErrNeedsHuman
	default:
		return false, nil, fmt.Errorf("unknown effect mode %s", spec.Effect)
	}
}

func (s *executionState) resolve(path string) (any, bool) {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil, false
	}
	var current any
	switch parts[0] {
	case "inputs":
		current = s.inputs
		parts = parts[1:]
	case "steps":
		if len(parts) < 3 || parts[2] != "output" {
			return nil, false
		}
		value, ok := s.steps[parts[1]]
		if !ok {
			return nil, false
		}
		current = value
		parts = parts[3:]
	case "item":
		if !s.hasItem {
			return nil, false
		}
		current = s.item
		parts = parts[1:]
	case "index":
		if !s.hasItem {
			return nil, false
		}
		current = s.index
		parts = parts[1:]
	case "config":
		if len(parts) != 2 {
			return nil, false
		}
		value, ok := s.runtime.Config[parts[1]]
		return value, ok
	case "attempt":
		return 0, true
	case "run":
		current = map[string]any{"id": s.run.ID, "flowRef": s.run.FlowRef}
		parts = parts[1:]
	default:
		return nil, false
	}
	for _, part := range parts {
		switch container := current.(type) {
		case map[string]any:
			value, ok := container[part]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, ok := arrayIndex(part, len(container))
			if !ok {
				return nil, false
			}
			current = container[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// arrayIndex resolves one path segment against an array: a decimal index, or
// `last` for the final element. A `while` step aggregates every pass into an
// array, and `last` is what lets a flow assert on the value the loop actually
// ended with instead of shelling out to a JSON parser. An empty array has no
// last element, so the reference simply does not resolve.
func arrayIndex(part string, length int) (int, bool) {
	if part == "last" {
		if length == 0 {
			return 0, false
		}
		return length - 1, true
	}
	index, err := strconv.Atoi(part)
	if err != nil || index < 0 || index >= length {
		return 0, false
	}
	return index, true
}

func validateInputs(specs map[string]flow.InputSpec, inputs map[string]any, schemas map[string]*flow.Schema) error {
	for name, spec := range specs {
		value, ok := inputs[name]
		if !ok {
			if spec.Default == nil {
				return fmt.Errorf("missing required input %q", name)
			}
			continue
		}
		raw, _ := json.Marshal(value)
		if err := validateSchema(spec.Schema, raw, schemas); err != nil {
			return fmt.Errorf("input %s: %w", name, err)
		}
	}
	return nil
}
func validateSchema(ref string, raw []byte, schemas map[string]*flow.Schema) error {
	if ref == "" {
		return nil
	}
	schema, ok := schemas[ref]
	if !ok {
		return fmt.Errorf("schema %s is not present in the pinned IR", ref)
	}
	return schema.ValidateJSON(raw)
}
func (r *Runtime) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Runtime) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *Runtime) retryDelay(base time.Duration, attempt int, key string) time.Duration {
	if r.Jitter != nil {
		return r.Jitter(base, attempt, key)
	}
	delay := base
	for n := 1; n < attempt && delay < 5*time.Minute; n++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	if delay <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", key, attempt)))
	// Stable ±10% jitter keeps replay/test behavior deterministic while avoiding
	// synchronized retries across independent runs.
	percent := int(sum[0])%21 - 10
	return delay + time.Duration(int64(delay)*int64(percent)/100)
}
func decodeObject(raw []byte) (map[string]any, error) {
	value, err := decodeAny(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	return object, nil
}
func decodeAny(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}
func foreachKey(index int, item any) string {
	raw, _ := json.Marshal(item)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%06d-%s", index, hex.EncodeToString(sum[:4]))
}
func idempotencyKey(run, scope, step, key string) string {
	return strings.Join([]string{run, scope, step, key}, "/")
}
func keySuffix(key string) string {
	if key == "" {
		return ""
	}
	return "[" + key + "]"
}
