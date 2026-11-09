package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/execlock"
)

var ErrNeedsDecision = errors.New("unit needs human decision")

type CorrectionLimits struct {
	MaxSeconds      int `json:"max_seconds"`
	MaxTokens       int `json:"max_tokens"`
	MaxToolCalls    int `json:"max_tool_calls"`
	MaxInconclusive int `json:"max_inconclusive"`
}

func DefaultCorrectionLimits() CorrectionLimits {
	return CorrectionLimits{MaxSeconds: 900, MaxTokens: 100000, MaxToolCalls: 30, MaxInconclusive: 2}
}
func (l CorrectionLimits) Validate() error {
	if l.MaxSeconds < 1 || l.MaxTokens < 1 || l.MaxToolCalls < 1 || l.MaxInconclusive < 0 || l.MaxInconclusive > 10 {
		return fmt.Errorf("correction limits require positive time/token/tool ceilings and 0..10 inconclusive recoveries")
	}
	return nil
}

type Unit struct {
	Version        int              `json:"version"`
	ID             string           `json:"id"`
	Risk           Decision         `json:"risk"`
	Limits         CorrectionLimits `json:"limits"`
	PatchLines     int              `json:"patch_lines"`
	Status         string           `json:"status"`
	Reservation    string           `json:"reservation,omitempty"`
	CorrectionTask string           `json:"correction_task,omitempty"`
	StartedAt      time.Time        `json:"started_at,omitempty"`
	Inconclusive   int              `json:"inconclusive"`
	Validation     *Validation      `json:"validation,omitempty"`
}

type Validation struct {
	TreeHash       string `json:"tree_hash"`
	Readable       bool   `json:"readable"`
	Passed         bool   `json:"passed"`
	ChangedLines   int    `json:"changed_lines"`
	Tokens         int    `json:"tokens"`
	ToolCalls      int    `json:"tool_calls"`
	EvidencePath   string `json:"evidence_path"`
	EvidenceDigest string `json:"evidence_digest"`
}

// UnitStore is scoped to one run. The exclusive filesystem lock and atomic
// rename make reservation survive crashes and prohibit concurrent repairs.
type UnitStore struct {
	Root string
	Now  func() time.Time
}

func (s UnitStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s UnitStore) Path(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.Root, hex.EncodeToString(sum[:])+".json")
}
func (s UnitStore) Load(id string) (Unit, error) {
	var u Unit
	raw, err := os.ReadFile(s.Path(id))
	if err != nil {
		return u, err
	}
	err = json.Unmarshal(raw, &u)
	if err == nil && (u.Version != 1 || u.ID != id) {
		err = fmt.Errorf("invalid durable unit identity")
	}
	return u, err
}

func (s UnitStore) update(id string, fn func(*Unit) error) (Unit, error) {
	if id == "" || s.Root == "" {
		return Unit{}, fmt.Errorf("unit ID and root required")
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return Unit{}, err
	}
	release, err := execlock.Acquire(s.Path(id) + ".lock")
	if err != nil {
		return Unit{}, fmt.Errorf("unit update already active: %w", err)
	}
	defer release()
	u, err := s.Load(id)
	if err != nil && !os.IsNotExist(err) {
		return u, err
	}
	u.ID = id
	operationErr := fn(&u)
	// A failed decision can deliberately persist needs_decision state.
	if operationErr != nil && !errors.Is(operationErr, ErrNeedsDecision) {
		return u, operationErr
	}
	raw, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return u, err
	}
	tmp, err := os.CreateTemp(s.Root, "unit-*.tmp")
	if err != nil {
		return u, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return u, err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return u, err
	}
	if err = tmp.Close(); err != nil {
		return u, err
	}
	if err = os.Rename(name, s.Path(id)); err != nil {
		return u, err
	}
	return u, operationErr
}

func (s UnitStore) Initialize(id string, c Candidate, p Policy, l CorrectionLimits) (Unit, error) {
	return s.update(id, func(u *Unit) error {
		if u.Version != 0 {
			if u.Risk.Candidate.TreeHash != c.TreeHash {
				return fmt.Errorf("unit already initialized for another candidate")
			}
			return nil
		}
		if err := l.Validate(); err != nil {
			return err
		}
		d, err := Classify(c, p)
		if err != nil {
			return err
		}
		budget := (c.ChangedLines + 1) / 2
		if budget > 200 {
			budget = 200
		}
		*u = Unit{Version: 1, ID: id, Risk: d, Limits: l, PatchLines: budget, Status: "reviewing"}
		return nil
	})
}

