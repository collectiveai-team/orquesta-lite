package loops

import (
	"errors"
	"strings"
	"testing"
)

func TestFullSuiteError_CarriesOutputAndMatchesSentinel(t *testing.T) {
	e := &FullSuiteError{Output: "FAILED test/test_docker.py::test_real - AssertionError\n1 failed, 118 passed"}

	// Existing sentinel checks must keep working.
	if !errors.Is(e, ErrFullSuiteFailed) {
		t.Errorf("FullSuiteError must satisfy errors.Is(err, ErrFullSuiteFailed)")
	}
	// The output must be reachable so the coder can be told what broke.
	if !strings.Contains(e.Error(), "test_real") {
		t.Errorf("Error() must include the failing-test output, got: %q", e.Error())
	}
	var fs *FullSuiteError
	if !errors.As(e, &fs) || fs.Output == "" {
		t.Errorf("errors.As must recover the output for feedback")
	}
}

func TestFullSuiteError_EmptyOutputFallsBackToSentinelMessage(t *testing.T) {
	e := &FullSuiteError{}
	if e.Error() != ErrFullSuiteFailed.Error() {
		t.Errorf("empty output should read as the sentinel message, got: %q", e.Error())
	}
	if !errors.Is(e, ErrFullSuiteFailed) {
		t.Errorf("must still match the sentinel with empty output")
	}
}
