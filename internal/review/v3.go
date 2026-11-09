package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ClaimV3 is the model-owned portion of review-result@3. IDs, ordering,
// causal attribution and approval are assigned by the runtime.
type ClaimV3 struct {
	Category   string   `json:"category"`
	Severity   string   `json:"severity"`
	State      string   `json:"state"`
	Location   string   `json:"location"`
	Trigger    string   `json:"trigger"`
	Impact     string   `json:"impact"`
	ProofRefs  []string `json:"proof_refs"`
	Causal     string   `json:"causal"`
	Resolution string   `json:"resolution"`
}

type ClaimsV3 struct {
	Version          int       `json:"version"`
	Lens             string    `json:"lens"`
	Status           string    `json:"status"`
	Summary          string    `json:"summary"`
	Findings         []ClaimV3 `json:"findings"`
	Limitations      []string  `json:"limitations"`
	ReviewedRevision string    `json:"reviewed_revision"`
	Evidence         []string  `json:"evidence"`
	Visual           *Visual   `json:"visual,omitempty"`
}

// Proof is populated by the runtime evidence resolver, never unmarshaled from
// model output. Causal is set only by verified base/candidate comparison.
type Proof struct {
	Revision     string
	Kind         string
	Causal       string
	BaseRevision string
	Verified     bool
	// Subject binds a runtime check comparison to exactly one declared check.
	// It cannot establish causality for an unrelated code-level hypothesis.
	Subject     string
	CheckPassed bool
}
type ProofResolver func(ref string) (Proof, error)

type FindingV3 struct {
	ClaimV3
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type ResultV3 struct {
	Version          int         `json:"version"`
	Status           string      `json:"status"`
	Decision         string      `json:"decision"`
	Approved         bool        `json:"approved"`
	Summary          string      `json:"summary"`
	Findings         []FindingV3 `json:"findings"`
	Limitations      []string    `json:"limitations"`
	ReviewedRevision string      `json:"reviewed_revision"`
	Evidence         []string    `json:"evidence"`
	Followups        []string    `json:"followups"`
	NeedsDecision    []string    `json:"needs_decision"`
	Visual           *Visual     `json:"visual,omitempty"`
	Digest           string      `json:"digest"`
}

func DecodeClaimsV3(raw []byte) (ClaimsV3, error) {
	var c ClaimsV3
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return c, fmt.Errorf("review-result@3: trailing JSON")
	}
	return c, c.Validate()
}

func (c ClaimsV3) Validate() error {
	if c.Version != 3 || !slices.Contains([]string{"complete", "partial", "unavailable", "not_applicable"}, c.Status) || strings.TrimSpace(c.Summary) == "" || c.Findings == nil || c.Limitations == nil || c.Evidence == nil {
		return fmt.Errorf("review-result@3 requires version 3, valid status, summary and explicit arrays")
	}
	if !slices.Contains([]string{"structural", "risk", "resilience", "readability", "reliability"}, c.Lens) {
		return fmt.Errorf("review-result@3 requires a valid lens")
	}
	complete := c.Status == "complete" || c.Status == "not_applicable"
	if complete && c.ReviewedRevision == "" {
		return fmt.Errorf("complete review requires reviewed_revision")
	}
	if !complete && len(c.Limitations) == 0 {
		return fmt.Errorf("incomplete review requires limitations")
	}
	if c.Status == "not_applicable" && (len(c.Findings) > 0 || len(c.Evidence) == 0 || len(c.Limitations) == 0) {
		return fmt.Errorf("not_applicable requires evidence, justification and no findings")
	}
	for _, f := range c.Findings {
		if _, ok := severityRank[f.Severity]; !ok {
			return fmt.Errorf("invalid finding severity %q", f.Severity)
		}
		if !slices.Contains([]string{"open", "resolved", "refuted"}, f.State) || !slices.Contains([]string{"introduced", "preexisting", "unknown"}, f.Causal) {
			return fmt.Errorf("finding requires valid state and causal")
		}
		if strings.TrimSpace(f.Category) == "" || strings.TrimSpace(f.Location) == "" || strings.TrimSpace(f.Trigger) == "" || strings.TrimSpace(f.Impact) == "" || len(f.ProofRefs) == 0 {
			return fmt.Errorf("finding requires category, location, trigger, impact and proof_refs")
		}
		if f.State != "open" && strings.TrimSpace(f.Resolution) == "" {
			return fmt.Errorf("resolved/refuted finding requires resolution")
		}
	}
	return nil
}

