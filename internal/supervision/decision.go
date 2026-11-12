package supervision

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Decision struct {
	Task          string    `json:"task"`
	Role          string    `json:"role"`
	Action        string    `json:"action"`
	Justification string    `json:"justification"`
	Time          time.Time `json:"time"`
	FreshContext  bool      `json:"fresh_context"`
}

// Acknowledge records the human/supervisor decision before resetting the
// checkpoint counters. The caller must start a fresh context after this call.
func Acknowledge(root, task, role, action, justification string) error {
	if root == "" || task == "" || role == "" || strings.TrimSpace(justification) == "" {
		return fmt.Errorf("supervision decision requires root, task, role and justification")
	}
	if action != "replan" && action != "delegate" && action != "continue" {
		return fmt.Errorf("supervision action must be replan, delegate or continue")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "supervision-decisions.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(Decision{task, role, action, justification, time.Now().UTC(), true})
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(task+"\x00"+role)))
	marker, err := json.Marshal(Decision{task, role, action, justification, time.Now().UTC(), true})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "supervision-fresh-"+id+".json"), marker, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "supervision-"+id+".json"), []byte("{}"), 0600)
}

// ContextEpoch identifies the latest acknowledged checkpoint. Session keys
// include this epoch, so restart cannot accidentally resume pre-audit context.
func ContextEpoch(root, task, role string) (string, error) {
	if root == "" {
		return "", nil
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(task+"\x00"+role)))
	raw, err := os.ReadFile(filepath.Join(root, "supervision-fresh-"+id+".json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var decision Decision
	if err = json.Unmarshal(raw, &decision); err != nil {
		return "", err
	}
	if decision.Task != task || decision.Role != role || !decision.FreshContext {
		return "", fmt.Errorf("invalid supervision context marker")
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

// LatestDecision returns the durable instruction that created the fresh
// session epoch. It is dynamic context and never part of the fixed prefix.
func LatestDecision(root, task, role string) (Decision, bool, error) {
	if root == "" {
		return Decision{}, false, nil
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(task+"\x00"+role)))
	raw, err := os.ReadFile(filepath.Join(root, "supervision-fresh-"+id+".json"))
	if os.IsNotExist(err) {
		return Decision{}, false, nil
	}
	if err != nil {
		return Decision{}, false, err
	}
	var decision Decision
	if err := json.Unmarshal(raw, &decision); err != nil {
		return decision, false, err
	}
	if decision.Task != task || decision.Role != role || !decision.FreshContext || decision.Time.IsZero() || strings.TrimSpace(decision.Justification) == "" || (decision.Action != "replan" && decision.Action != "delegate" && decision.Action != "continue") {
		return decision, false, fmt.Errorf("invalid supervision decision marker")
	}
	return decision, true, nil
}

// HasAcknowledgement rejects a stale acknowledgement from an earlier pause of
// the same durable task. The new checkpoint requires its own decision.
func HasAcknowledgement(root, task, role string, since time.Time) (bool, error) {
	decision, found, err := LatestDecision(root, task, role)
	return found && decision.Time.After(since), err
}
