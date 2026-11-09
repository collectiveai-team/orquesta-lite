package telemetry

import (
	"os"
	"path/filepath"
	"time"
)

// TaskReport measures only the correction's durable task. Other reviewers and
// concurrent units cannot spend or replenish its budget.
func TaskReport(root, task string, since time.Time) (Report, error) {
	calls, err := Read(filepath.Join(root, "usage.jsonl"))
	if os.IsNotExist(err) {
		return Summarize(nil), nil
	}
	if err != nil {
		return Report{}, err
	}
	selected := make([]Call, 0, len(calls))
	for _, call := range calls {
		if call.Task == task && !call.Time.Before(since) {
			selected = append(selected, call)
		}
	}
	return Summarize(selected), nil
}
