package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/execlock"
	"github.com/collectiveai-team/orquesta-lite/internal/external"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
)

const externalRequestTimeout = 30 * time.Second

func (r *Runtime) onlyExternalWorkPending(ctx context.Context, runID string) bool {
	run, err := r.Store.GetRun(ctx, runID)
	if err != nil || (run.Status != RunRunning && run.Status != RunWaitingExternal) {
		return false
	}
	steps, err := r.Store.ListSteps(ctx, runID)
	if err != nil {
		return false
	}
	waiting := false
	for _, step := range steps {
		if step.Status == StepRunning {
			return false
		}
		waiting = waiting || step.Status == StepWaitingExternal
	}
	return waiting
}

// ResumeExternal durably accepts a notification before acquiring the executor
// lock. A callback racing dispatch is left in the inbox for reconciliation.
// Events contain correlations, never a trusted completion result.
func (r *Runtime) ResumeExternal(ctx context.Context, runID, stepID, attemptID string, event external.Event) (*Run, error) {
	active, err := r.Store.ReceiveExternalEvent(ctx, runID, stepID, attemptID, event)
	if err != nil {
		return nil, err
	}
	if !active {
		return r.Store.GetRun(ctx, runID)
	}
	release, err := execlock.Acquire(r.Store.executorLock)
	if err != nil {
		// Acceptance is already durable. The executor or periodic reconciler
		// will inspect it; an active dispatch must not block webhook receipt.
		return r.Store.GetRun(ctx, runID)
	}
	defer release()
	return r.resumeExternalLocked(ctx, runID)
}

func (r *Runtime) resumeExternalLocked(ctx context.Context, runID string) (*Run, error) {
	run, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	switch run.Status {
	case RunPaused, RunNeedsHuman, RunFailed, RunSucceeded, RunCancelled:
		return run, nil
	}
	return r.resumeLocked(ctx, runID, false)
}

// ReconcileExternal uses the same inspection and validation path as callbacks.
func (r *Runtime) ReconcileExternal(ctx context.Context, runID string) (*Run, error) {
	release, err := execlock.Acquire(r.Store.executorLock)
	if err != nil {
		return nil, err
	}
	defer release()
	return r.resumeExternalLocked(ctx, runID)
}

