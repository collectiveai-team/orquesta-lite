// Package review defines the versioned, semantic review contract.
package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type Finding struct {
	ID         string   `json:"id"`
	Category   string   `json:"category"`
	Severity   string   `json:"severity"`
	State      string   `json:"state"`
	Location   string   `json:"location"`
	Trigger    string   `json:"trigger"`
	Impact     string   `json:"impact"`
	Evidence   []string `json:"evidence"`
	Origins    []string `json:"origins"`
	Resolution string   `json:"resolution"`
}

type Visual struct {
	Requested string   `json:"requested"`
	Executed  string   `json:"executed"`
	Required  []string `json:"required"`
	Checked   []string `json:"checked"`
	Pending   []string `json:"pending"`
	Reason    string   `json:"reason"`
	URL       string   `json:"url"`
	Artifacts []string `json:"artifacts"`
}

type Result struct {
	Version          int       `json:"version"`
	Status           string    `json:"status"`
	Decision         string    `json:"decision"`
	Approved         bool      `json:"approved"`
	Summary          string    `json:"summary"`
	Findings         []Finding `json:"findings"`
	Limitations      []string  `json:"limitations"`
	ReviewedRevision string    `json:"reviewed_revision"`
	Evidence         []string  `json:"evidence"`
	Visual           *Visual   `json:"visual,omitempty"`
}

func Decode(raw []byte) (Result, error) {
	var r Result
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	return r, r.Validate()
}

func (r Result) Validate() error {
	bad := func(s string) error { return fmt.Errorf("review-result@2: %s", s) }
	if r.Version != 2 || !slices.Contains([]string{"complete", "partial", "unavailable", "not_applicable"}, r.Status) || !slices.Contains([]string{"approve", "warn", "block", "inconclusive"}, r.Decision) {
		return bad("invalid version/status/decision")
	}
	if strings.TrimSpace(r.Summary) == "" || r.Findings == nil || r.Limitations == nil || r.Evidence == nil {
		return bad("summary and explicit arrays required")
	}
	complete := r.Status == "complete" || r.Status == "not_applicable"
	if r.Approved != (complete && r.Decision == "approve") {
		return bad("approved must agree with status and decision")
	}
	if !complete && (r.Decision == "approve" || r.Decision == "warn" || len(r.Limitations) == 0) {
		return bad("incomplete review requires limitations and inconclusive/block")
	}
	if complete && strings.TrimSpace(r.ReviewedRevision) == "" {
		return bad("completed review requires revision evidence")
	}
	if r.Status == "not_applicable" && (len(r.Evidence) == 0 || len(r.Limitations) == 0 || len(r.Findings) > 0) {
		return bad("not_applicable requires scope evidence, justification and no defects")
	}
	seen := map[string]bool{}
	blocking := false
	open := false
	for _, f := range r.Findings {
		if f.ID == "" || seen[f.ID] || f.Category == "" || f.Location == "" || f.Trigger == "" || f.Impact == "" || len(f.Evidence) == 0 || len(f.Origins) == 0 {
			return bad("finding requires unique ID, scenario, impact, evidence and origins")
		}
		seen[f.ID] = true
		if !slices.Contains([]string{"critical", "high", "medium", "low"}, f.Severity) || !slices.Contains([]string{"open", "resolved", "refuted"}, f.State) {
			return bad("invalid finding severity/state")
		}
		if f.State != "open" && strings.TrimSpace(f.Resolution) == "" {
			return bad("changed finding state requires resolution evidence")
		}
		if f.State == "open" {
			open = true
			if f.Severity != "low" {
				blocking = true
			}
		}
	}
	if blocking && r.Decision != "block" {
		return bad("open blocking defect cannot disappear from decision")
	}
	if r.Decision == "block" && !blocking {
		return bad("block requires confirmed blocking defect; missing evidence is inconclusive")
	}
	if r.Decision == "approve" && open {
		return bad("approval cannot carry open defects")
	}
	if r.Visual != nil {
		v := r.Visual
		if !slices.Contains([]string{"browser", "static", "not_applicable"}, v.Requested) || !slices.Contains([]string{"browser", "static", "not_applicable", "unavailable"}, v.Executed) {
			return bad("invalid visual mode")
		}
		missing := false
		for _, c := range v.Required {
			if !slices.Contains(v.Checked, c) {
				missing = true
				if !slices.Contains(v.Pending, c) {
					return bad("unchecked criterion must be pending")
				}
			}
		}
		degraded := (v.Requested == "browser" && v.Executed != "browser") || missing || len(v.Pending) > 0
		if degraded && (r.Status == "complete" || r.Approved || v.Reason == "" || len(r.Limitations) == 0) {
			return bad("degraded visual review must be incomplete with reason and limitations")
		}
		if v.Executed == "not_applicable" && (r.Status != "not_applicable" || v.Reason == "") {
			return bad("visual not_applicable needs justified scope")
		}
	}
	return nil
}

// Preserve checks historical identity. A reviewer may resolve/refute a defect
// explicitly with evidence, but cannot silently omit it or erase its sources.
func Preserve(current Result, previous []Result) error {
	for _, prior := range previous {
		if current.Approved && prior.Status != "complete" && prior.Status != "not_applicable" {
			return fmt.Errorf("cannot approve with incomplete required review")
		}
		for _, old := range prior.Findings {
			if old.State != "open" || old.Severity == "low" {
				continue
			}
			i := slices.IndexFunc(current.Findings, func(f Finding) bool { return f.ID == old.ID })
			if i < 0 {
				return fmt.Errorf("missing upstream blocking finding %s", old.ID)
			}
			f := current.Findings[i]
			for _, origin := range old.Origins {
				if !slices.Contains(f.Origins, origin) {
					return fmt.Errorf("lost origin for %s", old.ID)
				}
			}
			for _, e := range old.Evidence {
				if !slices.Contains(f.Evidence, e) {
					return fmt.Errorf("lost evidence for %s", old.ID)
				}
			}
			if f.State == "open" && old.Severity != "low" && f.Severity == "low" {
				return fmt.Errorf("silently downgraded blocking finding %s", old.ID)
			}
		}
	}
	return nil
}

// Aggregate keeps duplicate IDs as one defect with all evidence and sources.
// Conflicting claims require a new review; last-writer-wins would hide bugs.
func Aggregate(reviews []Result) ([]Finding, bool, error) {
	if len(reviews) == 0 {
		return nil, false, fmt.Errorf("required reviews cannot be empty")
	}
	findings := []Finding{}
	ready := true
	for _, r := range reviews {
		if err := r.Validate(); err != nil {
			return nil, false, err
		}
		ready = ready && (r.Status == "complete" || r.Status == "not_applicable")
		for _, f := range r.Findings {
			i := slices.IndexFunc(findings, func(old Finding) bool { return old.ID == f.ID })
			if i < 0 {
				findings = append(findings, f)
				continue
			}
			old := &findings[i]
			if old.State != f.State || old.Severity != f.Severity || old.Trigger != f.Trigger || old.Impact != f.Impact {
				return nil, false, fmt.Errorf("conflicting duplicate finding %s", f.ID)
			}
			for _, x := range f.Origins {
				if !slices.Contains(old.Origins, x) {
					old.Origins = append(old.Origins, x)
				}
			}
			for _, x := range f.Evidence {
				if !slices.Contains(old.Evidence, x) {
					old.Evidence = append(old.Evidence, x)
				}
			}
		}
	}
	return findings, ready, nil
}
