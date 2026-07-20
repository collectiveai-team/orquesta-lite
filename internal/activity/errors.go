package activity

import "fmt"

type ErrorClass string

const (
	ErrorTransient        ErrorClass = "transient"
	ErrorRateLimited      ErrorClass = "rate_limited"
	ErrorTimeout          ErrorClass = "timeout"
	ErrorInvalidContract  ErrorClass = "invalid_contract"
	ErrorGateFailed       ErrorClass = "gate_failed"
	ErrorConflict         ErrorClass = "conflict"
	ErrorPermissionDenied ErrorClass = "permission_denied"
	ErrorCancelled        ErrorClass = "cancelled"
	ErrorPermanent        ErrorClass = "permanent"
	ErrorOutcomeUnknown   ErrorClass = "outcome_unknown"
	ErrorNeedsHuman       ErrorClass = "needs_human"
)

type ApprovalRequiredError struct {
	Reason string
	Input  []byte
}

func (e *ApprovalRequiredError) Error() string { return "approval required: " + e.Reason }

// Error carries a stable machine-readable class without discarding the cause.
type Error struct {
	Class ErrorClass
	Op    string
	Err   error
}

func (e *Error) Error() string {
	if e.Op == "" {
		return fmt.Sprintf("%s: %v", e.Class, e.Err)
	}
	return fmt.Sprintf("%s %s: %v", e.Op, e.Class, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func Classify(err error) ErrorClass {
	if err == nil {
		return ""
	}
	for current := err; current != nil; {
		if classified, ok := current.(*Error); ok {
			return classified.Class
		}
		type unwrapper interface{ Unwrap() error }
		next, ok := current.(unwrapper)
		if !ok {
			break
		}
		current = next.Unwrap()
	}
	return ErrorPermanent
}