func (s UnitStore) BeginCorrection(id, reservation string) (Unit, error) {
	return s.update(id, func(u *Unit) error {
		if u.Version != 1 || reservation == "" {
			return fmt.Errorf("initialized unit and reservation required")
		}
		if u.Reservation == reservation && u.Status == "correcting" {
			return nil
		}
		if u.Reservation != "" || u.Status != "reviewing" {
			u.Status = "needs_decision"
			return fmt.Errorf("%w: the single correction is already reserved or exhausted", ErrNeedsDecision)
		}
		u.Reservation = reservation
		u.StartedAt = s.now()
		u.Status = "correcting"
		return nil
	})
}

// ClaimInvocation binds the reservation to one durable agent step. Repeating
// that task may repair its output contract; another task cannot edit products.
func (s UnitStore) ClaimInvocation(id, taskID string) (Unit, error) {
	return s.update(id, func(u *Unit) error {
		if taskID == "" || u.Version != 1 || u.Reservation == "" || u.Status != "correcting" {
			return fmt.Errorf("active correction and task identity required")
		}
		if u.CorrectionTask == taskID {
			return nil
		}
		if u.CorrectionTask != "" {
			u.Status = "needs_decision"
			return fmt.Errorf("%w: correction belongs to another invocation", ErrNeedsDecision)
		}
		u.CorrectionTask = taskID
		return nil
	})
}

// FinishValidation accepts only runtime-produced counts and evidence. An
// unreadable tree permits another read, never another product correction.
func (s UnitStore) FinishValidation(id string, v Validation, next Candidate, p Policy) (Unit, error) {
	return s.update(id, func(u *Unit) error {
		if u.Version != 1 || u.Reservation == "" || u.Status != "correcting" {
			return fmt.Errorf("correction not active")
		}
		decide := func(reason string) error {
			u.Status = "needs_decision"
			return fmt.Errorf("%w: %s", ErrNeedsDecision, reason)
		}
		if s.now().Sub(u.StartedAt) > time.Duration(u.Limits.MaxSeconds)*time.Second {
			return decide("correction time budget exceeded")
		}
		if v.Tokens < 0 || v.ToolCalls < 0 || v.ChangedLines < 0 {
			return fmt.Errorf("negative correction usage")
		}
		if v.Tokens > u.Limits.MaxTokens || v.ToolCalls > u.Limits.MaxToolCalls || v.ChangedLines > u.PatchLines {
			return decide("correction budget exceeded")
		}
		if !v.Readable {
			u.Inconclusive++
			if u.Inconclusive > u.Limits.MaxInconclusive {
				return decide("tree remains unreadable after bounded recovery")
			}
			return nil
		}
		if v.TreeHash == "" || v.TreeHash != next.TreeHash {
			return fmt.Errorf("validation tree does not match candidate")
		}
		if err := CheckScope(u.Risk, next, p); err != nil {
			return decide(err.Error())
		}
		if err := ValidateEvidence(v); err != nil {
			return decide(err.Error())
		}
		u.Validation = &v
		if !v.Passed {
			return decide("correction failed with evidence")
		}
		u.Status = "verified"
		return nil
	})
}

func ValidateEvidence(v Validation) error {
	if v.EvidencePath == "" || v.EvidenceDigest == "" {
		return fmt.Errorf("validation requires runtime evidence")
	}
	raw, err := os.ReadFile(v.EvidencePath)
	if err != nil {
		return fmt.Errorf("validation evidence unavailable: %w", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != v.EvidenceDigest {
		return fmt.Errorf("validation evidence digest mismatch")
	}
	return nil
}

// Verify detects artifact loss without re-executing an already applied repair.
func (s UnitStore) Verify(id string) error {
	u, err := s.Load(id)
	if err != nil {
		return err
	}
	if u.Status != "verified" || u.Validation == nil {
		return fmt.Errorf("%w: unit is not verified", ErrNeedsDecision)
	}
	if err = ValidateEvidence(*u.Validation); err != nil {
		return fmt.Errorf("%w: %v", ErrNeedsDecision, err)
	}
	return nil
}