// EvaluateV3 does not accept the model's causal attribution as authority. A
// finding remains unknown unless a trusted comparison proves its attribution.
func EvaluateV3(c ClaimsV3, revision, base string, resolve ProofResolver, previous []ResultV3) (ResultV3, error) {
	if err := c.Validate(); err != nil {
		return ResultV3{}, err
	}
	if revision == "" || base == "" || resolve == nil {
		return ResultV3{}, fmt.Errorf("runtime revision, base and proof resolver required")
	}
	if (c.Status == "complete" || c.Status == "not_applicable") && c.ReviewedRevision != revision {
		return ResultV3{}, fmt.Errorf("reviewed revision differs from frozen candidate")
	}
	r := ResultV3{Version: 3, Status: c.Status, Summary: c.Summary, Findings: []FindingV3{}, Limitations: slices.Clone(c.Limitations), ReviewedRevision: revision, Evidence: []string{}, Followups: []string{}, NeedsDecision: []string{}, Visual: c.Visual}
	historical := map[string]string{}
	for _, prior := range previous {
		for _, f := range prior.Findings {
			for _, ref := range f.ProofRefs {
				historical[ref] = prior.ReviewedRevision
			}
		}
	}
	verify := func(ref string) (Proof, error) {
		p, err := resolve(ref)
		if err != nil {
			return p, err
		}
		if !p.Verified || p.Kind == "memory" || (p.Revision != revision && p.Revision != base && historical[ref] != p.Revision) {
			return p, fmt.Errorf("unverified or unrelated proof %q", ref)
		}
		return p, nil
	}
	for _, ref := range c.Evidence {
		if _, err := verify(ref); err != nil {
			return r, err
		}
		r.Evidence = append(r.Evidence, ref)
	}
	for _, claim := range c.Findings {
		causal := "unknown"
		conflict := false
		currentProof := false
		checkResolution := false
		for _, ref := range claim.ProofRefs {
			p, err := verify(ref)
			if err != nil {
				return r, err
			}
			currentProof = currentProof || p.Revision == revision
			checkResolution = checkResolution || p.Revision == revision && p.Kind == "check_comparison" && p.Subject == claim.Trigger && p.CheckPassed
			if p.Kind == "check_comparison" && p.Subject != "" && claim.Category == "runtime_check" && claim.Trigger == p.Subject && p.BaseRevision == base && p.Revision == revision && (p.Causal == "introduced" || p.Causal == "preexisting") {
				if causal != "unknown" && causal != p.Causal {
					conflict = true
				}
				causal = p.Causal
			}
		}
		if claim.State != "open" && (!currentProof || claim.Category == "runtime_check" && !checkResolution) {
			return r, fmt.Errorf("resolved/refuted finding requires new candidate evidence; runtime checks require a passing comparison")
		}
		if conflict {
			causal = "unknown"
		}
		claim.Causal = causal
		claim.ProofRefs = sortedUnique(claim.ProofRefs)
		identity, _ := json.Marshal([]string{claim.Category, claim.Location, strings.Join(strings.Fields(claim.Trigger), " ")})
		id := hash(identity)
		body, _ := json.Marshal(claim)
		f := FindingV3{ClaimV3: claim, ID: "finding-" + id, Digest: hash(body)}
		if slices.ContainsFunc(r.Findings, func(old FindingV3) bool { return old.ID == f.ID }) {
			return r, fmt.Errorf("duplicate finding identity %s", f.ID)
		}
		r.Findings = append(r.Findings, f)
	}
	slices.SortFunc(r.Findings, func(a, b FindingV3) int { return strings.Compare(a.ID, b.ID) })
	complete := c.Status == "complete" || c.Status == "not_applicable"
	blocking := false
	open := false
	for _, f := range r.Findings {
		if f.State != "open" {
			continue
		}
		if f.Causal == "preexisting" {
			r.Followups = append(r.Followups, f.ID)
			continue
		}
		if f.Causal == "unknown" {
			r.NeedsDecision = append(r.NeedsDecision, f.ID)
			continue
		}
		open = true
		if f.Severity != "low" {
			blocking = true
		}
	}
	r.Decision = "approve"
	if open {
		r.Decision = "warn"
	}
	if !complete || len(r.NeedsDecision) > 0 {
		r.Decision = "inconclusive"
	}
	if blocking {
		r.Decision = "block"
	}
	r.Approved = complete && r.Decision == "approve"
	for _, prior := range previous {
		if r.Approved && prior.Status != "complete" && prior.Status != "not_applicable" {
			return r, fmt.Errorf("cannot approve with incomplete upstream review")
		}
		for _, old := range prior.Findings {
			if old.State != "open" {
				continue
			}
			i := slices.IndexFunc(r.Findings, func(f FindingV3) bool { return f.ID == old.ID })
			if i < 0 {
				return r, fmt.Errorf("missing historical finding %s", old.ID)
			}
			f := r.Findings[i]
			for _, ref := range old.ProofRefs {
				if !slices.Contains(f.ProofRefs, ref) {
					return r, fmt.Errorf("lost historical proof %q", ref)
				}
			}
			if f.State == "open" && severityRank[f.Severity] < severityRank[old.Severity] {
				return r, fmt.Errorf("silently downgraded finding %s", old.ID)
			}
		}
	}
	// Retain the existing visual coverage invariants without treating followups
	// as new product blockers.
	if c.Visual != nil {
		legacy := Result{Status: r.Status, Approved: r.Approved, Limitations: r.Limitations, Visual: c.Visual}
		var problems []string
		validateVisual(legacy, func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) })
		if len(problems) > 0 {
			return r, fmt.Errorf("visual review: %s", strings.Join(problems, "; "))
		}
	}
	raw, _ := json.Marshal(r)
	r.Digest = hash(raw)
	return r, nil
}

func sortedUnique(values []string) []string {
	v := slices.Clone(values)
	slices.Sort(v)
	return slices.Compact(v)
}
func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