// RunExternalReconciler is hosted by Orquesta (or another long-lived process).
// It requires no public HTTP listener and survives restarts through the store.
func (r *Runtime) RunExternalReconciler(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("external reconciliation interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		runs, err := r.Store.externalRecoveryRuns(ctx)
		if err != nil {
			return err
		}
		for _, runID := range runs {
			// A held executor lock or one unavailable adapter must not stop
			// periodic recovery of unrelated runs.
			_, _ = r.ReconcileExternal(ctx, runID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runtime) Pause(ctx context.Context, runID string) error {
	return r.Store.SetRunStatus(ctx, runID, RunPaused, "paused by operator")
}

func (r *Runtime) cancelExternalForDeadline(ctx context.Context, runID string) {
	records, err := r.Store.ListExternalAttempts(ctx, runID)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.Status == "decided" {
			continue
		}
		result := external.Result{}
		if record.Result != nil {
			result = *record.Result
		}
		_ = r.Store.RecordExternalCheck(ctx, record.ID, result, "timeout")
	}
	_ = r.CancelExternal(ctx, runID)
}

// CancelExternal first fences new dispatches, then records every remote
// cancellation acknowledgement. Failure to cancel remotely never un-cancels
// the local flow.
func (r *Runtime) CancelExternal(ctx context.Context, runID string) error {
	if err := r.Store.SetRunStatus(ctx, runID, RunCancelled, "cancelled by operator"); err != nil {
		return err
	}
	records, err := r.Store.ListExternalAttempts(ctx, runID)
	if err != nil {
		return err
	}
	steps, err := r.Store.ListSteps(ctx, runID)
	if err != nil {
		return err
	}
	byID := map[int64]StepRun{}
	for _, step := range steps {
		byID[step.ID] = step
	}
	var combined error
	for _, record := range records {
		if externalWorkTerminal(&record) || (record.Cancellation != nil && record.Cancellation.Accepted) {
			continue
		}
		if record.References.IssueID == "" {
			combined = errors.Join(combined, r.Store.SaveExternalCancellation(ctx, record.ID, external.Cancellation{Detail: "dispatch reference is not yet confirmed"}))
			continue
		}
		spec, decodeErr := external.DecodeStep(byID[record.StepRunID].Inputs)
		if decodeErr != nil {
			combined = errors.Join(combined, decodeErr)
			continue
		}
		adapter := r.ExternalAdapters[spec.Adapter]
		if err := r.cancelExternalAttempt(ctx, adapter, &record); err != nil {
			combined = errors.Join(combined, err)
		}
	}
	return combined
}

func (r *Runtime) cancelExternalAttempt(ctx context.Context, adapter external.Adapter, record *ExternalAttempt) error {
	cancellation := external.Cancellation{Detail: "adapter is not configured"}
	var cancelErr error
	if adapter != nil {
		remoteCtx, cancel := context.WithTimeout(ctx, externalRequestTimeout)
		cancellation, cancelErr = adapter.Cancel(remoteCtx, record.References)
		cancel()
		if cancelErr != nil {
			// Do not persist arbitrary network errors that may contain credentials.
			cancellation = external.Cancellation{Detail: "external cancellation could not be verified"}
		}
	} else {
		cancelErr = fmt.Errorf("external adapter is not configured")
	}
	return errors.Join(cancelErr, r.Store.SaveExternalCancellation(context.WithoutCancel(ctx), record.ID, cancellation))
}

func (r *Runtime) RetryExternal(ctx context.Context, runID, attemptID string) error {
	release, err := execlock.Acquire(r.Store.executorLock)
	if err != nil {
		return err
	}
	defer release()
	record, err := r.Store.GetExternalAttempt(ctx, attemptID)
	if err != nil {
		return err
	}
	if record.RunID != runID {
		return ErrExternalInactive
	}
	run, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	latest, err := r.Store.LatestExternalAttempt(ctx, record.StepRunID)
	if err != nil {
		return err
	}
	if latest.ID != record.ID || run.Status == RunCancelled || run.Status == RunSucceeded {
		return ErrExternalInactive
	}
	if !externalWorkTerminal(record) && record.References.IssueID == "" {
		return fmt.Errorf("%w: reconcile the uncertain dispatch before retrying", ErrNeedsHuman)
	}
	if record.References.IssueID != "" && !externalWorkTerminal(record) && (record.Cancellation == nil || !record.Cancellation.Accepted) {
		steps, err := r.Store.ListSteps(ctx, runID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.ID != record.StepRunID {
				continue
			}
			spec, err := external.DecodeStep(step.Inputs)
			if err != nil {
				return err
			}
			if err := r.cancelExternalAttempt(ctx, r.ExternalAdapters[spec.Adapter], record); err != nil {
				return fmt.Errorf("%w: previous external work cancellation is unconfirmed", ErrNeedsHuman)
			}
		}
		confirmed, err := r.Store.GetExternalAttempt(ctx, attemptID)
		if err != nil {
			return err
		}
		if confirmed.Cancellation == nil || !confirmed.Cancellation.Accepted {
			return fmt.Errorf("%w: previous external work cancellation is unconfirmed", ErrNeedsHuman)
		}
	}
	return r.Store.RetryExternal(ctx, runID, record.StepID, attemptID)
}

func externalWorkTerminal(record *ExternalAttempt) bool {
	if record.Result == nil {
		return false
	}
	switch record.Result.TaskStatus {
	case "done", "completed", "cancelled", "canceled":
		switch record.Result.RunStatus {
		case "succeeded", "completed", "failed", "error", "timed_out", "cancelled", "canceled":
			return true
		}
	}
	return false
}

func (s *executionState) executeExternal(ctx context.Context, step flow.IRStep, stored *StepRun) (any, error) {
	spec, err := external.DecodeStep(stored.Inputs)
	if err != nil {
		return nil, &activity.Error{Class: activity.ErrorInvalidContract, Op: step.Uses.String(), Err: err}
	}
	if stored.Status == StepNeedsHuman {
		return nil, ErrNeedsHuman
	}
	adapter := s.runtime.ExternalAdapters[spec.Adapter]
	if adapter == nil {
		return nil, fmt.Errorf("external adapter %q is not configured", spec.Adapter)
	}
	record, err := s.runtime.Store.LatestExternalAttempt(ctx, stored.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record.Status == "decided" && stored.Status == StepReady) {
		if err := s.checkBudget(ctx); err != nil {
			return nil, err
		}
		remoteCtx, cancel := context.WithTimeout(ctx, externalRequestTimeout)
		employee, resolveErr := adapter.ResolveEmployee(remoteCtx, spec.Employee)
		cancel()
		if resolveErr != nil {
			return nil, fmt.Errorf("external employee resolution failed: %w", resolveErr)
		}
		request := external.DispatchRequest{SourceKey: s.run.SourceKey, EmployeeID: employee, ProjectID: spec.ProjectID, Instructions: spec.Instructions, CompletionCondition: spec.CompletionCondition, Deadline: s.runtime.now().Add(time.Duration(spec.TimeoutSeconds) * time.Second)}
		if spec.OutputSchema != "" {
			schema := s.schemas[spec.OutputSchema]
			if schema == nil {
				return nil, fmt.Errorf("external output schema %s is not pinned", spec.OutputSchema)
			}
			request.OutputSchema, err = json.Marshal(schema)
			if err != nil {
				return nil, err
			}
		}
		record, err = s.runtime.Store.PrepareExternal(ctx, stored, step.Activity.Ref(), request)
	}
	if err != nil {
		if errors.Is(err, ErrExternalInactive) {
			return nil, ErrPaused
		}
		return nil, err
	}
	result := external.Result{}
	if record.Result != nil {
		result = *record.Result
	}
	if !s.runtime.now().Before(record.Request.Deadline) {
		if record.References.IssueID != "" && (record.Cancellation == nil || !record.Cancellation.Accepted) {
			_ = s.runtime.cancelExternalAttempt(ctx, adapter, record)
		}
		return s.finishExternal(ctx, spec, record, result, "timeout")
	}
	if record.References.IssueID == "" {
		// Paperclip's deduplication records expire after seven days. Preserve
		// uncertainty for intervention instead of risking a second assignment.
		if s.runtime.now().Sub(record.CreatedAt) >= 6*24*time.Hour {
			return s.finishExternal(ctx, spec, record, result, "invalid")
		}
		if err := s.checkExternalControl(ctx); err != nil {
			return nil, err
		}
		remoteCtx, cancel := context.WithTimeout(ctx, externalRequestTimeout)
		refs, dispatchErr := adapter.Dispatch(remoteCtx, record.Request)
		cancel()
		if dispatchErr != nil {
			outcome := "invalid"
			var unavailable *external.UnavailableError
			if errors.As(dispatchErr, &unavailable) || errors.Is(dispatchErr, context.DeadlineExceeded) {
				outcome = "unavailable"
			}
			return s.finishExternal(ctx, spec, record, result, outcome)
		}
		if err := s.runtime.Store.SaveExternalDispatch(context.WithoutCancel(ctx), record.ID, refs); err != nil {
			return nil, err
		}
		record.References = refs
		if latest, err := s.runtime.Store.GetRun(context.WithoutCancel(ctx), s.run.ID); err == nil && latest.Status == RunCancelled {
			_ = s.runtime.cancelExternalAttempt(context.WithoutCancel(ctx), adapter, record)
			return nil, context.Canceled
		}
		return nil, ErrWaitingExternal
	}
	if err := s.checkExternalControl(ctx); err != nil {
		return nil, err
	}
	remoteCtx, cancel := context.WithTimeout(ctx, externalRequestTimeout)
	result, inspectErr := adapter.Inspect(remoteCtx, record.References)
	cancel()
	if inspectErr != nil {
		var unavailable *external.UnavailableError
		outcome := "invalid"
		if errors.As(inspectErr, &unavailable) || errors.Is(inspectErr, context.DeadlineExceeded) {
			outcome = "unavailable"
		}
		// Retain previously reported usage during a temporary outage.
		if record.Result != nil {
			result = *record.Result
		}
		return s.finishExternal(ctx, spec, record, result, outcome)
	}
	if !s.runtime.now().Before(record.Request.Deadline) {
		_ = s.runtime.cancelExternalAttempt(ctx, adapter, record)
		return s.finishExternal(ctx, spec, record, result, "timeout")
	}
	outcome := externalOutcome(record, result)
	if outcome == "success" {
		if err := s.validateExternalResult(spec, record, result); err != nil {
			outcome = "invalid"
		}
	}
	return s.finishExternal(ctx, spec, record, result, outcome)
}

func (s *executionState) checkExternalControl(ctx context.Context) error {
	run, err := s.runtime.Store.GetRun(ctx, s.run.ID)
	if err != nil {
		return err
	}
	if run.Status == RunCancelled {
		return context.Canceled
	}
	if run.Status == RunPaused {
		return ErrPaused
	}
	if run.Status != RunRunning && run.Status != RunWaitingExternal {
		return ErrNeedsHuman
	}
	return ctx.Err()
}

func externalOutcome(record *ExternalAttempt, result external.Result) string {
	if !result.ValidUsage() {
		return "invalid"
	}
	if result.EmployeeID != record.EmployeeID {
		return "invalid"
	}
	switch strings.ToLower(result.EmployeeStatus) {
	case "paused", "blocked", "error", "terminated":
		return "blocked"
	}
	switch strings.ToLower(result.TaskStatus) {
	case "cancelled", "canceled":
		return "cancelled"
	case "blocked":
		return "blocked"
	case "failed", "error":
		return "failed"
	}
	switch strings.ToLower(result.RunStatus) {
	case "failed", "error", "timed_out":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "blocked":
		return "blocked"
	}
	if (result.TaskStatus == "done" || result.TaskStatus == "completed") && (result.RunStatus == "succeeded" || result.RunStatus == "completed") {
		return "success"
	}
	return "waiting"
}

func (s *executionState) validateExternalResult(spec external.Step, record *ExternalAttempt, result external.Result) error {
	if result.EmployeeID != record.EmployeeID || !json.Valid(result.Deliverables) || string(result.Deliverables) == "null" {
		return fmt.Errorf("external deliverables or responsible employee are invalid")
	}
	if spec.RequireEvidence == nil || *spec.RequireEvidence {
		valid := false
		for _, ref := range result.Evidence {
			valid = valid || strings.TrimSpace(ref) != ""
		}
		if !valid {
			return fmt.Errorf("external result has no evidence")
		}
	}
	if !result.ValidUsage() {
		return fmt.Errorf("external usage is invalid")
	}
	if err := validateSchema(spec.OutputSchema, result.Deliverables, s.schemas); err != nil {
		return err
	}
	if spec.CompletionGate != "" {
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		value, err := decodeAny(raw)
		if err != nil {
			return err
		}
		gate := *s
		gate.item, gate.hasItem = value, true
		passed, err := flow.EvalBool(spec.CompletionGate, gate.resolve)
		if err != nil {
			return err
		}
		if !passed {
			return fmt.Errorf("external completion gate failed")
		}
	}
	return nil
}

func (s *executionState) finishExternal(ctx context.Context, spec external.Step, record *ExternalAttempt, result external.Result, outcome string) (any, error) {
	if outcome == "waiting" || spec.Route(outcome) == "wait" {
		if err := s.runtime.Store.RecordExternalCheck(ctx, record.ID, result, outcome); err != nil {
			return nil, err
		}
		return nil, ErrWaitingExternal
	}
	decision := spec.Route(outcome)
	status := StepFailed
	switch decision {
	case "continue":
		status = StepRouted
		if outcome == "success" {
			status = StepSucceeded
		}
	case "needs_human":
		status = StepNeedsHuman
	}
	output, err := json.Marshal(map[string]any{"outcome": outcome, "decision": decision, "result": result})
	if err != nil {
		return nil, err
	}
	if outcome == "success" && spec.OutputMode == "deliverables" {
		output = result.Deliverables
	}
	if len(output) > maxWorkflowValueBytes {
		return nil, fmt.Errorf("external result exceeds %d bytes", maxWorkflowValueBytes)
	}
	applied, err := s.runtime.Store.DecideExternal(ctx, record.ID, result, outcome, decision, output, status)
	if err != nil {
		return nil, err
	}
	if !applied {
		if err := s.checkExternalControl(ctx); err != nil {
			return nil, err
		}
		return nil, ErrWaitingExternal
	}
	if status == StepNeedsHuman {
		return nil, ErrNeedsHuman
	}
	if status == StepFailed {
		return nil, &activity.Error{Class: activity.ErrorPermanent, Op: "external.dispatch", Err: fmt.Errorf("external outcome: %s", outcome)}
	}
	return decodeAny(output)
}
