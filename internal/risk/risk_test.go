package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/execlock"
)

func TestRiskSignalsAndGlobstar(t *testing.T) {
	p := DefaultPolicy()
	for _, tc := range []struct {
		name   string
		c      Candidate
		level  string
		lenses int
	}{
		{"small covered", Candidate{TreeHash: "tree", BaseRevision: "base", Paths: []string{"docs/help.md"}, ChangedLines: 2, CoverageKnown: true}, "low", 0},
		{"unknown coverage", Candidate{TreeHash: "tree", BaseRevision: "base", Paths: []string{"pkg/foo.go"}, ChangedLines: 2}, "standard", 1},
		{"root auth", Candidate{TreeHash: "tree", BaseRevision: "base", Paths: []string{"auth/login.go"}, CoverageKnown: true}, "high", 4},
		{"nested auth", Candidate{TreeHash: "tree", BaseRevision: "base", Paths: []string{"pkg/auth/login.go"}, CoverageKnown: true}, "high", 4},
		{"large", Candidate{TreeHash: "tree", BaseRevision: "base", Paths: []string{"foo.go"}, ChangedLines: 400, CoverageKnown: true}, "high", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Classify(tc.c, p)
			if err != nil {
				t.Fatal(err)
			}
			if d.Level != tc.level || len(d.Lenses) != tc.lenses {
				t.Fatalf("got %+v", d)
			}
		})
	}
}

func TestRepairScopeCannotAddSensitivePaths(t *testing.T) {
	p := DefaultPolicy()
	c := Candidate{TreeHash: "one", BaseRevision: "base", Paths: []string{"foo.go"}, ChangedLines: 10}
	d, err := Classify(c, p)
	if err != nil {
		t.Fatal(err)
	}
	c.TreeHash = "two"
	c.Paths = append(c.Paths, "payments/charge.go")
	if CheckScope(d, c, p) == nil {
		t.Fatal("new sensitive path accepted")
	}
}

func TestUnitReservationPersistsAndEvidenceLossDoesNotRepeatRepair(t *testing.T) {
	now := time.Now().UTC()
	s := UnitStore{Root: t.TempDir(), Now: func() time.Time { return now }}
	p := DefaultPolicy()
	c := Candidate{TreeHash: "tree", BaseRevision: "base", Paths: []string{"foo.go"}, ChangedLines: 20}
	u, err := s.Initialize("unit", c, p, DefaultCorrectionLimits())
	if err != nil || u.PatchLines != 10 {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err = s.BeginCorrection("unit", "reservation"); err != nil {
		t.Fatal(err)
	}
	s = UnitStore{Root: s.Root, Now: func() time.Time { return now }}
	if _, err = s.BeginCorrection("unit", "reservation"); err != nil {
		t.Fatal("idempotent reservation", err)
	}
	evidence := filepath.Join(t.TempDir(), "validation.json")
	raw := []byte(`{"passed":true}`)
	if err = os.WriteFile(evidence, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	v := Validation{TreeHash: "tree", Readable: true, Passed: true, ChangedLines: 5, Tokens: 100, ToolCalls: 1, EvidencePath: evidence, EvidenceDigest: hex.EncodeToString(sum[:])}
	u, err = s.FinishValidation("unit", v, c, p)
	if err != nil || u.Status != "verified" {
		t.Fatalf("%+v %v", u, err)
	}
	if err = s.Verify("unit"); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(s.Verify("unit"), ErrNeedsDecision) {
		t.Fatal("missing evidence accepted")
	}
	if _, err = s.BeginCorrection("unit", "another"); !errors.Is(err, ErrNeedsDecision) {
		t.Fatal("second correction accepted", err)
	}
}

func TestInconclusiveValidationIsBoundedAndCannotRestartCorrection(t *testing.T) {
	s := UnitStore{Root: t.TempDir()}
	p := DefaultPolicy()
	c := Candidate{TreeHash: "tree", BaseRevision: "base", ChangedLines: 10}
	limits := DefaultCorrectionLimits()
	limits.MaxInconclusive = 1
	if _, err := s.Initialize("unit", c, p, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCorrection("unit", "one"); err != nil {
		t.Fatal(err)
	}
	u, err := s.FinishValidation("unit", Validation{Readable: false}, c, p)
	if err != nil || u.Status != "correcting" {
		t.Fatalf("%+v %v", u, err)
	}
	u, err = s.FinishValidation("unit", Validation{Readable: false}, c, p)
	if !errors.Is(err, ErrNeedsDecision) || u.Status != "needs_decision" {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestCorrectionBudgetFailureIsDurable(t *testing.T) {
	s := UnitStore{Root: t.TempDir()}
	p := DefaultPolicy()
	c := Candidate{TreeHash: "tree", BaseRevision: "base", ChangedLines: 4}
	if _, err := s.Initialize("unit", c, p, DefaultCorrectionLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCorrection("unit", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishValidation("unit", Validation{ChangedLines: 3}, c, p); !errors.Is(err, ErrNeedsDecision) {
		t.Fatal(err)
	}
	u, err := s.Load("unit")
	if err != nil || u.Status != "needs_decision" {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestUnitLockSurvivesFileButNotAbandonedProcess(t *testing.T) {
	s := UnitStore{Root: t.TempDir()}
	id := "unit"
	if err := os.WriteFile(s.Path(id)+".lock", []byte("abandoned"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := execlock.Acquire(s.Path(id) + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	c := Candidate{TreeHash: "tree", BaseRevision: "base", ChangedLines: 2}
	if _, err = s.Initialize(id, c, DefaultPolicy(), DefaultCorrectionLimits()); err == nil {
		release()
		t.Fatal("concurrent update accepted")
	}
	release()
	if _, err = s.Initialize(id, c, DefaultPolicy(), DefaultCorrectionLimits()); err != nil {
		t.Fatal("abandoned lock file prevented recovery", err)
	}
}
