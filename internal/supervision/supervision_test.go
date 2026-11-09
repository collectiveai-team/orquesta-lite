package supervision

import (
	"errors"
	"testing"
	"time"
)

func TestObservableBudgetAndIncident(t *testing.T) {
	p := Policy{MaxToolCalls: 2, MaxExploratoryReads: 1}
	var c Counters
	if err := p.Observe(&c, Observation{Tool: "Read"}); err != nil {
		t.Fatal(err)
	}
	if c.ExploratoryReads != 0 {
		t.Fatal("read classified exploratory without evidence")
	}
	if err := p.Observe(&c, Observation{Tool: "Read"}); !errors.Is(err, ErrCheckpointRequired) {
		t.Fatal("tool limit did not checkpoint")
	}
	var clean Counters
	var checkpoint *CheckpointError
	if err := p.Observe(&clean, Observation{Incident: "wrong cwd"}); !errors.As(err, &checkpoint) || !checkpoint.FreshContext {
		t.Fatal("incident requires fresh audit")
	}
	if Route(true, 1) != "direct" || Route(false, 1) != "workflow" || Route(true, 2) != "workflow" {
		t.Fatal("trivial route mismatch")
	}
}

func TestObservationHistorySurvivesCheckpointReset(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	for _, record := range []Record{
		{Task: "repair", Role: "integrator", Time: now, Observation: Observation{Tool: "Read"}},
		{Task: "review", Role: "reviewer", Time: now, Observation: Observation{Tool: "Read"}},
		{Task: "repair", Role: "integrator", Time: now.Add(-time.Hour), Observation: Observation{Tool: "Read"}},
	} {
		if err := AppendObservation(root, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := Acknowledge(root, "repair", "integrator", "continue", "audited"); err != nil {
		t.Fatal(err)
	}
	counts, err := Measure(root, "repair", "integrator", now.Add(-time.Minute))
	if err != nil || counts.ToolCalls != 1 {
		t.Fatalf("wrong correction count: %+v %v", counts, err)
	}
}

func TestAcknowledgementRequiresConcreteDecision(t *testing.T) {
	root := t.TempDir()
	if Acknowledge(root, "task", "coder", "continue", "") == nil {
		t.Fatal("empty justification accepted")
	}
	if err := Acknowledge(root, "task", "coder", "delegate", "fresh worker after repeated exploration"); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorAcknowledgementCannotApproveLaterCheckpoint(t *testing.T) {
	root := t.TempDir()
	before := time.Now().UTC().Add(-time.Second)
	if err := Acknowledge(root, "task", "coder", "continue", "audit confirmed"); err != nil {
		t.Fatal(err)
	}
	valid, err := HasAcknowledgement(root, "task", "coder", before)
	if err != nil || !valid {
		t.Fatal("fresh acknowledgement rejected")
	}
	valid, err = HasAcknowledgement(root, "task", "coder", time.Now().UTC().Add(time.Second))
	if err != nil || valid {
		t.Fatal("old acknowledgement accepted for later checkpoint")
	}
}
