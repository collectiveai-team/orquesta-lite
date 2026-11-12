package supervision

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record contains observable classifications only. Tool arguments are omitted:
// this journal counts work without duplicating arbitrary command payloads.
type Record struct {
	Task        string      `json:"task"`
	Role        string      `json:"role"`
	Time        time.Time   `json:"time"`
	Observation Observation `json:"observation"`
}

var observationsMu sync.Mutex

func AppendObservation(root string, record Record) error {
	if root == "" || record.Task == "" || record.Role == "" || record.Time.IsZero() {
		return fmt.Errorf("observation requires run root, task, role and time")
	}
	observationsMu.Lock()
	defer observationsMu.Unlock()
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "tool-observations.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(f).Encode(record)
	closeErr := f.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

// Measure counts immutable observations for one role invocation after a
// reservation. Acknowledging a checkpoint resets counters, never this history.
func Measure(root, task, role string, since time.Time) (Counters, error) {
	f, err := os.Open(filepath.Join(root, "tool-observations.jsonl"))
	if os.IsNotExist(err) {
		return Counters{}, nil
	}
	if err != nil {
		return Counters{}, err
	}
	defer f.Close()
	var result Counters
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1<<20)
	for s.Scan() {
		var record Record
		if err := json.Unmarshal(s.Bytes(), &record); err != nil {
			return result, err
		}
		if record.Task != task || role != "" && record.Role != role || record.Time.Before(since) {
			continue
		}
		if record.Observation.Tool != "" {
			result.ToolCalls++
		}
		if record.Observation.Exploratory {
			result.ExploratoryReads++
		}
		if record.Observation.Nonmechanical {
			result.NonmechanicalEdits++
		}
	}
	return result, s.Err()
}
