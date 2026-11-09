// Package supervision implements checkpoints from observable tool events.
// It never guesses that a read is exploratory or an edit is nonmechanical.
package supervision

import (
	"errors"
	"fmt"
	"strings"
)

var ErrCheckpointRequired = errors.New("supervisor checkpoint required")

type Policy struct {
	MaxToolCalls          int `json:"max_tool_calls,omitempty"`
	MaxExploratoryReads   int `json:"max_exploratory_reads,omitempty"`
	MaxNonmechanicalEdits int `json:"max_nonmechanical_edits,omitempty"`
}

func (p Policy) Validate() error {
	if p.MaxToolCalls < 0 || p.MaxExploratoryReads < 0 || p.MaxNonmechanicalEdits < 0 {
		return fmt.Errorf("supervision thresholds cannot be negative")
	}
	return nil
}
func (p Policy) Enabled() bool {
	return p.MaxToolCalls > 0 || p.MaxExploratoryReads > 0 || p.MaxNonmechanicalEdits > 0
}

type Counters struct {
	ToolCalls          int `json:"tool_calls"`
	ExploratoryReads   int `json:"exploratory_reads"`
	NonmechanicalEdits int `json:"nonmechanical_edits"`
}
type Observation struct {
	Tool          string `json:"tool"`
	Exploratory   bool   `json:"exploratory,omitempty"`
	Nonmechanical bool   `json:"nonmechanical,omitempty"`
	Incident      string `json:"incident,omitempty"`
}
type CheckpointError struct {
	Reason       string
	FreshContext bool
}

func (e *CheckpointError) Error() string { return ErrCheckpointRequired.Error() + ": " + e.Reason }
func (e *CheckpointError) Unwrap() error { return ErrCheckpointRequired }
func (p Policy) Observe(c *Counters, o Observation) error {
	if o.Tool != "" {
		c.ToolCalls++
	}
	if o.Exploratory {
		c.ExploratoryReads++
	}
	if o.Nonmechanical {
		c.NonmechanicalEdits++
	}
	if strings.TrimSpace(o.Incident) != "" {
		return &CheckpointError{Reason: "incident audit: " + o.Incident, FreshContext: true}
	}
	if p.MaxToolCalls > 0 && c.ToolCalls >= p.MaxToolCalls || p.MaxExploratoryReads > 0 && c.ExploratoryReads >= p.MaxExploratoryReads || p.MaxNonmechanicalEdits > 0 && c.NonmechanicalEdits >= p.MaxNonmechanicalEdits {
		return &CheckpointError{Reason: "observable tool budget reached; replan, delegate fresh, or record continuation justification", FreshContext: true}
	}
	return nil
}

// Route avoids orchestration only for explicitly known, single-step commands.
func Route(knownCommand bool, steps int) string {
	if knownCommand && steps == 1 {
		return "direct"
	}
	return "workflow"
}
